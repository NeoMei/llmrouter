package deepseek

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/relay/channel"
	"github.com/QuantumNous/new-api/relay/channel/claude"
	"github.com/QuantumNous/new-api/relay/channel/openai"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/setting/reasoning"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
)

type Adaptor struct {
}

const responsesCustomToolsContextKey = "deepseek_responses_custom_tools"

func (a *Adaptor) ConvertGeminiRequest(*gin.Context, *relaycommon.RelayInfo, *dto.GeminiChatRequest) (any, error) {
	//TODO implement me
	return nil, errors.New("not implemented")
}

func (a *Adaptor) ConvertClaudeRequest(c *gin.Context, info *relaycommon.RelayInfo, req *dto.ClaudeRequest) (any, error) {
	adaptor := claude.Adaptor{}
	convertedRequest, err := adaptor.ConvertClaudeRequest(c, info, req)
	if err != nil {
		return nil, err
	}
	claudeRequest, ok := convertedRequest.(*dto.ClaudeRequest)
	if !ok {
		return convertedRequest, nil
	}
	if err := applyDeepSeekV4ClaudeThinkingSuffix(info, claudeRequest); err != nil {
		return nil, err
	}
	return claudeRequest, nil
}

func (a *Adaptor) ConvertAudioRequest(c *gin.Context, info *relaycommon.RelayInfo, request dto.AudioRequest) (io.Reader, error) {
	//TODO implement me
	return nil, errors.New("not implemented")
}

func (a *Adaptor) ConvertImageRequest(c *gin.Context, info *relaycommon.RelayInfo, request dto.ImageRequest) (any, error) {
	//TODO implement me
	return nil, errors.New("not implemented")
}

func (a *Adaptor) Init(info *relaycommon.RelayInfo) {
}

func (a *Adaptor) GetRequestURL(info *relaycommon.RelayInfo) (string, error) {
	if info.RelayMode == constant.RelayModeResponses {
		return fmt.Sprintf("%s/v1/responses", info.ChannelBaseUrl), nil
	}
	fimBaseUrl := info.ChannelBaseUrl
	switch info.RelayFormat {
	case types.RelayFormatClaude:
		return fmt.Sprintf("%s/anthropic/v1/messages", info.ChannelBaseUrl), nil
	default:
		if !strings.HasSuffix(info.ChannelBaseUrl, "/beta") {
			fimBaseUrl += "/beta"
		}
		switch info.RelayMode {
		case constant.RelayModeCompletions:
			return fmt.Sprintf("%s/completions", fimBaseUrl), nil
		default:
			return fmt.Sprintf("%s/v1/chat/completions", info.ChannelBaseUrl), nil
		}
	}
}

func (a *Adaptor) SetupRequestHeader(c *gin.Context, req *http.Header, info *relaycommon.RelayInfo) error {
	channel.SetupApiRequestHeader(info, c, req)
	req.Set("Authorization", "Bearer "+info.ApiKey)
	return nil
}

func (a *Adaptor) ConvertOpenAIRequest(c *gin.Context, info *relaycommon.RelayInfo, request *dto.GeneralOpenAIRequest) (any, error) {
	if request == nil {
		return nil, errors.New("request is nil")
	}
	for i := range request.Messages {
		if request.Messages[i].Role == "developer" {
			request.Messages[i].Role = "system"
		}
	}
	if err := applyDeepSeekV4OpenAIThinkingSuffix(info, request); err != nil {
		return nil, err
	}

	return request, nil
}

func applyDeepSeekV4OpenAIThinkingSuffix(info *relaycommon.RelayInfo, request *dto.GeneralOpenAIRequest) error {
	modelName := request.Model
	if info != nil && info.ChannelMeta != nil && info.UpstreamModelName != "" {
		modelName = info.UpstreamModelName
	}
	baseModel, thinkingType, effort, ok := reasoning.ParseDeepSeekV4ThinkingSuffix(modelName)
	if !ok {
		return nil
	}
	thinking, err := common.Marshal(map[string]string{
		"type": thinkingType,
	})
	if err != nil {
		return fmt.Errorf("error marshalling thinking: %w", err)
	}
	request.Model = baseModel
	request.THINKING = thinking
	request.ReasoningEffort = effort
	if info != nil {
		if info.ChannelMeta != nil {
			info.UpstreamModelName = baseModel
		}
		info.ReasoningEffort = effort
	}
	return nil
}

