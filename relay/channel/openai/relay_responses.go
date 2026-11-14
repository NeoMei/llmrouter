package openai

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/logger"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/relay/toolpolicy"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/service/responsesstate"
	"github.com/QuantumNous/new-api/types"

	"github.com/gin-gonic/gin"
)

func OaiResponsesHandler(c *gin.Context, info *relaycommon.RelayInfo, resp *http.Response) (*dto.Usage, *types.NewAPIError) {
	nativeEvidence, finishNativeEvidence := service.NativeTokenObserver(c.Request.Context(), info, dto.BillingUsageSourceOAIResponses)
	defer finishNativeEvidence()
	defer service.CloseResponseBodyGracefully(resp)

	// read response body
	var responsesResponse dto.OpenAIResponsesResponse
	reader := io.Reader(resp.Body)
	if info != nil && info.PlatformBilling != nil {
		reader = io.LimitReader(resp.Body, 16<<20+1)
	}
	responseBody, err := io.ReadAll(reader)
	if info != nil && info.PlatformBilling != nil && len(responseBody) > 16<<20 {
		return nil, types.NewError(fmt.Errorf("managed response exceeds completion bound"), types.ErrorCodeBadResponseBody, types.ErrOptionWithSkipRetry())
	}
	nativeEvidence.Observe(responseBody, false)
	if err != nil {
		return nil, types.NewOpenAIError(err, types.ErrorCodeReadResponseBodyFailed, http.StatusInternalServerError)
	}
	if err := toolpolicy.ValidateRawResponseJSON(responseBody); err != nil {
		return nil, toolpolicy.NewResponseValidationError(err)
	}
	err = common.Unmarshal(responseBody, &responsesResponse)
	if err != nil {
		return nil, types.NewOpenAIError(err, types.ErrorCodeBadResponseBody, http.StatusInternalServerError)
	}
	if oaiError := responsesResponse.GetOpenAIError(); oaiError != nil && oaiError.Type != "" {
		// A generated response can fail after DeepSeek has already billed its
		// input, output, and server tools. Preserve that terminal usage so the
		// caller can settle it even though this request returns an error.
		return responsesUsage(responsesResponse.Usage), types.WithOpenAIError(*oaiError, resp.StatusCode)
	}
	if err := toolpolicy.ValidateResponsesResponseContext(c, &responsesResponse); err != nil {
		return nil, toolpolicy.NewResponseValidationError(err)
	}

	if responsesResponse.HasImageGenerationCall() {
		c.Set("image_generation_call", true)
		c.Set("image_generation_call_quality", responsesResponse.GetQuality())
		c.Set("image_generation_call_size", responsesResponse.GetSize())
	}

	if info != nil && info.PlatformBilling != nil {
		if err := info.PlatformBilling.RegisterNativeCompletion(c.Request.Context(), responseBody); err != nil {
			return nil, toolpolicy.NewResponseValidationError(err)
		}
	}
	// Bill protocol output items first, then let the vendor's terminal counts
	// replace the same keys. Applying vendor counts before these protocol
	// counts would double-bill a completed Search or Fetch.
	for _, item := range responsesResponse.Output {
		switch item.Type {
		case dto.BuildInCallWebSearchCall, dto.BuildInToolFileSearch:
			info.CountBillableToolCall(item.Type)
		}
	}
	info.ApplyVendorToolUsage(responseBody)
	persistResponsesTurn(c, info, &responsesResponse)

	// 写入新的 response body
	service.IOCopyBytesGracefully(c, resp, responseBody)

	return responsesUsage(responsesResponse.Usage), nil
}

func responsesUsage(source *dto.Usage) *dto.Usage {
	usage := &dto.Usage{}
	if source == nil {
		return usage
	}
	usage.PromptTokens = source.InputTokens
	usage.CompletionTokens = source.OutputTokens
	usage.TotalTokens = source.TotalTokens
	if source.InputTokensDetails != nil {
		usage.PromptTokensDetails = *source.InputTokensDetails
	}
	if source.OutputTokensDetails != nil {
		usage.CompletionTokenDetails = *source.OutputTokensDetails
		usage.OutputTokensDetails = source.OutputTokensDetails
	}
	return usage
}

