package claude

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	appconstant "github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/toolpolicy"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClaudeNativeStreamToolFailuresDoNotReachWriter(t *testing.T) {
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
		{
			name:      "undeclared tool",
			setPolicy: true,
			body: claudeSSE(
				`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_1","name":"delete_all","input":{}}}`,
				`{"type":"content_block_stop","index":0}`,
				`{"type":"message_stop"}`,
			),
		},
		{
			name: "missing policy",
			body: claudeSSE(
				`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_1","name":"lookup","input":{}}}`,
				`{"type":"content_block_stop","index":0}`,
				`{"type":"message_stop"}`,
			),
		},
		{
			name:      "truncated before message stop",
			setPolicy: true,
			body: claudeSSE(
				`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_1","name":"lookup","input":{}}}`,
				`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\\"q\\":1}"}}`,
				`{"type":"content_block_stop","index":0}`,
			),
		},
		{
			name:       "required choice with text only",
			setPolicy:  true,
			toolChoice: "required",
			body: claudeSSE(
				`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
				`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hello"}}`,
				`{"type":"content_block_stop","index":0}`,
				`{"type":"message_stop"}`,
			),
		},
		{
			name:       "named choice with text only",
			setPolicy:  true,
			toolChoice: map[string]any{"type": "function", "function": map[string]any{"name": "lookup"}},
			body: claudeSSE(
				`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
				`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hello"}}`,
				`{"type":"content_block_stop","index":0}`,
				`{"type":"message_stop"}`,
			),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
			if test.setPolicy {
				policy, err := toolpolicy.BuildFromChat([]dto.ToolCallRequest{{Type: "function", Function: dto.FunctionRequest{Name: "lookup"}}}, test.toolChoice)
				require.NoError(t, err)
				toolpolicy.Set(c, policy)
			}
			info := &relaycommon.RelayInfo{RelayFormat: types.RelayFormatClaude, DisablePing: true, ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "claude-test"}}
			usage, apiErr := ClaudeStreamHandler(c, &http.Response{Body: io.NopCloser(strings.NewReader(test.body))}, info)
			require.NotNil(t, apiErr)
			assert.Nil(t, usage)
			assert.Empty(t, recorder.Body.String())
			assert.Zero(t, info.SendResponseCount)
		})
	}
}

func TestClaudeTextOnlyTruncatedStreamReturnsNilUsageWithoutSuccessTerminal(t *testing.T) {
	previousTimeout := appconstant.StreamingTimeout
	appconstant.StreamingTimeout = 30
	t.Cleanup(func() { appconstant.StreamingTimeout = previousTimeout })
	tests := []struct {
		name      string
		cancelled bool
		body      string
	}{
		{name: "EOF", body: claudeSSE(
			`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"partial"}}`,
			`{"type":"content_block_stop","index":0}`,
		)},
		{name: "cancelled", cancelled: true, body: claudeSSE(
			`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":"done"}}`,
			`{"type":"content_block_stop","index":0}`,
			`{"type":"message_stop"}`,
		)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
			if test.cancelled {
				ctx, cancel := context.WithCancel(c.Request.Context())
				cancel()
				c.Request = c.Request.WithContext(ctx)
			}
			info := &relaycommon.RelayInfo{RelayFormat: types.RelayFormatClaude, DisablePing: true, ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "claude-test"}}
			usage, apiErr := ClaudeStreamHandler(c, &http.Response{Body: io.NopCloser(strings.NewReader(test.body))}, info)
			require.Nil(t, usage)
			require.NotNil(t, apiErr)
			assert.NotContains(t, recorder.Body.String(), "message_stop")
		})
	}
}

func TestClaudeFailedTerminalStillReportsBillableUsage(t *testing.T) {
	previousTimeout := appconstant.StreamingTimeout
	appconstant.StreamingTimeout = 30
	t.Cleanup(func() { appconstant.StreamingTimeout = previousTimeout })
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	info := &relaycommon.RelayInfo{
		RelayFormat:        types.RelayFormatClaude,
		DisablePing:        true,
		ChannelMeta:        &relaycommon.ChannelMeta{UpstreamModelName: "deepseek-flash"},
		ResponsesUsageInfo: &relaycommon.ResponsesUsageInfo{BuiltInTools: map[string]*relaycommon.BuildInToolInfo{}},
	}
	info.VendorToolUsage = func(raw []byte) map[string]int {
		var payload struct {
			Usage struct {
				ServerToolUse struct {
					ToolSearch int `json:"tool_search"`
				} `json:"server_tool_use"`
			} `json:"usage"`
		}
		require.NoError(t, json.Unmarshal(raw, &payload))
		return map[string]int{"deepseek_search": payload.Usage.ServerToolUse.ToolSearch}
	}
	body := claudeSSE(`{"type":"error","error":{"type":"api_error","message":"generation failed"},"usage":{"input_tokens":13,"output_tokens":2,"server_tool_use":{"tool_search":1}}}`)

	usage, apiErr := ClaudeStreamHandler(c, &http.Response{Body: io.NopCloser(strings.NewReader(body))}, info)
	require.NotNil(t, apiErr)
	require.NotNil(t, usage)
	assert.Equal(t, 13, usage.PromptTokens)
	assert.Equal(t, 2, usage.CompletionTokens)
	assert.Equal(t, 1, info.ResponsesUsageInfo.BuiltInTools["deepseek_search"].CallCount)
}

func claudeSSE(frames ...string) string {
	var builder strings.Builder
	for _, frame := range frames {
		builder.WriteString("data: ")
		builder.WriteString(frame)
		builder.WriteString("\n\n")
	}
	return builder.String()
}
