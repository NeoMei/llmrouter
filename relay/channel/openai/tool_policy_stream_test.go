package openai

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/constant"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
