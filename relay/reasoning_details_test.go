package relay

import (
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
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/model_setting"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestTextHelperFiltersAnthropicStateForUnrelatedChatProviders(t *testing.T) {
	service.InitHttpClient()
	originalPassthrough := model_setting.GetGlobalSettings().PassThroughRequestEnabled
	model_setting.GetGlobalSettings().PassThroughRequestEnabled = false
	t.Cleanup(func() { model_setting.GetGlobalSettings().PassThroughRequestEnabled = originalPassthrough })
	for _, tc := range []struct {
		name        string
		channel     int
		passthrough bool
	}{
		{"OpenAI", constant.ChannelTypeOpenAI, false},
		{"DeepSeek", constant.ChannelTypeDeepSeek, false},
		{"Gateway", constant.ChannelTypeGateway, false},
		{"explicit raw passthrough", constant.ChannelTypeOpenAI, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var received []byte
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				received, _ = io.ReadAll(r.Body)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":{"message":"fixture ends after request capture","type":"invalid_request_error"}}`))
			}))
			defer server.Close()
			raw := `{"model":"test-model","messages":[{"role":"assistant","content":"answer","reasoning_content":"thought","reasoning_details":[{"type":"reasoning.text","text":"thought","signature":"YW50aHJvcGljLXNpZ25hdHVyZS1leGFtcGxl","format":"anthropic-claude-v1"},{"type":"vendor.future","opaque":{"keep":true}}]}]}`
			var request dto.GeneralOpenAIRequest
			require.NoError(t, common.Unmarshal([]byte(raw), &request))
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(raw))
			c.Request.Header.Set("Content-Type", "application/json")
			common.SetContextKey(c, constant.ContextKeyChannelType, tc.channel)
			common.SetContextKey(c, constant.ContextKeyChannelBaseUrl, server.URL)
			common.SetContextKey(c, constant.ContextKeyOriginalModel, "test-model")
			common.SetContextKey(c, constant.ContextKeyChannelKey, "fixture-key")
			common.SetContextKey(c, constant.ContextKeyChannelSetting, dto.ChannelSettings{PassThroughBodyEnabled: tc.passthrough})
			defer common.CleanupBodyStorage(c)
			apiErr := TextHelper(c, &relaycommon.RelayInfo{Request: &request, OriginModelName: "test-model", RequestURLPath: "/v1/chat/completions", RelayMode: relayconstant.RelayModeChatCompletions, RelayFormat: types.RelayFormatOpenAI})
			require.NotNil(t, apiErr)
			require.NotEmpty(t, received, "request must reach the real adaptor")
			if tc.passthrough {
				assert.JSONEq(t, raw, string(received))
			} else {
				assert.JSONEq(t, `[{"type":"vendor.future","opaque":{"keep":true}}]`, gjson.GetBytes(received, "messages.0.reasoning_details").Raw)
				assert.Equal(t, "thought", gjson.GetBytes(received, "messages.0.reasoning_content").String())
			}
			assert.Contains(t, string(request.Messages[0].ReasoningDetails), "anthropic-claude-v1", "per-attempt copy preserves original request for a later retry")
		})
	}
}
