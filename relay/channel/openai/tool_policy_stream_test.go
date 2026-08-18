package openai

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relay/toolpolicy"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setCustomStreamToolPolicy(t *testing.T, c *gin.Context) {
	t.Helper()
	policy, err := toolpolicy.BuildFromChat([]dto.ToolCallRequest{{
		Type: "custom", Custom: json.RawMessage(`{"name":"shell","format":{"type":"text"}}`),
	}}, nil)
	require.NoError(t, err)
	toolpolicy.Set(c, policy)
}

func newToolPolicyStreamContext(t *testing.T, path, body string, format types.RelayFormat) (*gin.Context, *httptest.ResponseRecorder, *http.Response, *relaycommon.RelayInfo) {
	t.Helper()
	oldTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() { constant.StreamingTimeout = oldTimeout })
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, path, nil)
	setNonStreamToolPolicy(t, c, "lookup")
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
	info := &relaycommon.RelayInfo{
		RelayMode:       relayconstant.RelayModeChatCompletions,
		RelayFormat:     format,
		DisablePing:     true,
		ChannelMeta:     &relaycommon.ChannelMeta{UpstreamModelName: "gpt-test"},
		IsStream:        true,
		OriginModelName: "gpt-test",
	}
	return c, recorder, resp, info
}

func TestOaiStreamHandlerRejectsInvalidBufferedToolWithoutSuccessTerminal(t *testing.T) {
	body := strings.Join([]string{
		`data: {"id":"chatcmpl_1","object":"chat.completion.chunk","model":"gpt-test","choices":[{"index":0,"delta":{"content":"safe text"},"finish_reason":null}]}`,
		`data: {"id":"chatcmpl_1","object":"chat.completion.chunk","model":"gpt-test","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"delete_all","arguments":"{}"}}]},"finish_reason":null}]}`,
		`data: {"id":"chatcmpl_1","object":"chat.completion.chunk","model":"gpt-test","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
		`data: [DONE]`,
		``,
	}, "\n")
	c, recorder, resp, info := newToolPolicyStreamContext(t, "/v1/chat/completions", body, types.RelayFormatOpenAI)

	usage, apiErr := OaiStreamHandler(c, info, resp)
	require.Nil(t, usage)
	require.NotNil(t, apiErr)
	assert.Equal(t, types.ErrorCodeUpstreamToolPolicyViolation, apiErr.GetErrorCode())
	assert.Contains(t, recorder.Body.String(), "safe text")
	assert.NotContains(t, recorder.Body.String(), "delete_all")
	assert.NotContains(t, recorder.Body.String(), `"finish_reason":"tool_calls"`)
	assert.NotContains(t, recorder.Body.String(), "[DONE]")
}

func TestOaiResponsesStreamHandlerRejectsIncompleteToolBeforeCompleted(t *testing.T) {
	body := strings.Join([]string{
		`data: {"type":"response.output_text.delta","delta":"safe text"}`,
		`data: {"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"item_1","call_id":"call_1","name":"lookup","status":"in_progress"}}`,
		`data: {"type":"response.completed","response":{"id":"resp_1","status":"completed","output":[]}}`,
		`data: [DONE]`,
		``,
	}, "\n")
	c, recorder, resp, info := newToolPolicyStreamContext(t, "/v1/responses", body, types.RelayFormatOpenAIResponses)

	usage, apiErr := OaiResponsesStreamHandler(c, info, resp)
	require.Nil(t, usage)
	require.NotNil(t, apiErr)
	assert.Equal(t, types.ErrorCodeUpstreamToolProtocolInvalid, apiErr.GetErrorCode())
	assert.Contains(t, recorder.Body.String(), "safe text")
	assert.NotContains(t, recorder.Body.String(), "item_1")
	assert.NotContains(t, recorder.Body.String(), "response.completed")
}

func TestOaiResponsesStreamHandlerRejectsDuplicateKeysBeforeWritingFrame(t *testing.T) {
	body := "data: {\"type\":\"response.output_text.delta\",\"delta\":\"first\",\"delta\":\"second\"}\n\ndata: [DONE]\n\n"
	c, recorder, resp, info := newToolPolicyStreamContext(t, "/v1/responses", body, types.RelayFormatOpenAIResponses)

	usage, apiErr := OaiResponsesStreamHandler(c, info, resp)
	require.Nil(t, usage)
	require.NotNil(t, apiErr)
	assert.Equal(t, types.ErrorCodeUpstreamToolProtocolInvalid, apiErr.GetErrorCode())
	assert.Empty(t, recorder.Body.String())
}

func TestNativeStreamHandlersReturnErrorAndNilUsageOnUpstreamErrorTerminal(t *testing.T) {
	tests := []struct {
		name   string
		path   string
		format types.RelayFormat
		body   string
		handle func(*gin.Context, *relaycommon.RelayInfo, *http.Response) (*dto.Usage, *types.NewAPIError)
	}{
		{name: "responses failed", path: "/v1/responses", format: types.RelayFormatOpenAIResponses, body: strings.Join([]string{
			`data: {"type":"response.failed","error":{"message":"upstream failed"}}`,
			`data: {"type":"response.completed","response":{"id":"must_not_run","status":"completed","output":[]}}`,
			`data: [DONE]`, ``,
		}, "\n"), handle: OaiResponsesStreamHandler},
		{name: "chat error", path: "/v1/chat/completions", format: types.RelayFormatOpenAI, body: strings.Join([]string{
			`data: {"error":{"message":"upstream failed","type":"server_error"}}`,
			`data: {"choices":[{"index":0,"delta":{"content":"must not run"},"finish_reason":"stop"}]}`,
			`data: [DONE]`, ``,
		}, "\n"), handle: OaiStreamHandler},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			c, recorder, resp, info := newToolPolicyStreamContext(t, test.path, test.body, test.format)
			usage, apiErr := test.handle(c, info, resp)
			require.Nil(t, usage)
			require.NotNil(t, apiErr)
			assert.NotContains(t, recorder.Body.String(), "[DONE]")
			assert.NotContains(t, recorder.Body.String(), "response.completed")
			assert.NotContains(t, recorder.Body.String(), "must not run")
			assert.Empty(t, recorder.Body.String())
		})
	}
}

