package openai

import (
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
	defer service.CloseResponseBodyGracefully(resp)

	// read response body
	var responsesResponse dto.OpenAIResponsesResponse
	responseBody, err := io.ReadAll(resp.Body)
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
		return nil, types.WithOpenAIError(*oaiError, resp.StatusCode)
	}
	if err := toolpolicy.ValidateResponsesResponseContext(c, &responsesResponse); err != nil {
		return nil, toolpolicy.NewResponseValidationError(err)
	}

	if responsesResponse.HasImageGenerationCall() {
		c.Set("image_generation_call", true)
		c.Set("image_generation_call_quality", responsesResponse.GetQuality())
		c.Set("image_generation_call_size", responsesResponse.GetSize())
	}

	persistResponsesTurn(c, info, &responsesResponse)

	// 写入新的 response body
	service.IOCopyBytesGracefully(c, resp, responseBody)

	// compute usage
	usage := dto.Usage{}
	if responsesResponse.Usage != nil {
		usage.PromptTokens = responsesResponse.Usage.InputTokens
		usage.CompletionTokens = responsesResponse.Usage.OutputTokens
		usage.TotalTokens = responsesResponse.Usage.TotalTokens
		if responsesResponse.Usage.InputTokensDetails != nil {
			usage.PromptTokensDetails = *responsesResponse.Usage.InputTokensDetails
		}
		if responsesResponse.Usage.OutputTokensDetails != nil {
			usage.CompletionTokenDetails = *responsesResponse.Usage.OutputTokensDetails
		}
	}
	if info == nil || info.ResponsesUsageInfo == nil || info.ResponsesUsageInfo.BuiltInTools == nil {
		return &usage, nil
	}
	// 解析 Tools 用量
	for _, tool := range responsesResponse.Tools {
		buildToolinfo, ok := info.ResponsesUsageInfo.BuiltInTools[common.Interface2String(tool["type"])]
		if !ok || buildToolinfo == nil {
			logger.LogError(c, fmt.Sprintf("BuiltInTools not found for tool type: %v", tool["type"]))
			continue
		}
		buildToolinfo.CallCount++
	}
	return &usage, nil
}

func OaiResponsesStreamHandler(c *gin.Context, info *relaycommon.RelayInfo, resp *http.Response) (*dto.Usage, *types.NewAPIError) {
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
		case "response.failed", "response.error", "response.incomplete", "error":
			terminalSeen = true
			terminalValid = false
			terminalResponse = nil
		case "response.output_text.delta":
			// 处理输出文本
			responseTextBuilder.WriteString(streamResponse.Delta)
		case dto.ResponsesOutputTypeItemDone:
			// 函数调用处理
			if streamResponse.Item != nil {
				switch streamResponse.Item.Type {
				case dto.BuildInCallWebSearchCall:
					if info != nil && info.ResponsesUsageInfo != nil && info.ResponsesUsageInfo.BuiltInTools != nil {
						if webSearchTool, exists := info.ResponsesUsageInfo.BuiltInTools[dto.BuildInToolWebSearchPreview]; exists && webSearchTool != nil {
							webSearchTool.CallCount++
						}
					}
				}
			}
		}
		return true
	}

	helper.StreamScannerHandler(c, resp, info, func(data string, sr *helper.StreamResult) {
		if streamErr != nil {
			sr.Stop(streamErr)
			return
		}
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
		return nil, streamErr
	}
	// A clean upstream EOF is a legitimate Responses transport boundary. The
	// validator still requires a valid, ordered success terminal before release.
	if info.StreamStatus.EndReason != relaycommon.StreamEndReasonEOF || info.StreamStatus.HasErrors() || c.Request.Context().Err() != nil {
		if abortErr := abortInvalidStreamTransport(c, info, streamValidator); abortErr != nil {
			return nil, abortErr
		}
	}
	flushed, err := streamValidator.Finish()
	if err != nil {
		return nil, toolpolicy.NewResponseValidationError(err)
	}
	for _, frame := range flushed {
		if !handleValidatedData(string(frame), nil) {
			return nil, streamErr
		}
	}
	if !terminalSeen {
		return nil, toolpolicy.NewResponseValidationError(fmt.Errorf("%w: responses stream ended without a delivered success terminal", toolpolicy.ErrIncompleteToolCall))
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

	if usage.PromptTokens == 0 && usage.CompletionTokens != 0 {
		usage.PromptTokens = info.GetEstimatePromptTokens()
	}

	usage.TotalTokens = usage.PromptTokens + usage.CompletionTokens

	return usage, nil
}

// persistResponsesTurn stores a completed Responses turn for later
// previous_response_id replay when stateful continuity is enabled. It is
// best-effort: a nil store, missing response ID, or validation failure simply
// skips persistence without affecting the relay path.
func persistResponsesTurn(c *gin.Context, info *relaycommon.RelayInfo, responsesResponse *dto.OpenAIResponsesResponse) {
	relaycommon.PersistResponsesTurn(c, info, responsesResponse)
}
