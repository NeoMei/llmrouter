package gemini

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	appconstant "github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/relay/toolpolicy"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGeminiNativeResponsesStreamToolFailuresDoNotReachWriter(t *testing.T) {
	gin.SetMode(gin.TestMode)
	previousTimeout := appconstant.StreamingTimeout
	appconstant.StreamingTimeout = 30
	t.Cleanup(func() { appconstant.StreamingTimeout = previousTimeout })
	tests := []struct {
		name       string
		body       string
		setPolicy  bool
		toolChoice any
	}{
		{name: "undeclared tool", setPolicy: true, body: geminiSSE(`{"candidates":[{"index":0,"finishReason":"STOP","content":{"role":"model","parts":[{"functionCall":{"name":"delete_all","args":{}}}]}}]}`)},
		{name: "missing policy", body: geminiSSE(`{"candidates":[{"index":0,"finishReason":"STOP","content":{"role":"model","parts":[{"functionCall":{"name":"lookup","args":{}}}]}}]}`)},
		{name: "truncated before candidate finish", setPolicy: true, body: "data: {\"candidates\":[{\"index\":0,\"content\":{\"role\":\"model\",\"parts\":[{\"functionCall\":{\"name\":\"lookup\",\"args\":{}}}]}}]}\n\n"},
		{name: "required choice with text only", setPolicy: true, toolChoice: "required", body: geminiSSE(`{"candidates":[{"index":0,"finishReason":"STOP","content":{"role":"model","parts":[{"text":"hello"}]}}]}`)},
		{name: "named choice with text only", setPolicy: true, toolChoice: map[string]any{"type": "function", "function": map[string]any{"name": "lookup"}}, body: geminiSSE(`{"candidates":[{"index":0,"finishReason":"STOP","content":{"role":"model","parts":[{"text":"hello"}]}}]}`)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
			if test.setPolicy {
				policy, err := toolpolicy.BuildFromChat([]dto.ToolCallRequest{{Type: "function", Function: dto.FunctionRequest{Name: "lookup"}}}, test.toolChoice)
				require.NoError(t, err)
				toolpolicy.Set(c, policy)
			}
			info := newGeminiResponsesRelayInfo(true)
			usage, apiErr := GeminiResponsesStreamHandler(c, info, &http.Response{Body: io.NopCloser(strings.NewReader(test.body))})
			require.NotNil(t, apiErr)
			assert.Nil(t, usage)
			assert.Empty(t, recorder.Body.String())
			assert.Zero(t, info.SendResponseCount)
		})
	}
}

func geminiSSE(frame string) string {
	return "data: " + frame + "\n\ndata: [DONE]\n\n"
}
