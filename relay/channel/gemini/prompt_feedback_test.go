package gemini

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relay/toolpolicy"
	"github.com/QuantumNous/new-api/service/responsesstate"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGeminiPromptFeedbackBlockFailsClosedAcrossHandlers(t *testing.T) {
	previousTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() { constant.StreamingTimeout = previousTimeout })
	t.Setenv("RESPONSES_STATE_ENABLED", "1")
	responsesstate.ResetForTest()
	t.Cleanup(responsesstate.ResetForTest)
	store, err := responsesstate.Default()
	require.NoError(t, err)
	scope := responsesstate.Scope{TenantID: 11, UserID: 11, TokenID: 7, RouteID: 3, TargetID: 4, ChannelID: 5, Model: "gemini-test"}
	payload := `{"promptFeedback":{"blockReason":"SAFETY"}}`

	tests := []struct {
		name    string
		stream  bool
		info    func() *relaycommon.RelayInfo
		handler func(*gin.Context, *relaycommon.RelayInfo, *http.Response) (*dto.Usage, *types.NewAPIError)
	}{
		{name: "native nonstream", info: func() *relaycommon.RelayInfo {
			return &relaycommon.RelayInfo{RelayMode: relayconstant.RelayModeGemini, RelayFormat: types.RelayFormatGemini, ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "gemini-test"}}
		}, handler: func(c *gin.Context, info *relaycommon.RelayInfo, resp *http.Response) (*dto.Usage, *types.NewAPIError) {
			return GeminiTextGenerationHandler(c, info, resp)
		}},
		{name: "chat nonstream", info: func() *relaycommon.RelayInfo {
			return &relaycommon.RelayInfo{RelayFormat: types.RelayFormatOpenAI, ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "gemini-test"}}
		}, handler: GeminiChatHandler},
		{name: "responses nonstream", info: func() *relaycommon.RelayInfo { return newGeminiResponsesRelayInfo(false) }, handler: GeminiResponsesHandler},
		{name: "native stream", stream: true, info: func() *relaycommon.RelayInfo {
			return &relaycommon.RelayInfo{IsStream: true, RelayMode: relayconstant.RelayModeGemini, RelayFormat: types.RelayFormatGemini, DisablePing: true, ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "gemini-test"}}
		}, handler: GeminiTextGenerationStreamHandler},
		{name: "chat stream", stream: true, info: func() *relaycommon.RelayInfo {
			return &relaycommon.RelayInfo{IsStream: true, RelayFormat: types.RelayFormatOpenAI, DisablePing: true, ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "gemini-test"}}
		}, handler: GeminiChatStreamHandler},
		{name: "responses stream", stream: true, info: func() *relaycommon.RelayInfo { return newGeminiResponsesRelayInfo(true) }, handler: GeminiResponsesStreamHandler},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			policy, policyErr := toolpolicy.BuildFromChat([]dto.ToolCallRequest{{Type: "function", Function: dto.FunctionRequest{Name: "lookup"}}}, "required")
			require.NoError(t, policyErr)
			toolpolicy.Set(c, policy)
			requestID := "gemini_block_" + strings.ReplaceAll(test.name, " ", "_")
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
			c.Set(common.RequestIdKey, requestID)
			c.Set(string(constant.ContextKeyResponsesContinuity), responsesstate.ContinuityContext{Scope: scope, CurrentInput: json.RawMessage(`[]`)})
			body := payload
			if test.stream {
				body = "data: " + payload + "\n\ndata: [DONE]\n\n"
			}
			usage, apiErr := test.handler(c, test.info(), &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))})
			require.Nil(t, usage)
			require.NotNil(t, apiErr)
			assert.Equal(t, types.ErrorCodePromptBlocked, apiErr.GetErrorCode())
			assert.Equal(t, "gemini_block_reason=SAFETY", common.GetContextKeyString(c, constant.ContextKeyAdminRejectReason))
			assert.NotContains(t, recorder.Body.String(), "response.completed")
			assert.NotContains(t, recorder.Body.String(), "finish_reason")
			_, loadErr := store.Load(context.Background(), scope, "chatcmpl-"+requestID)
			require.ErrorIs(t, loadErr, responsesstate.ErrNotFound)
		})
	}
}
