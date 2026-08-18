package openai

import (
	"fmt"
	"io"
	"net/http"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/logger"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/relay/toolpolicy"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/service/relayconvert"
	"github.com/QuantumNous/new-api/service/responsesstate"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
)

func OaiChatToResponsesHandler(c *gin.Context, info *relaycommon.RelayInfo, resp *http.Response) (*dto.Usage, *types.NewAPIError) {
	if resp == nil || resp.Body == nil {
		return nil, types.NewOpenAIError(fmt.Errorf("invalid response"), types.ErrorCodeBadResponse, http.StatusInternalServerError)
	}
	defer service.CloseResponseBodyGracefully(resp)

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, types.NewOpenAIError(err, types.ErrorCodeReadResponseBodyFailed, http.StatusInternalServerError)
	}
	if err := toolpolicy.ValidateRawResponseJSON(body); err != nil {
		return nil, toolpolicy.NewResponseValidationError(err)
	}

	var chatResp dto.OpenAITextResponse
	if err := common.Unmarshal(body, &chatResp); err != nil {
		return nil, types.NewOpenAIError(err, types.ErrorCodeBadResponseBody, http.StatusInternalServerError)
	}
	if oaiError := chatResp.GetOpenAIError(); oaiError != nil && oaiError.Type != "" {
		return nil, types.WithOpenAIError(*oaiError, resp.StatusCode)
	}
	if err := toolpolicy.ValidateChatResponseContext(c, &chatResp); err != nil {
		return nil, toolpolicy.NewResponseValidationError(err)
	}

	if responseID := helper.GetResponseID(c); responseID != "" {
		chatResp.Id = responseID
	}
	convertResult, err := relayconvert.ConvertResponse(c, info, types.RelayFormatOpenAIResponses, &chatResp)
	if err != nil {
		return nil, types.NewOpenAIError(err, types.ErrorCodeBadResponseBody, http.StatusInternalServerError)
	}
	responsesResp, ok := convertResult.Value.(*dto.OpenAIResponsesResponse)
	if !ok {
		return nil, types.NewOpenAIError(fmt.Errorf("expected OpenAI responses response, got %T", convertResult.Value), types.ErrorCodeBadResponseBody, http.StatusInternalServerError)
	}
	usage := convertResult.Usage
	if usage == nil || usage.TotalTokens == 0 {
		text := service.ExtractOutputTextFromResponses(responsesResp)
		usage = service.ResponseText2Usage(c, text, info.UpstreamModelName, info.GetEstimatePromptTokens())
		responsesResp.Usage = relayconvert.UsageFromChatUsage(usage)
	}

	responseBody, err := common.Marshal(responsesResp)
	if err != nil {
		return nil, types.NewOpenAIError(err, types.ErrorCodeJsonMarshalFailed, http.StatusInternalServerError)
	}

	persistResponsesTurn(c, info, responsesResp)
	service.IOCopyBytesGracefully(c, resp, responseBody)
	return usage, nil
}