func TestOaiResponsesStreamHandlerDoesNotLeakCompletedCallBeforeLateInvalidFrame(t *testing.T) {
	body := strings.Join([]string{
		`data: {"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"item_1","call_id":"call_1","name":"lookup","status":"in_progress"}}`,
		`data: {"type":"response.output_item.done","output_index":0,"item":{"type":"function_call","id":"item_1","call_id":"call_1","name":"lookup","arguments":"{}","status":"completed"}}`,
		`data: {"type":"response.function_call_arguments.delta","call_id":"call_1","delta":"late"}`,
		`data: {"type":"response.completed","response":{"id":"resp_1","status":"completed","output":[]}}`,
		`data: [DONE]`,
		``,
	}, "\n")
	c, recorder, resp, info := newToolPolicyStreamContext(t, "/v1/responses", body, types.RelayFormatOpenAIResponses)
	usage, apiErr := OaiResponsesStreamHandler(c, info, resp)
	require.Nil(t, usage)
	require.NotNil(t, apiErr)
	assert.Empty(t, recorder.Body.String())
}

func TestOaiResponsesStreamHandlerRejectsFieldNullBeforeWritingToolFrames(t *testing.T) {
	body := strings.Join([]string{
		`data: {"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"item_1","call_id":"call_1","name":"lookup","status":"in_progress"}}`,
		`data: {"type":"response.function_call_arguments.delta","call_id":"call_1","delta":null}`,
		`data: [DONE]`,
		``,
	}, "\n")
	c, recorder, resp, info := newToolPolicyStreamContext(t, "/v1/responses", body, types.RelayFormatOpenAIResponses)
	usage, apiErr := OaiResponsesStreamHandler(c, info, resp)
	require.Nil(t, usage)
	require.NotNil(t, apiErr)
	assert.Empty(t, recorder.Body.String())
}

