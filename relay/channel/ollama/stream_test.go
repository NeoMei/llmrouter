package ollama

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/toolpolicy"
	"github.com/QuantumNous/new-api/types"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOllamaChatHandlerNonStreamToolCalls(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name string
		raw  string
	}{
		{
			name: "compact ndjson per-line parse path",
			raw: `{"model":"llama3.1","created_at":"2026-05-27T12:00:00Z","message":{"role":"assistant","content":"","tool_calls":[{"function":{"name":"get_weather","arguments":{"city":"Paris","days":0}}}]},"done":false}
{"model":"llama3.1","created_at":"2026-05-27T12:00:00Z","message":{"role":"assistant","content":""},"done":true,"done_reason":"stop","prompt_eval_count":5,"eval_count":7}`,
		},
		{
			name: "pretty json fallback parse path",
			raw: `{
  "model": "llama3.1",
  "created_at": "2026-05-27T12:00:00Z",
  "message": {
    "role": "assistant",
    "content": "",
    "tool_calls": [
      {
        "function": {
          "name": "get_weather",
          "arguments": {
            "city": "Paris",
            "days": 0
          }
        }
      }
    ]
  },
  "done": true,
  "done_reason": "stop",
  "prompt_eval_count": 5,
  "eval_count": 7
}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			setOllamaToolPolicy(t, c, "get_weather")

			resp := &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(tt.raw)),
			}

			usage, apiErr := ollamaChatHandler(c, &relaycommon.RelayInfo{
				ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "fallback-model"},
			}, resp)
			require.Nil(t, apiErr)
			require.NotNil(t, usage)
			assert.Equal(t, 12, usage.TotalTokens)

			var out dto.OpenAITextResponse
			require.NoError(t, common.Unmarshal(w.Body.Bytes(), &out))
			require.Len(t, out.Choices, 1)
			assert.Equal(t, constant.FinishReasonToolCalls, out.Choices[0].FinishReason)

			var toolCalls []dto.ToolCallResponse
			require.NoError(t, common.Unmarshal(out.Choices[0].Message.ToolCalls, &toolCalls))
			require.Len(t, toolCalls, 1)
			assert.NotEmpty(t, toolCalls[0].ID)
			assert.Equal(t, "function", toolCalls[0].Type)
			assert.Equal(t, "get_weather", toolCalls[0].Function.Name)
			assert.Nil(t, toolCalls[0].Index)

			var args map[string]any
			require.NoError(t, common.Unmarshal([]byte(toolCalls[0].Function.Arguments), &args))
			assert.Equal(t, "Paris", args["city"])
			assert.Equal(t, float64(0), args["days"])
		})
	}
}

func TestOllamaChatHandlerRejectsInvalidNonStreamToolCalls(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name         string
		toolName     string
		arguments    string
		setPolicy    bool
		expectedCode types.ErrorCode
	}{
		{
			name:         "undeclared tool",
			toolName:     "delete_all",
			arguments:    `{"city":"Paris"}`,
			setPolicy:    true,
			expectedCode: types.ErrorCodeUpstreamToolPolicyViolation,
		},
		{
			name:         "malformed arguments",
			toolName:     "get_weather",
			arguments:    `["Paris"]`,
			setPolicy:    true,
			expectedCode: types.ErrorCodeUpstreamToolProtocolInvalid,
		},
		{
			name:         "missing policy",
			toolName:     "get_weather",
			arguments:    `{"city":"Paris"}`,
			expectedCode: types.ErrorCodeUpstreamToolProtocolInvalid,
		},
		{
			name:         "duplicate argument key",
			toolName:     "get_weather",
			arguments:    `{"city":"Paris","city":"London"}`,
			setPolicy:    true,
			expectedCode: types.ErrorCodeUpstreamToolProtocolInvalid,
		},
	}
	formats := []struct {
		name   string
		pretty bool
	}{
		{name: "compact ndjson", pretty: false},
		{name: "pretty fallback", pretty: true},
	}

	for _, format := range formats {
		for _, tt := range tests {
			t.Run(format.name+"/"+tt.name, func(t *testing.T) {
				w := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(w)
				if tt.setPolicy {
					setOllamaToolPolicy(t, c, "get_weather")
				}

				raw := ollamaToolResponseJSON(tt.toolName, tt.arguments, format.pretty)
				resp := &http.Response{
					StatusCode: http.StatusOK,
					Header:     make(http.Header),
					Body:       io.NopCloser(strings.NewReader(raw)),
				}
				info := &relaycommon.RelayInfo{
					ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "fallback-model"},
				}

				usage, apiErr := ollamaChatHandler(c, info, resp)

				require.NotNil(t, apiErr)
				assert.Equal(t, tt.expectedCode, apiErr.GetErrorCode())
				assert.Nil(t, usage)
				assert.Empty(t, w.Body.Bytes(), "invalid upstream bytes must not reach the client")
				assert.Zero(t, info.SendResponseCount)
			})
		}
	}
}

func setOllamaToolPolicy(t *testing.T, c *gin.Context, name string) {
	t.Helper()
	policy, err := toolpolicy.BuildFromChat([]dto.ToolCallRequest{{
		Type:     "function",
		Function: dto.FunctionRequest{Name: name},
	}}, nil)
	require.NoError(t, err)
	toolpolicy.Set(c, policy)
}

func ollamaToolResponseJSON(name, arguments string, pretty bool) string {
	if !pretty {
		return fmt.Sprintf(`{"model":"llama3.1","message":{"role":"assistant","tool_calls":[{"function":{"name":%q,"arguments":%s}}]},"done":false}
{"model":"llama3.1","message":{"role":"assistant"},"done":true,"done_reason":"stop","prompt_eval_count":5,"eval_count":7}`, name, arguments)
	}
	return fmt.Sprintf(`{
  "model": "llama3.1",
  "message": {
    "role": "assistant",
    "tool_calls": [
      {
        "function": {
          "name": %q,
          "arguments": %s
        }
      }
    ]
  },
  "done": true,
  "done_reason": "stop",
  "prompt_eval_count": 5,
  "eval_count": 7
}`, name, arguments)
}