func applyDeepSeekV4ClaudeThinkingSuffix(info *relaycommon.RelayInfo, request *dto.ClaudeRequest) error {
	modelName := request.Model
	if info != nil && info.ChannelMeta != nil && info.UpstreamModelName != "" {
		modelName = info.UpstreamModelName
	}
	baseModel, thinkingType, effort, ok := reasoning.ParseDeepSeekV4ThinkingSuffix(modelName)
	if !ok {
		return nil
	}
	request.Model = baseModel
	request.Thinking = &dto.Thinking{Type: thinkingType}
	if effort == "" {
		request.OutputConfig = nil
	} else {
		outputConfig, err := common.Marshal(map[string]string{
			"effort": effort,
		})
		if err != nil {
			return fmt.Errorf("error marshalling output_config: %w", err)
		}
		request.OutputConfig = outputConfig
	}
	if info != nil {
		if info.ChannelMeta != nil {
			info.UpstreamModelName = baseModel
		}
		info.ReasoningEffort = effort
	}
	return nil
}

func (a *Adaptor) ConvertRerankRequest(c *gin.Context, relayMode int, request dto.RerankRequest) (any, error) {
	return nil, nil
}

func (a *Adaptor) ConvertEmbeddingRequest(c *gin.Context, info *relaycommon.RelayInfo, request dto.EmbeddingRequest) (any, error) {
	//TODO implement me
	return nil, errors.New("not implemented")
}

func (a *Adaptor) ConvertOpenAIResponsesRequest(c *gin.Context, info *relaycommon.RelayInfo, request dto.OpenAIResponsesRequest) (any, error) {
	customTools, err := promoteResponsesAdditionalTools(&request)
	if err != nil {
		return nil, err
	}
	if c != nil && len(customTools) > 0 {
		c.Set(responsesCustomToolsContextKey, customTools)
	}
	adaptor := openai.Adaptor{}
	return adaptor.ConvertOpenAIResponsesRequest(c, info, request)
}

func promoteResponsesAdditionalTools(request *dto.OpenAIResponsesRequest) (map[string]struct{}, error) {
	customTools := make(map[string]struct{})
	if request == nil || common.GetJsonType(request.Input) != "array" {
		return customTools, nil
	}

	var inputItems []json.RawMessage
	if err := common.Unmarshal(request.Input, &inputItems); err != nil {
		return nil, fmt.Errorf("invalid Responses input: %w", err)
	}
	filteredInput := make([]json.RawMessage, 0, len(inputItems))
	promotedTools := make([]json.RawMessage, 0)
	for _, item := range inputItems {
		var envelope struct {
			Type  string            `json:"type"`
			Tools []json.RawMessage `json:"tools"`
		}
		if err := common.Unmarshal(item, &envelope); err != nil || envelope.Type != "additional_tools" {
			filteredInput = append(filteredInput, item)
			continue
		}
		for _, tool := range envelope.Tools {
			normalized, convertedCustomTools, err := normalizeDeepSeekResponsesTool(tool)
			if err != nil {
				return nil, err
			}
			for _, key := range convertedCustomTools {
				customTools[key] = struct{}{}
			}
			promotedTools = append(promotedTools, normalized)
		}
	}
	if len(promotedTools) == 0 {
		return customTools, nil
	}

	tools := make([]json.RawMessage, 0, len(promotedTools))
	if len(request.Tools) > 0 && common.GetJsonType(request.Tools) != "null" {
		if common.GetJsonType(request.Tools) != "array" {
			return nil, errors.New("invalid Responses tools: expected array")
		}
		if err := common.Unmarshal(request.Tools, &tools); err != nil {
			return nil, fmt.Errorf("invalid Responses tools: %w", err)
		}
	}
	tools = append(tools, promotedTools...)

	encodedTools, err := common.Marshal(tools)
	if err != nil {
		return nil, fmt.Errorf("encode promoted Responses tools: %w", err)
	}
	encodedInput, err := common.Marshal(filteredInput)
	if err != nil {
		return nil, fmt.Errorf("encode filtered Responses input: %w", err)
	}
	request.Tools = encodedTools
	request.Input = encodedInput
	return customTools, nil
}

