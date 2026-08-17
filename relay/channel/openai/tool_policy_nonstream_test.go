package openai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/toolpolicy"
	"github.com/QuantumNous/new-api/service/responsesstate"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setNonStreamToolPolicy(t *testing.T, c *gin.Context, name string) {
	t.Helper()
	policy, err := toolpolicy.BuildFromChat([]dto.ToolCallRequest{{
		Type: "function", Function: dto.FunctionRequest{Name: name},
	}}, nil)
	require.NoError(t, err)
	toolpolicy.Set(c, policy)
}

func TestOpenaiHandlerRejectsInvalidToolCallBeforeWriting(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	setNonStreamToolPolicy(t, c, "lookup")
	info := &relaycommon.RelayInfo{RelayFormat: types.RelayFormatOpenAI, ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "model-a"}}
	resp := &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{
		"id":"chatcmpl_1","model":"model-a","choices":[
			{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":"safe"}},
			{"index":1,"finish_reason":"tool_calls","message":{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"delete_all","arguments":"{}"}}]}}
		],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}
	}`))}

	usage, apiErr := OpenaiHandler(c, info, resp)
	require.Nil(t, usage)
	require.NotNil(t, apiErr)
	assert.Equal(t, types.ErrorCodeUpstreamToolPolicyViolation, apiErr.GetErrorCode())
	assert.False(t, types.IsSkipRetryError(apiErr))
	assert.Empty(t, recorder.Body.String())
	assert.Zero(t, info.SendResponseCount)
}

func TestOaiResponsesHandlerDoesNotPersistOrWriteInvalidToolCall(t *testing.T) {
	t.Setenv("RESPONSES_STATE_ENABLED", "1")
	responsesstate.ResetForTest()
	t.Cleanup(responsesstate.ResetForTest)
	store, err := responsesstate.Default()
	require.NoError(t, err)

	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	setNonStreamToolPolicy(t, c, "lookup")
	scope := responsesstate.Scope{TenantID: 11, UserID: 11, TokenID: 7, RouteID: 3, TargetID: 4, ChannelID: 5, Model: "model-a"}
	c.Set(string(constant.ContextKeyResponsesContinuity), responsesstate.ContinuityContext{Scope: scope, CurrentInput: json.RawMessage(`[]`)})
	info := &relaycommon.RelayInfo{UserId: 11, TokenId: 7, ModelRouteId: 3, ModelTargetId: 4, ResolvedChannelId: 5, ResolvedModelName: "model-a", ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "model-a"}}
	resp := &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{
		"id":"resp_invalid","status":"completed","output":[{"type":"function_call","call_id":"call_1","name":"delete_all","arguments":"{}","status":"completed"}]
	}`))}

	usage, apiErr := OaiResponsesHandler(c, info, resp)
	require.Nil(t, usage)
	require.NotNil(t, apiErr)
	assert.Equal(t, types.ErrorCodeUpstreamToolPolicyViolation, apiErr.GetErrorCode())
	assert.Empty(t, recorder.Body.String())
	_, loadErr := store.Load(context.Background(), scope, "resp_invalid")
	assert.ErrorIs(t, loadErr, responsesstate.ErrNotFound)
	assert.Zero(t, info.SendResponseCount)
}

func TestChatToResponsesHandlerValidatesBeforeConversionAndPersistence(t *testing.T) {
	t.Setenv("RESPONSES_STATE_ENABLED", "1")
	responsesstate.ResetForTest()
	t.Cleanup(responsesstate.ResetForTest)
	store, err := responsesstate.Default()
	require.NoError(t, err)

	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	setNonStreamToolPolicy(t, c, "lookup")
	scope := responsesstate.Scope{TenantID: 11, UserID: 11, TokenID: 7, RouteID: 3, TargetID: 4, ChannelID: 5, Model: "model-a"}
	c.Set(string(constant.ContextKeyResponsesContinuity), responsesstate.ContinuityContext{Scope: scope, CurrentInput: json.RawMessage(`[]`)})
	info := &relaycommon.RelayInfo{UserId: 11, TokenId: 7, ModelRouteId: 3, ModelTargetId: 4, ResolvedChannelId: 5, ResolvedModelName: "model-a", RelayFormat: types.RelayFormatOpenAIResponses, ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "model-a"}}
	resp := &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{
		"id":"chat_invalid","model":"model-a","choices":[{"index":0,"finish_reason":"tool_calls","message":{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"delete_all","arguments":"{}"}}]}}]
	}`))}

	usage, apiErr := OaiChatToResponsesHandler(c, info, resp)
	require.Nil(t, usage)
	require.NotNil(t, apiErr)
	assert.Equal(t, types.ErrorCodeUpstreamToolPolicyViolation, apiErr.GetErrorCode())
	assert.Empty(t, recorder.Body.String())
	_, loadErr := store.Load(context.Background(), scope, "chat_invalid")
	assert.ErrorIs(t, loadErr, responsesstate.ErrNotFound)
}

func TestResponsesToChatHandlerValidatesBeforeConversionAndWriting(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	policy, err := toolpolicy.BuildFromResponses(json.RawMessage(`[{"type":"function","name":"lookup"}]`), nil)
	require.NoError(t, err)
	toolpolicy.Set(c, policy)
	info := &relaycommon.RelayInfo{RelayFormat: types.RelayFormatOpenAI, ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "model-a"}}
	resp := &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{
		"id":"resp_invalid","status":"completed","output":[{"type":"function_call","call_id":"call_1","name":"lookup","arguments":"[]","status":"completed"}]
	}`))}

	usage, apiErr := OaiResponsesToChatHandler(c, info, resp)
	require.Nil(t, usage)
	require.NotNil(t, apiErr)
	assert.Equal(t, types.ErrorCodeUpstreamToolProtocolInvalid, apiErr.GetErrorCode())
	assert.Empty(t, recorder.Body.String())
}
