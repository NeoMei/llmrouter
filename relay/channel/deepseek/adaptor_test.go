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
		Model: "deepseek-flash",
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
	assert.Equal(t, "https://api.deepseek.com/responses", url)
}

func TestGetRequestURLUsesNativeSearchAndFetchEndpoints(t *testing.T) {
	for _, tc := range []struct {
		mode int
		want string
	}{
		{constant.RelayModeToolsSearch, "https://api.deepseek.com/v1/tools/search"},
		{constant.RelayModeToolsFetch, "https://api.deepseek.com/v1/tools/fetch"},
	} {
		url, err := (&Adaptor{}).GetRequestURL(&relaycommon.RelayInfo{
			ChannelMeta: &relaycommon.ChannelMeta{ChannelBaseUrl: "https://api.deepseek.com"},
			RelayMode:   tc.mode,
		})
		require.NoError(t, err)
		assert.Equal(t, tc.want, url)
	}
}

func TestResponsesToolUsageUsesVendorBillingCounters(t *testing.T) {
	raw := []byte(`{"status":"completed","usage":{"input_tokens":10,"output_tokens":2,"server_tool_use":{"web_search_requests":7,"web_fetch_requests":9,"tool_search":3,"tool_fetch":4,"tool_image_search":2}}}`)
	assert.Equal(t, map[string]int{SearchToolPriceKey: 3, FetchToolPriceKey: 4, ImageSearchToolPriceKey: 2}, responsesToolUsage(raw))

	terminal := []byte(`{"type":"response.completed","response":{"status":"completed","usage":{"input_tokens":10,"output_tokens":2,"server_tool_use":{"tool_search":1,"tool_fetch":0,"tool_image_search":0}}}}`)
	assert.Equal(t, map[string]int{SearchToolPriceKey: 1, FetchToolPriceKey: 0, ImageSearchToolPriceKey: 0}, responsesToolUsage(terminal))

	// DeepSeek omits server_tool_use when every counter is zero. The final
	// usage is authoritative and must replace provisional streamed counts.
	assert.Equal(t, map[string]int{SearchToolPriceKey: 0, FetchToolPriceKey: 0, ImageSearchToolPriceKey: 0}, responsesToolUsage([]byte(`{"status":"completed","usage":{"input_tokens":10}}`)))
	assert.Nil(t, responsesToolUsage([]byte(`{"status":"completed","output":[]}`)))
}

func TestInitSetsSearchBillingKeys(t *testing.T) {
	info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{}}
	(&Adaptor{}).Init(info)
	assert.Equal(t, SearchToolPriceKey, info.WebSearchBillingKey)
	assert.Equal(t, FetchToolPriceKey, info.WebFetchBillingKey)
	info.RelayMode = constant.RelayModeResponses
	(&Adaptor{}).Init(info)
	assert.NotNil(t, info.VendorToolUsage)
}

func TestConvertOpenAIResponsesRequestPromotesAdditionalTools(t *testing.T) {
	request := dto.OpenAIResponsesRequest{
		Model: "deepseek-flash",
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
	assert.Equal(t, "function", tools[0]["type"])
	assert.Equal(t, "functions.exec", tools[0]["name"])
	parameters, ok := tools[0]["parameters"].(map[string]any)
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
		"functions.exec": {},
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

func TestRewriteDeepSeekResponsesSSERepairsMessageAndReasoningIDs(t *testing.T) {
	upstream := strings.Join([]string{
		`event: response.output_item.added`,
		`data: {"type":"response.output_item.added","item":{"type":"reasoning","id":"11111111-2222","status":"in_progress"},"output_index":0,"sequence_number":1}`,
		``,
		`event: response.reasoning_text.delta`,
		`data: {"type":"response.reasoning_text.delta","item_id":"11111111-2222","output_index":0,"delta":"thinking","sequence_number":2}`,
		``,
		`event: response.output_item.done`,
		`data: {"type":"response.output_item.done","item":{"type":"reasoning","id":"11111111-2222","status":"completed"},"output_index":0,"sequence_number":3}`,
		``,
		`event: response.output_item.added`,
		`data: {"type":"response.output_item.added","item":{"type":"message","id":"33333333-4444","status":"in_progress"},"output_index":1,"sequence_number":4}`,
		``,
		`event: response.output_item.added`,
		`data: {"type":"response.output_item.added","item":{"type":"function_call","id":"call-item","call_id":"call-1","name":"lookup","status":"in_progress"},"output_index":2,"sequence_number":5}`,
		``,
		`event: response.completed`,
		`data: {"type":"response.completed","response":{"status":"completed","output":[{"type":"reasoning","id":"11111111-2222"},{"type":"message","id":"33333333-4444"},{"type":"function_call","id":"call-item","call_id":"call-1"}]}}`,
		``,
	}, "\n")

	result, err := io.ReadAll(rewriteDeepSeekResponsesSSE(io.NopCloser(strings.NewReader(upstream)), nil))
	require.NoError(t, err)
	text := string(result)
	assert.Contains(t, text, `"id":"rs_11111111-2222"`)
	assert.Contains(t, text, `"item_id":"rs_11111111-2222"`)
	assert.Contains(t, text, `"id":"msg_33333333-4444"`)
	assert.Contains(t, text, `"id":"call-item"`)
	assert.Contains(t, text, `"call_id":"call-1"`)
	assert.Equal(t, 3, strings.Count(text, `"id":"rs_11111111-2222"`))
}

func TestRewriteDeepSeekResponsesJSONRepairsMessageAndReasoningIDs(t *testing.T) {
	body := `{"status":"completed","output":[{"type":"reasoning","id":"r-bare"},{"type":"message","id":"msg_already"},{"type":"message"}]}`
	result, err := io.ReadAll(rewriteDeepSeekResponsesJSON(io.NopCloser(strings.NewReader(body)), nil))
	require.NoError(t, err)
	text := string(result)
	assert.Contains(t, text, `"id":"rs_r-bare"`)
	assert.Contains(t, text, `"id":"msg_already"`)
	assert.Contains(t, text, `"id":"msg_deepseek_2"`)
}