func normalizeDeepSeekResponsesTool(raw json.RawMessage) (json.RawMessage, []string, error) {
	var tool map[string]any
	if err := common.Unmarshal(raw, &tool); err != nil {
		return nil, nil, fmt.Errorf("invalid Responses tool: %w", err)
	}
	if common.Interface2String(tool["type"]) != "namespace" {
		return raw, nil, nil
	}
	namespace := common.Interface2String(tool["name"])
	innerTools, ok := tool["tools"].([]any)
	if !ok {
		return raw, nil, nil
	}
	customTools := make([]string, 0)
	for _, value := range innerTools {
		inner, ok := value.(map[string]any)
		if !ok || common.Interface2String(inner["type"]) != "custom" {
			continue
		}
		customTools = append(customTools, customToolKey(namespace, common.Interface2String(inner["name"])))
		inner["type"] = "function"
		delete(inner, "format")
		inner["parameters"] = map[string]any{
			"type": "object",
			"properties": map[string]any{
				"input": map[string]any{
					"type":        "string",
					"description": "Raw input for the client-executed tool.",
				},
			},
			"required":             []string{"input"},
			"additionalProperties": false,
		}
	}
	encoded, err := common.Marshal(tool)
	if err != nil {
		return nil, nil, fmt.Errorf("encode normalized Responses tool: %w", err)
	}
	return encoded, customTools, nil
}

func customToolKey(namespace, name string) string {
	return namespace + "\x00" + name
}

func customToolInput(arguments any) string {
	text := common.Interface2String(arguments)
	if text == "" {
		return ""
	}
	var payload map[string]any
	if err := common.Unmarshal([]byte(text), &payload); err == nil {
		if input, ok := payload["input"].(string); ok {
			return input
		}
	}
	return text
}

func rewriteDeepSeekResponsesItem(item map[string]any, customTools map[string]struct{}) bool {
	if common.Interface2String(item["type"]) != "function_call" {
		return false
	}
	key := customToolKey(common.Interface2String(item["namespace"]), common.Interface2String(item["name"]))
	if _, ok := customTools[key]; !ok {
		return false
	}
	item["type"] = "custom_tool_call"
	item["input"] = customToolInput(item["arguments"])
	delete(item, "arguments")
	delete(item, "namespace")
	return true
}

func rewriteDeepSeekResponsesPayload(payload map[string]any, customTools map[string]struct{}, trackedItems map[string]struct{}) (bool, error) {
	eventType := common.Interface2String(payload["type"])
	switch eventType {
	case "response.output_item.added", "response.output_item.done":
		item, ok := payload["item"].(map[string]any)
		if ok && rewriteDeepSeekResponsesItem(item, customTools) {
			if itemID := common.Interface2String(item["id"]); itemID != "" {
				trackedItems[itemID] = struct{}{}
			}
		}
	case "response.function_call_arguments.delta":
		if _, ok := trackedItems[common.Interface2String(payload["item_id"])]; ok {
			return true, nil
		}
	case "response.function_call_arguments.done":
		if _, ok := trackedItems[common.Interface2String(payload["item_id"])]; ok {
			payload["type"] = "response.custom_tool_call_input.done"
			payload["input"] = customToolInput(payload["arguments"])
			delete(payload, "arguments")
		}
	case "response.completed":
		response, ok := payload["response"].(map[string]any)
		if !ok {
			break
		}
		output, _ := response["output"].([]any)
		for _, value := range output {
			if item, ok := value.(map[string]any); ok {
				rewriteDeepSeekResponsesItem(item, customTools)
			}
		}
	}
	return false, nil
}

