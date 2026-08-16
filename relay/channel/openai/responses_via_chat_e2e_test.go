package openai

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/relay/channel"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// TestResponsesViaChatEndToEnd drives the full adaptor chain against a
// chat-only upstream: a /v1/responses request is converted to chat
// completions, sent to the upstream chat endpoint, and the chat response is
// converted back into a responses-shaped body for the client.
func TestResponsesViaChatEndToEnd(t *testing.T) {
	gin.SetMode(gin.TestMode)
	service.InitHttpClient()

	var upstreamPath string
	var upstreamBody map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamPath = r.URL.Path
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &upstreamBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id": "chatcmpl-1", "object": "chat.completion", "created": 1,
			"model": "test-model",
			"choices": [{"index": 0, "finish_reason": "stop",
				"message": {"role": "assistant", "content": "pong"}}],
			"usage": {"prompt_tokens": 3, "completion_tokens": 2, "total_tokens": 5}
		}`))
	}))
	defer upstream.Close()

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)

	info := &relaycommon.RelayInfo{
		RelayMode:   relayconstant.RelayModeResponses,
		RelayFormat: types.RelayFormatOpenAIResponses,
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelType:       constant.ChannelTypeOpenAI,
			ChannelBaseUrl:    upstream.URL,
			UpstreamModelName: "test-model",
			ApiKey:            "k",
			ChannelId:         4242,
		},
	}
	info.ResponsesViaChat = true

	adaptor := &Adaptor{}
	request := dto.OpenAIResponsesRequest{
		Model: "test-model",
		Input: json.RawMessage(`"ping"`),
	}

	converted, err := adaptor.ConvertOpenAIResponsesRequest(c, info, request)
	require.NoError(t, err)
	chatReq, ok := converted.(*dto.GeneralOpenAIRequest)
	require.True(t, ok)

	bodyJSON, err := common.Marshal(chatReq)
	require.NoError(t, err)

	respRaw, err := channel.DoApiRequest(adaptor, c, info, bytes.NewReader(bodyJSON))
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, respRaw.StatusCode)
	require.Equal(t, "/v1/chat/completions", upstreamPath, "request must hit the upstream chat endpoint")
	require.Equal(t, "test-model", upstreamBody["model"])
	require.NotEmpty(t, upstreamBody["messages"], "converted request must carry chat messages")

	usage, apiErr := adaptor.DoResponse(c, respRaw, info)
	require.Nil(t, apiErr)
	require.NotNil(t, usage)

	body := recorder.Body.String()
	require.Contains(t, body, `"object":"response"`, "client must receive a responses-shaped body")
	require.Contains(t, body, "pong")
	require.False(t, strings.Contains(body, "chat.completion"), "chat wire format must not leak to the client")
}