func OaiResponsesStreamHandler(c *gin.Context, info *relaycommon.RelayInfo, resp *http.Response) (*dto.Usage, *types.NewAPIError) {
	nativeEvidence, finishNativeEvidence := service.NativeTokenObserver(c.Request.Context(), info, dto.BillingUsageSourceOAIResponses)
	defer finishNativeEvidence()
	if resp == nil || resp.Body == nil {
		logger.LogError(c, "invalid response or response body")
		return nil, types.NewError(fmt.Errorf("invalid response"), types.ErrorCodeBadResponse)
	}

	defer service.CloseResponseBodyGracefully(resp)

	var usage = &dto.Usage{}
	var responseTextBuilder strings.Builder
	terminalSeen := false
	terminalValid := false
	var terminalResponse *dto.OpenAIResponsesResponse
	var failedTerminalUsage *dto.Usage
	streamValidator := toolpolicy.NewStreamValidatorFromContext(c)
	var streamErr *types.NewAPIError
	stopStream := func(sr *helper.StreamResult) {
		if sr != nil {
			sr.Stop(streamErr)
		}
	}

	handleValidatedData := func(data string, sr *helper.StreamResult) bool {
		// 检查当前数据是否包含 completed 状态和 usage 信息
		var streamResponse dto.ResponsesStreamResponse
		if err := common.UnmarshalJsonStr(data, &streamResponse); err != nil {
			logger.LogError(c, "failed to unmarshal stream response: "+err.Error())
			streamErr = toolpolicy.NewResponseValidationError(fmt.Errorf("%w: invalid responses stream frame: %v", toolpolicy.ErrMalformedToolCall, err))
			stopStream(sr)
			return false
		}
		wasTerminalSeen := terminalSeen
		if wasTerminalSeen {
			terminalValid = false
			terminalResponse = nil
		}
		if info != nil && info.PlatformBilling != nil && (streamResponse.Type == "response.completed" || streamResponse.Type == "response.done") {
			var envelope map[string]json.RawMessage
			if common.UnmarshalUnique([]byte(data), &envelope) != nil {
				streamErr = toolpolicy.NewResponseValidationError(fmt.Errorf("invalid managed terminal JSON"))
				stopStream(sr)
				return false
			}
			if err := info.PlatformBilling.RegisterNativeCompletion(c.Request.Context(), envelope["response"]); err != nil {
				streamErr = toolpolicy.NewResponseValidationError(err)
				stopStream(sr)
				return false
			}
		}
		if err := helper.ResponseChunkData(c, streamResponse, data); err != nil {
			streamErr = types.NewOpenAIError(err, types.ErrorCodeBadResponseBody, http.StatusBadGateway, types.ErrOptionWithSkipRetry())
			if info.StreamStatus != nil {
				info.StreamStatus.RecordError(err.Error())
			}
			stopStream(sr)
			return false
		}
		switch streamResponse.Type {
		case "response.completed", "response.done":
			terminalSeen = true
			if streamResponse.Response != nil {
				if !wasTerminalSeen && responsesstate.EligibleCompletedResponse(streamResponse.Response) {
					terminalValid = true
					terminalResponse = streamResponse.Response
				}
				if streamResponse.Response.Usage != nil {
					if streamResponse.Response.Usage.InputTokens != 0 {
						usage.PromptTokens = streamResponse.Response.Usage.InputTokens
					}
					if streamResponse.Response.Usage.OutputTokens != 0 {
						usage.CompletionTokens = streamResponse.Response.Usage.OutputTokens
					}
					if streamResponse.Response.Usage.TotalTokens != 0 {
						usage.TotalTokens = streamResponse.Response.Usage.TotalTokens
					}
					if streamResponse.Response.Usage.InputTokensDetails != nil {
						usage.PromptTokensDetails = *streamResponse.Response.Usage.InputTokensDetails
					}
				}
				if streamResponse.Response.Usage != nil && streamResponse.Response.Usage.OutputTokensDetails != nil {
					usage.CompletionTokenDetails = *streamResponse.Response.Usage.OutputTokensDetails
				}
				if streamResponse.Response.HasImageGenerationCall() {
					c.Set("image_generation_call", true)
					c.Set("image_generation_call_quality", streamResponse.Response.GetQuality())
					c.Set("image_generation_call_size", streamResponse.Response.GetSize())
				}
			}
			info.ApplyVendorToolUsage(common.StringToByteSlice(data))
		case "response.failed", "response.error", "response.incomplete", "error":
			terminalSeen = true
			terminalValid = false
			terminalResponse = nil
			if streamResponse.Response != nil {
				applyResponsesStreamUsage(usage, streamResponse.Response.Usage)
			}
			info.ApplyVendorToolUsage(common.StringToByteSlice(data))
		case "response.output_text.delta":
			// 处理输出文本
			responseTextBuilder.WriteString(streamResponse.Delta)
		case dto.ResponsesOutputTypeItemDone:
			// 函数调用处理
			if streamResponse.Item != nil {
				switch streamResponse.Item.Type {
				case dto.BuildInCallWebSearchCall:
					info.CountBillableToolCall(dto.BuildInCallWebSearchCall)
				case dto.BuildInToolFileSearch:
					info.CountBillableToolCall(dto.BuildInToolFileSearch)
				}
			}
		}
		return true
	}

	helper.StreamScannerHandler(c, resp, info, func(data string, sr *helper.StreamResult) {
		nativeEvidence.Observe([]byte(data), true)
		if streamErr != nil {
			sr.Stop(streamErr)
			return
		}
		captureResponsesFailedTerminalUsage(data, info, &failedTerminalUsage)
		emitted, err := streamValidator.Accept(common.StringToByteSlice(data))
		if err != nil {
			streamErr = toolpolicy.NewResponseValidationError(err)
			sr.Stop(streamErr)
			return
		}
		for _, frame := range emitted {
			if !handleValidatedData(string(frame), sr) {
				return
			}
		}
	})
	if streamErr != nil {
		return failedTerminalUsage, streamErr
	}
	// A clean upstream EOF is a legitimate Responses transport boundary. The
	// validator still requires a valid, ordered success terminal before release.
	if info.StreamStatus.EndReason != relaycommon.StreamEndReasonEOF || info.StreamStatus.HasErrors() || c.Request.Context().Err() != nil {
		if abortErr := abortInvalidStreamTransport(c, info, streamValidator); abortErr != nil {
			return failedTerminalUsage, abortErr
		}
	}
	flushed, err := streamValidator.Finish()
	if err != nil {
		return failedTerminalUsage, toolpolicy.NewResponseValidationError(err)
	}
	for _, frame := range flushed {
		if !handleValidatedData(string(frame), nil) {
			return nil, streamErr
		}
	}
	if !terminalSeen {
		return failedTerminalUsage, toolpolicy.NewResponseValidationError(fmt.Errorf("%w: responses stream ended without a delivered success terminal", toolpolicy.ErrIncompleteToolCall))
	}

	if terminalValid && terminalResponse != nil && info.StreamStatus != nil && info.StreamStatus.IsNormalEnd() && !info.StreamStatus.HasErrors() && c.Request.Context().Err() == nil {
		persistResponsesTurn(c, info, terminalResponse)
	}

	if usage.CompletionTokens == 0 {
		// 计算输出文本的 token 数量
		tempStr := responseTextBuilder.String()
		if len(tempStr) > 0 {
			// 非正常结束，使用输出文本的 token 数量
			completionTokens := service.CountTextToken(tempStr, info.UpstreamModelName)
			usage.CompletionTokens = completionTokens
		}
	}

	if usage.PromptTokens == 0 && (usage.CompletionTokens != 0 || usage.TotalTokens != 0) {
		usage.PromptTokens = info.GetEstimatePromptTokens()
	}

	usage.TotalTokens = usage.PromptTokens + usage.CompletionTokens

	return usage, nil
}