func rewriteDeepSeekResponsesSSE(body io.ReadCloser, customTools map[string]struct{}) io.ReadCloser {
	reader, writer := io.Pipe()
	go func() {
		defer body.Close()
		scanner := bufio.NewScanner(body)
		scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
		trackedItems := make(map[string]struct{})
		pendingEvent := ""
		skipBlank := false
		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(line, "event:") {
				pendingEvent = line
				continue
			}
			if strings.HasPrefix(line, "data:") {
				var payload map[string]any
				if err := common.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(line, "data:"))), &payload); err == nil {
					drop, err := rewriteDeepSeekResponsesPayload(payload, customTools, trackedItems)
					if err != nil {
						writer.CloseWithError(err)
						return
					}
					if drop {
						pendingEvent = ""
						skipBlank = true
						continue
					}
					encoded, err := common.Marshal(payload)
					if err != nil {
						writer.CloseWithError(err)
						return
					}
					if pendingEvent != "" {
						eventType := common.Interface2String(payload["type"])
						if eventType != "" {
							pendingEvent = "event: " + eventType
						}
						fmt.Fprintln(writer, pendingEvent)
						pendingEvent = ""
					}
					fmt.Fprintf(writer, "data: %s\n", encoded)
					continue
				}
			}
			if pendingEvent != "" {
				fmt.Fprintln(writer, pendingEvent)
				pendingEvent = ""
			}
			if line == "" && skipBlank {
				skipBlank = false
				continue
			}
			fmt.Fprintln(writer, line)
		}
		if pendingEvent != "" {
			fmt.Fprintln(writer, pendingEvent)
		}
		writer.CloseWithError(scanner.Err())
	}()
	return reader
}

func rewriteDeepSeekResponsesJSON(body io.ReadCloser, customTools map[string]struct{}) io.ReadCloser {
	data, err := io.ReadAll(body)
	body.Close()
	if err != nil {
		return io.NopCloser(bytes.NewReader(data))
	}
	var response map[string]any
	if err := common.Unmarshal(data, &response); err != nil {
		return io.NopCloser(bytes.NewReader(data))
	}
	output, _ := response["output"].([]any)
	for _, value := range output {
		if item, ok := value.(map[string]any); ok {
			rewriteDeepSeekResponsesItem(item, customTools)
		}
	}
	encoded, err := common.Marshal(response)
	if err != nil {
		return io.NopCloser(bytes.NewReader(data))
	}
	return io.NopCloser(bytes.NewReader(encoded))
}

func (a *Adaptor) DoRequest(c *gin.Context, info *relaycommon.RelayInfo, requestBody io.Reader) (any, error) {
	return channel.DoApiRequest(a, c, info, requestBody)
}

func (a *Adaptor) DoResponse(c *gin.Context, resp *http.Response, info *relaycommon.RelayInfo) (usage any, err *types.NewAPIError) {
	if info.RelayMode == constant.RelayModeResponses && resp != nil && resp.StatusCode < http.StatusBadRequest {
		if value, ok := c.Get(responsesCustomToolsContextKey); ok {
			if customTools, ok := value.(map[string]struct{}); ok && len(customTools) > 0 {
				if info.IsStream {
					resp.Body = rewriteDeepSeekResponsesSSE(resp.Body, customTools)
				} else {
					resp.Body = rewriteDeepSeekResponsesJSON(resp.Body, customTools)
				}
			}
		}
	}
	switch info.RelayFormat {
	case types.RelayFormatClaude:
		adaptor := claude.Adaptor{}
		return adaptor.DoResponse(c, resp, info)
	default:
		adaptor := openai.Adaptor{}
		return adaptor.DoResponse(c, resp, info)
	}
}

func (a *Adaptor) GetModelList() []string {
	return ModelList
}

func (a *Adaptor) GetChannelName() string {
	return ChannelName
}
