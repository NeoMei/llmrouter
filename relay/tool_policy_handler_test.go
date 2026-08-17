package relay

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
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

func TestRelayHelpersRejectInvalidToolDefinitionsBeforeAdaptorAndClearOldPolicy(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("RESPONSES_STATE_ENABLED", "0")
	responsesstate.ResetForTest()
	t.Cleanup(responsesstate.ResetForTest)

	valid, err := toolpolicy.BuildFromResponses(json.RawMessage(`[{"type":"function","name":"old"}]`), nil)
	require.NoError(t, err)

	tests := []struct {
		name string
		body string
		info func() *relaycommon.RelayInfo
		run  func(*gin.Context, *relaycommon.RelayInfo) *types.NewAPIError
	}{
		{
			name: "chat",
			info: func() *relaycommon.RelayInfo {
				return &relaycommon.RelayInfo{Request: &dto.GeneralOpenAIRequest{
					Model: "model-a",
					Tools: []dto.ToolCallRequest{{Type: "function"}},
				}}
			},
			run: TextHelper,
		},
		{
			name: "responses",
			body: `{"model":"model-a","input":[],"tools":[{"type":"function","name":""}]}`,
			info: func() *relaycommon.RelayInfo {
				return &relaycommon.RelayInfo{
					RelayMode: relayconstant.RelayModeResponses,
					Request: &dto.OpenAIResponsesRequest{
						Model: "model-a", Input: json.RawMessage(`[]`),
						Tools: json.RawMessage(`[{"type":"function","name":""}]`),
					},
				}
			},
			run: ResponsesHelper,
		},
		{
			name: "claude",
			info: func() *relaycommon.RelayInfo {
				return &relaycommon.RelayInfo{Request: &dto.ClaudeRequest{
					Model: "model-a", Tools: []any{map[string]any{"input_schema": map[string]any{"type": "object"}}},
				}}
			},
			run: ClaudeHelper,
		},
		{
			name: "gemini",
			info: func() *relaycommon.RelayInfo {
				return &relaycommon.RelayInfo{Request: &dto.GeminiChatRequest{
					Tools: json.RawMessage(`{"functionDeclarations":"bad"}`),
				}}
			},
			run: GeminiHelper,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			writer := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(writer)
			body := tc.body
			if body == "" {
				body = `{}`
			}
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/test", strings.NewReader(body))
			c.Request.Header.Set("Content-Type", "application/json")
			c.Set("channel_type", -999)
			defer common.CleanupBodyStorage(c)
			toolpolicy.Set(c, valid)

			apiErr := tc.run(c, tc.info())

			require.NotNil(t, apiErr)
			assert.Equal(t, http.StatusBadRequest, apiErr.StatusCode)
			assert.Equal(t, types.ErrorCodeInvalidRequest, apiErr.GetErrorCode())
			assert.True(t, types.IsSkipRetryError(apiErr))
			_, ok := toolpolicy.FromContext(c)
			assert.False(t, ok, "a failed retry attempt must not retain an earlier policy")
		})
	}
}