func OaiChatToResponsesStreamHandler(c *gin.Context, info *relaycommon.RelayInfo, resp *http.Response) (*dto.Usage, *types.NewAPIError) {
	if resp == nil || resp.Body == nil {
		return nil, types.NewOpenAIError(fmt.Errorf("invalid response"), types.ErrorCodeBadResponse, http.StatusInternalServerError)
	}
	defer service.CloseResponseBodyGracefully(resp)

	responseID := helper.GetResponseID(c)
	state, err := relayconvert.NewResponseStreamState(types.RelayFormatOpenAI, types.RelayFormatOpenAIResponses, relayconvert.ResponseStreamOptions{
		ID:    responseID,
		Model: info.UpstreamModelName,
	})
	if err != nil {
		return nil, types.NewOpenAIError(err, types.ErrorCodeBadResponse, http.StatusInternalServerError)
	}
	streamErr := (*types.NewAPIError)(nil)
	streamValidator := toolpolicy.NewStreamValidatorFromContext(c)
	stopStream := func(sr *helper.StreamResult) {
		if sr != nil {
			sr.Stop(streamErr)
		}
	}
	terminalSeen := false
	terminalValid := false
	var terminalResponse *dto.OpenAIResponsesResponse

	sendEvent := func(event relayconvert.ChatToResponsesStreamEvent) bool {
		if terminalSeen {
			terminalValid = false
			terminalResponse = nil
		}
		switch event.Type {
		case "response.completed":
			if !terminalSeen {
				terminalSeen = true
				terminalValid = responsesstate.EligibleCompletedResponse(event.Payload.Response)
				if terminalValid {
					terminalResponse = event.Payload.Response
				}
			}
		case "response.failed", "response.error", "response.incomplete", "error":
			terminalSeen = true
			terminalValid = false
			terminalResponse = nil
		}
		data, err := common.Marshal(event.Payload)
		if err != nil {
			streamErr = types.NewOpenAIError(err, types.ErrorCodeJsonMarshalFailed, http.StatusInternalServerError)
			return false
		}
		helper.ResponseChunkData(c, dto.ResponsesStreamResponse{Type: event.Type}, string(data))
		return true
	}

	convertValidatedData := func(data string, sr *helper.StreamResult) bool {
		if streamErr != nil {
			stopStream(sr)
			return false
		}

		var errorResp dto.OpenAITextResponse
		if err := common.UnmarshalJsonStr(data, &errorResp); err == nil {
			if oaiError := errorResp.GetOpenAIError(); oaiError != nil && oaiError.Type != "" {
				streamErr = types.WithOpenAIError(*oaiError, resp.StatusCode)
				stopStream(sr)
				return false
			}
		}

		var chunk dto.ChatCompletionsStreamResponse
		if err := common.UnmarshalJsonStr(data, &chunk); err != nil {
			logger.LogError(c, "failed to unmarshal chat stream response: "+err.Error())
			streamErr = toolpolicy.NewResponseValidationError(fmt.Errorf("%w: invalid chat stream frame: %v", toolpolicy.ErrMalformedToolCall, err))
			stopStream(sr)
			return false
		}

		results, err := relayconvert.ConvertStreamResponseChunk(c, info, state, &chunk)
		if err != nil {
			streamErr = types.NewOpenAIError(err, types.ErrorCodeBadResponse, http.StatusInternalServerError)
			stopStream(sr)
			return false
		}
		for _, result := range results {
			event, ok := result.Value.(relayconvert.ChatToResponsesStreamEvent)
			if !ok {
				streamErr = types.NewOpenAIError(fmt.Errorf("expected OAI responses stream event, got %T", result.Value), types.ErrorCodeBadResponse, http.StatusInternalServerError)
				stopStream(sr)
				return false
			}
			if !sendEvent(event) {
				stopStream(sr)
				return false
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
			if !convertValidatedData(string(frame), sr) {
				return
			}
		}
	})

	if streamErr != nil {
		return nil, streamErr
	}
	flushed, err := streamValidator.Finish()
	if err != nil {
		return nil, toolpolicy.NewResponseValidationError(err)
	}
	for _, frame := range flushed {
		if !convertValidatedData(string(frame), nil) {
			return nil, streamErr
		}
	}

	usage := state.Usage()
	if usage == nil || usage.TotalTokens == 0 {
		usage = service.ResponseText2Usage(c, state.UsageText(), info.UpstreamModelName, info.GetEstimatePromptTokens())
		state.SetUsage(usage)
	}

	finalResults, err := relayconvert.FinalizeStreamResponse(c, info, state)
	if err != nil {
		return nil, types.NewOpenAIError(err, types.ErrorCodeBadResponse, http.StatusInternalServerError)
	}
	for _, result := range finalResults {
		event, ok := result.Value.(relayconvert.ChatToResponsesStreamEvent)
		if !ok {
			return nil, types.NewOpenAIError(fmt.Errorf("expected OAI responses stream event, got %T", result.Value), types.ErrorCodeBadResponse, http.StatusInternalServerError)
		}
		if !sendEvent(event) {
			return nil, streamErr
		}
	}
	if terminalValid && terminalResponse != nil && info.StreamStatus != nil && info.StreamStatus.IsNormalEnd() && !info.StreamStatus.HasErrors() && c.Request.Context().Err() == nil {
		persistResponsesTurn(c, info, terminalResponse)
	}

	return usage, nil
}