// captureResponsesFailedTerminalUsage keeps usage from a provider terminal that
// proves generation and server tools ran. The stream validator deliberately
// rejects the failure as a client success, but the provider still bills the
// work. Returning this usage on the error path lets the caller settle it once.
func captureResponsesFailedTerminalUsage(data string, info *relaycommon.RelayInfo, destination **dto.Usage) {
	if info == nil {
		return
	}
	var event dto.ResponsesStreamResponse
	if err := common.UnmarshalJsonStr(data, &event); err != nil || event.Response == nil {
		return
	}
	switch event.Type {
	case "response.failed", "response.incomplete", "response.error":
	default:
		return
	}
	if event.Response.Usage == nil {
		return
	}
	usage := responsesUsage(event.Response.Usage)
	info.ApplyVendorToolUsage(common.StringToByteSlice(data))
	if *destination == nil || usage.TotalTokens != 0 || usage.PromptTokens != 0 || usage.CompletionTokens != 0 {
		*destination = usage
	}
}

func applyResponsesStreamUsage(usage *dto.Usage, source *dto.Usage) {
	if usage == nil || source == nil {
		return
	}
	if source.InputTokens != 0 {
		usage.PromptTokens = source.InputTokens
	}
	if source.OutputTokens != 0 {
		usage.CompletionTokens = source.OutputTokens
	}
	if source.TotalTokens != 0 {
		usage.TotalTokens = source.TotalTokens
	}
	if source.InputTokensDetails != nil {
		usage.PromptTokensDetails = *source.InputTokensDetails
	}
	if source.OutputTokensDetails != nil {
		usage.CompletionTokenDetails = *source.OutputTokensDetails
		usage.OutputTokensDetails = source.OutputTokensDetails
	}
}

// persistResponsesTurn stores a completed Responses turn for later
// previous_response_id replay when stateful continuity is enabled. It is
// best-effort: a nil store, missing response ID, or validation failure simply
// skips persistence without affecting the relay path.
func persistResponsesTurn(c *gin.Context, info *relaycommon.RelayInfo, responsesResponse *dto.OpenAIResponsesResponse) {
	relaycommon.PersistResponsesTurn(c, info, responsesResponse)
}