func TestOaiChatToResponsesStreamHandlerPreservesCustomToolKind(t *testing.T) {
	body := strings.Join([]string{
		`data: {"id":"chatcmpl_1","object":"chat.completion.chunk","model":"gpt-test","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"custom","function":{"name":"shell","arguments":"echo ok"}}]},"finish_reason":null}]}`,
		`data: {"id":"chatcmpl_1","object":"chat.completion.chunk","model":"gpt-test","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
		`data: [DONE]`,
		``,
	}, "\n")
	c, recorder, resp, info := newToolPolicyStreamContext(t, "/v1/responses", body, types.RelayFormatOpenAIResponses)
	setCustomStreamToolPolicy(t, c)
	usage, apiErr := OaiChatToResponsesStreamHandler(c, info, resp)
	require.Nil(t, apiErr)
	require.NotNil(t, usage)
	output := recorder.Body.String()
	assert.Contains(t, output, `"type":"custom_tool_call"`)
	assert.Contains(t, output, `"type":"response.custom_tool_call_input.delta"`)
	assert.Contains(t, output, `"type":"response.custom_tool_call_input.done"`)
	assert.NotContains(t, output, `"type":"function_call"`)
	assert.NotContains(t, output, "response.function_call_arguments")
}

func TestOaiChatToResponsesStreamHandlerDoesNotConvertInvalidToolFrames(t *testing.T) {
	body := strings.Join([]string{
		`data: {"id":"chatcmpl_1","object":"chat.completion.chunk","model":"gpt-test","choices":[{"index":0,"delta":{"content":"safe text"},"finish_reason":null}]}`,
		`data: {"id":"chatcmpl_1","object":"chat.completion.chunk","model":"gpt-test","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"delete_all","arguments":"{}"}}]},"finish_reason":null}]}`,
		`data: {"id":"chatcmpl_1","object":"chat.completion.chunk","model":"gpt-test","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
		`data: [DONE]`,
		``,
	}, "\n")
	c, recorder, resp, info := newToolPolicyStreamContext(t, "/v1/responses", body, types.RelayFormatOpenAIResponses)

	usage, apiErr := OaiChatToResponsesStreamHandler(c, info, resp)
	require.Nil(t, usage)
	require.NotNil(t, apiErr)
	assert.Equal(t, types.ErrorCodeUpstreamToolPolicyViolation, apiErr.GetErrorCode())
	assert.Contains(t, recorder.Body.String(), "safe text")
	assert.NotContains(t, recorder.Body.String(), "delete_all")
	assert.NotContains(t, recorder.Body.String(), "response.completed")
}

func TestOaiResponsesToChatStreamHandlerDoesNotConvertInvalidToolFrames(t *testing.T) {
	body := strings.Join([]string{
		`data: {"type":"response.output_text.delta","delta":"safe text"}`,
		`data: {"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"item_1","call_id":"call_1","name":"delete_all","status":"in_progress"}}`,
		`data: {"type":"response.output_item.done","output_index":0,"item":{"type":"function_call","id":"item_1","call_id":"call_1","name":"delete_all","arguments":"{}","status":"completed"}}`,
		`data: {"type":"response.completed","response":{"id":"resp_1","status":"completed","output":[{"type":"function_call","id":"item_1","call_id":"call_1","name":"delete_all","arguments":"{}","status":"completed"}]}}`,
		`data: [DONE]`,
		``,
	}, "\n")
	c, recorder, resp, info := newToolPolicyStreamContext(t, "/v1/chat/completions", body, types.RelayFormatOpenAI)

	usage, apiErr := OaiResponsesToChatStreamHandler(c, info, resp)
	require.Nil(t, usage)
	require.NotNil(t, apiErr)
	assert.Equal(t, types.ErrorCodeUpstreamToolPolicyViolation, apiErr.GetErrorCode())
	assert.Contains(t, recorder.Body.String(), "safe text")
	assert.NotContains(t, recorder.Body.String(), "delete_all")
	assert.NotContains(t, recorder.Body.String(), `"finish_reason":"tool_calls"`)
	assert.NotContains(t, recorder.Body.String(), "[DONE]")
}
