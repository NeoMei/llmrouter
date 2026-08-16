package relay

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/relay/channel/openai"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// TestResponsesCompactViaChatEndToEnd drives the compaction turn against a
// chat-only upstream and asserts the v1 compaction response shape Codex expects.
func TestResponsesCompactViaChatEndToEnd(t *testing.T) {
	gin.SetMode(gin.TestMode)
	service.InitHttpClient()

	var upstreamPath string
	var upstreamBody map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamPath = r.URL.Path
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &upstreamBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-x","object":"chat.completion","model":"glm-5.2","choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":"Progress: fixed auth. Next: write tests."}}],"usage":{"prompt_tokens":100,"completion_tokens":20,"total_tokens":120}}`))
	}))
	defer upstream.Close()

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses/compact", nil)

	info := &relaycommon.RelayInfo{
		RelayMode:   relayconstant.RelayModeResponsesCompact,
		RelayFormat: types.RelayFormatOpenAIResponses,
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelType:       constant.ChannelTypeOpenAI,
			ChannelBaseUrl:    upstream.URL,
			UpstreamModelName: "glm-5.2",
			ApiKey:            "k",
			ChannelId:         4343,
		},
	}
	info.ResponsesViaChat = true

	request := &dto.OpenAIResponsesRequest{
		Model: "glm-5.2",
		Input: json.RawMessage(`[
			{"type":"message","role":"user","content":[{"type":"input_text","text":"please fix the auth bug"}]},
			{"type":"message","role":"assistant","content":[{"type":"output_text","text":"done"}]},
			{"type":"compaction_trigger"}
		]`),
	}

	usage, apiErr := responsesCompactViaChat(c, info, &openai.Adaptor{}, request)
	require.Nil(t, apiErr)
	require.Equal(t, 100, usage.PromptTokens)
	require.Equal(t, 20, usage.CompletionTokens)

	require.Equal(t, "/v1/chat/completions", upstreamPath, "compaction must run on the chat endpoint")
	require.Equal(t, "glm-5.2", upstreamBody["model"])
	require.NotNil(t, upstreamBody["messages"])
	require.Nil(t, upstreamBody["tools"], "tool surface must be dropped")

	body := recorder.Body.String()
	var payload map[string]any
	require.NoError(t, common.Unmarshal([]byte(body), &payload))
	require.Equal(t, "response.compaction", payload["object"])
	output := payload["output"].([]any)
	require.Len(t, output, 2, "retained user message + summary item")
	summaryItem := output[1].(map[string]any)
	require.Equal(t, "user", summaryItem["role"])
	summaryContent := summaryItem["content"].([]any)[0].(map[string]any)
	require.Equal(t, "input_text", summaryContent["type"])
	require.True(t, strings.HasPrefix(summaryContent["text"].(string), compactSummaryPrefix))
	require.Contains(t, summaryContent["text"].(string), "Progress: fixed auth")
	usageOut := payload["usage"].(map[string]any)
	require.Equal(t, float64(120), usageOut["total_tokens"])
	require.False(t, strings.Contains(body, "chat.completion"), "chat wire format must not leak")
}
