package deepseek

import (
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/constant"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConvertOpenAIRequestNormalizesDeveloperRole(t *testing.T) {
	request := &dto.GeneralOpenAIRequest{
		Model: "deepseek-v4-flash",
		Messages: []dto.Message{
			{Role: "developer", Content: "follow project instructions"},
			{Role: "user", Content: "hello"},
		},
	}

	converted, err := (&Adaptor{}).ConvertOpenAIRequest(nil, nil, request)

	require.NoError(t, err)
	chatRequest, ok := converted.(*dto.GeneralOpenAIRequest)
	require.True(t, ok)
	require.Len(t, chatRequest.Messages, 2)
	assert.Equal(t, "system", chatRequest.Messages[0].Role)
	assert.Equal(t, "user", chatRequest.Messages[1].Role)
}

func TestGetRequestURLUsesNativeResponsesEndpoint(t *testing.T) {
	url, err := (&Adaptor{}).GetRequestURL(&relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{ChannelBaseUrl: "https://api.deepseek.com"},
		RelayMode:   constant.RelayModeResponses,
	})

	require.NoError(t, err)
	assert.Equal(t, "https://api.deepseek.com/v1/responses", url)
}

func TestConvertOpenAIResponsesRequestPromotesAdditionalTools(t *testing.T) {
	request := dto.OpenAIResponsesRequest{
		Model: "deepseek-v4-flash",
		Input: json.RawMessage(`[
			{"type":"additional_tools","role":"developer","tools":[{"type":"namespace","name":"functions","tools":[{"type":"custom","name":"exec","description":"Run shell code","format":{"type":"grammar"}}]}]},
			{"type":"message","role":"user","content":"read the file"}
		]`),
	}

	converted, err := (&Adaptor{}).ConvertOpenAIResponsesRequest(nil, nil, request)

	require.NoError(t, err)
	responsesRequest, ok := converted.(dto.OpenAIResponsesRequest)
	require.True(t, ok)
	var tools []map[string]any
	require.NoError(t, json.Unmarshal(responsesRequest.Tools, &tools))
	require.Len(t, tools, 1)
	assert.Equal(t, "namespace", tools[0]["type"])
	assert.Equal(t, "functions", tools[0]["name"])
	innerTools, ok := tools[0]["tools"].([]any)
	require.True(t, ok)
	require.Len(t, innerTools, 1)
	innerTool, ok := innerTools[0].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "function", innerTool["type"])
	parameters, ok := innerTool["parameters"].(map[string]any)
	require.True(t, ok)
	properties, ok := parameters["properties"].(map[string]any)
	require.True(t, ok)
	assert.Contains(t, properties, "input")
	var input []map[string]any
	require.NoError(t, json.Unmarshal(responsesRequest.Input, &input))
	require.Len(t, input, 1)
	assert.Equal(t, "message", input[0]["type"])
}

func TestRewriteDeepSeekResponsesSSEConvertsCustomToolCall(t *testing.T) {
	upstream := strings.Join([]string{
		`event: response.output_item.added`,
		`data: {"type":"response.output_item.added","item":{"type":"function_call","id":"item-1","status":"in_progress","arguments":"","call_id":"call-1","name":"exec","namespace":"functions"},"output_index":0,"sequence_number":1}`,
		``,
		`event: response.function_call_arguments.delta`,
		`data: {"type":"response.function_call_arguments.delta","delta":"{\"input\":\"cat proof\"}","item_id":"item-1","output_index":0,"sequence_number":2}`,
		``,
		`event: response.function_call_arguments.done`,
		`data: {"type":"response.function_call_arguments.done","arguments":"{\"input\":\"cat proof\"}","item_id":"item-1","output_index":0,"sequence_number":3}`,
		``,
		`event: response.output_item.done`,
		`data: {"type":"response.output_item.done","item":{"type":"function_call","id":"item-1","status":"completed","arguments":"{\"input\":\"cat proof\"}","call_id":"call-1","name":"exec","namespace":"functions"},"output_index":0,"sequence_number":4}`,
		``,
	}, "\n")

	rewritten := rewriteDeepSeekResponsesSSE(io.NopCloser(strings.NewReader(upstream)), map[string]struct{}{
		customToolKey("functions", "exec"): {},
	})
	result, err := io.ReadAll(rewritten)
	require.NoError(t, err)
	text := string(result)
	assert.NotContains(t, text, "response.function_call_arguments")
	assert.Contains(t, text, "response.custom_tool_call_input.done")
	assert.Contains(t, text, `"type":"custom_tool_call"`)
	assert.Contains(t, text, `"input":"cat proof"`)
	assert.NotContains(t, text, `"namespace":"functions"`)
}
