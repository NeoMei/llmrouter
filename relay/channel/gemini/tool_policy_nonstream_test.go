package gemini

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/toolpolicy"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGeminiChatHandlerRejectsInvalidToolCallBeforeWriting(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1beta/models/model-a:generateContent", nil)
	policy, err := toolpolicy.BuildFromChat([]dto.ToolCallRequest{{Type: "function", Function: dto.FunctionRequest{Name: "lookup"}}}, nil)
	require.NoError(t, err)
	toolpolicy.Set(c, policy)
	info := &relaycommon.RelayInfo{RelayFormat: types.RelayFormatGemini, ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "model-a"}}
	resp := &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{
		"candidates":[
			{"index":0,"content":{"role":"model","parts":[{"text":"safe"}]}},
			{"index":1,"content":{"role":"model","parts":[{"functionCall":{"name":"delete_all","args":{}}}]}}
		],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2}
	}`))}

	usage, apiErr := GeminiChatHandler(c, info, resp)
	require.Nil(t, usage)
	require.NotNil(t, apiErr)
	assert.Equal(t, types.ErrorCodeUpstreamToolPolicyViolation, apiErr.GetErrorCode())
	assert.Empty(t, recorder.Body.String())
	assert.Zero(t, info.SendResponseCount)
}

func TestGeminiNativeNonStreamHandlersRejectInvalidToolCallsBeforeWriting(t *testing.T) {
	tests := []struct {
		name    string
		handler func(*gin.Context, *relaycommon.RelayInfo, *http.Response) (*dto.Usage, *types.NewAPIError)
	}{
		{name: "native Gemini", handler: GeminiTextGenerationHandler},
		{name: "Gemini to Responses", handler: GeminiResponsesHandler},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, "/generate", nil)
			policy, err := toolpolicy.BuildFromGemini(json.RawMessage(`[{"functionDeclarations":[{"name":"lookup"}]}]`), nil)
			require.NoError(t, err)
			toolpolicy.Set(c, policy)
			info := &relaycommon.RelayInfo{RelayFormat: types.RelayFormatGemini, ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "model-a"}}
			resp := &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{
				"candidates":[{"index":0,"content":{"role":"model","parts":[{"functionCall":{"name":"delete_all","args":{}}}]}}]
			}`))}

			usage, apiErr := tt.handler(c, info, resp)
			require.Nil(t, usage)
			require.NotNil(t, apiErr)
			assert.Equal(t, types.ErrorCodeUpstreamToolPolicyViolation, apiErr.GetErrorCode())
			assert.Empty(t, recorder.Body.String())
		})
	}
}

func TestGeminiHandlerRejectsDuplicateArgumentKeysAndMissingPolicy(t *testing.T) {
	for _, tc := range []struct {
		name      string
		setPolicy bool
		body      string
	}{
		{name: "duplicate args", setPolicy: true, body: `{"candidates":[{"content":{"parts":[{"functionCall":{"name":"lookup","args":{"q":1,"q":2}}}]}}]}`},
		{name: "missing policy", body: `{"candidates":[{"content":{"parts":[{"functionCall":{"name":"lookup","args":{}}}]}}]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, "/generate", nil)
			if tc.setPolicy {
				policy, err := toolpolicy.BuildFromGemini(json.RawMessage(`[{"functionDeclarations":[{"name":"lookup"}]}]`), nil)
				require.NoError(t, err)
				toolpolicy.Set(c, policy)
			}
			info := &relaycommon.RelayInfo{RelayFormat: types.RelayFormatGemini, ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "model-a"}}
			usage, apiErr := GeminiChatHandler(c, info, &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(tc.body))})
			require.Nil(t, usage)
			require.NotNil(t, apiErr)
			assert.Equal(t, types.ErrorCodeUpstreamToolProtocolInvalid, apiErr.GetErrorCode())
			assert.Empty(t, recorder.Body.String())
		})
	}
}
