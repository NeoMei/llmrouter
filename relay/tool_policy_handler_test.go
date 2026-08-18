package relay

import (
	"encoding/json"
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

func TestFinalOutboundPolicyMatchesOverriddenBodyAcrossProtocols(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tests := []struct {
		name       string
		format     types.RelayFormat
		request    any
		body       string
		override   map[string]any
		allowed    *toolpolicy.Call
		disallowed toolpolicy.Call
		wantNoTool bool
	}{
		{
			name: "chat auto to none", format: types.RelayFormatOpenAI,
			request:    &dto.GeneralOpenAIRequest{},
			body:       `{"tools":[{"type":"function","function":{"name":"lookup"}}],"tool_choice":"auto"}`,
			override:   map[string]any{"tool_choice": "none"},
			disallowed: toolpolicy.Call{ID: "call_1", Name: "lookup", Arguments: json.RawMessage(`{}`)},
		},
		{
			name: "responses none to named with replacement", format: types.RelayFormatOpenAIResponses,
			request: &dto.OpenAIResponsesRequest{},
			body:    `{"tools":[{"type":"function","name":"lookup"}],"tool_choice":"none"}`,
			override: map[string]any{
				"tools":       []any{map[string]any{"type": "function", "name": "save"}},
				"tool_choice": map[string]any{"type": "function", "name": "save"},
			},
			allowed:    &toolpolicy.Call{ID: "call_1", Name: "save", Arguments: json.RawMessage(`{}`)},
			disallowed: toolpolicy.Call{ID: "call_2", Name: "lookup", Arguments: json.RawMessage(`{}`)},
		},
		{
			name: "claude none to auto", format: types.RelayFormatClaude,
			request:    &dto.ClaudeRequest{},
			body:       `{"tools":[{"name":"lookup","input_schema":{"type":"object"}}],"tool_choice":{"type":"none"}}`,
			override:   map[string]any{"tool_choice": map[string]any{"type": "auto"}},
			allowed:    &toolpolicy.Call{ID: "toolu_1", Name: "lookup", Arguments: json.RawMessage(`{}`)},
			disallowed: toolpolicy.Call{ID: "toolu_2", Name: "missing", Arguments: json.RawMessage(`{}`)},
		},
		{
			name: "gemini none to any with replacement", format: types.RelayFormatGemini,
			request: &dto.GeminiChatRequest{},
			body: `{"contents":[],"tools":[{"functionDeclarations":[{"name":"lookup"}]}],` +
				`"toolConfig":{"functionCallingConfig":{"mode":"NONE"}}}`,
			override: map[string]any{
				"tools": []any{map[string]any{"functionDeclarations": []any{map[string]any{"name": "save"}}}},
				"toolConfig": map[string]any{
					"functionCallingConfig": map[string]any{
						"mode": "ANY", "allowedFunctionNames": []string{"save"},
					},
				},
			},
			allowed:    &toolpolicy.Call{ID: "call_1", Name: "save", Arguments: json.RawMessage(`{}`)},
			disallowed: toolpolicy.Call{ID: "call_2", Name: "lookup", Arguments: json.RawMessage(`{}`)},
		},
		{
			name: "responses tool deletion", format: types.RelayFormatOpenAIResponses,
			request: &dto.OpenAIResponsesRequest{},
			body:    `{"tools":[{"type":"function","name":"lookup"}],"tool_choice":"auto"}`,
			override: map[string]any{
				"operations": []any{map[string]any{"path": "tools", "mode": "delete"}},
			},
			disallowed: toolpolicy.Call{ID: "call_1", Name: "lookup", Arguments: json.RawMessage(`{}`)},
			wantNoTool: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/test", nil)
			info := &relaycommon.RelayInfo{
				RelayFormat: tc.format,
				ChannelMeta: &relaycommon.ChannelMeta{ParamOverride: tc.override},
			}

			outbound, apiErr := finalizeOutboundToolPolicy(c, info, tc.request, []byte(tc.body))
			require.Nil(t, apiErr)
			require.NotEqual(t, []byte(tc.body), outbound, "the fixture must exercise an effective override")
			policy, ok := toolpolicy.FromContext(c)
			require.True(t, ok)
			bodyPolicy, err := toolpolicy.BuildFromOutboundJSON(tc.format, outbound)
			require.NoError(t, err)
			assert.Equal(t, bodyPolicy.HasCatalog(), policy.HasCatalog())
			assert.Equal(t, tc.wantNoTool, !policy.HasCatalog())
			if tc.allowed != nil {
				require.NoError(t, policy.ValidateCall(*tc.allowed))
				require.NoError(t, bodyPolicy.ValidateCall(*tc.allowed))
			}
			assert.Error(t, policy.ValidateCall(tc.disallowed))
			assert.Error(t, bodyPolicy.ValidateCall(tc.disallowed))
		})
	}
}

func TestInvalidParamOverrideFailsBeforePolicyCaptureAndClearsOldAttempt(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	old, err := toolpolicy.BuildFromResponses(json.RawMessage(`[{"type":"function","name":"old"}]`), nil)
	require.NoError(t, err)
	toolpolicy.Set(c, old)

	info := &relaycommon.RelayInfo{
		RelayFormat: types.RelayFormatOpenAI,
		ChannelMeta: &relaycommon.ChannelMeta{
			ParamOverride: map[string]any{
				"operations": []any{map[string]any{"path": "tool_choice", "mode": "trim_prefix"}},
			},
		},
	}
	body, apiErr := finalizeOutboundToolPolicy(
		c, info, &dto.GeneralOpenAIRequest{},
		[]byte(`{"tools":[{"type":"function","function":{"name":"lookup"}}],"tool_choice":"auto"}`),
	)

	require.NotNil(t, apiErr)
	assert.Nil(t, body)
	assert.Equal(t, types.ErrorCodeChannelParamOverrideInvalid, apiErr.GetErrorCode())
	assert.True(t, types.IsSkipRetryError(apiErr))
	_, ok := toolpolicy.FromContext(c)
	assert.False(t, ok)
}

func TestSemanticallyInvalidOverrideFailsBeforePolicyCapture(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	info := &relaycommon.RelayInfo{
		RelayFormat: types.RelayFormatOpenAIResponses,
		ChannelMeta: &relaycommon.ChannelMeta{
			ParamOverride: map[string]any{"tool_choice": "sometimes"},
		},
	}

	body, apiErr := finalizeOutboundToolPolicy(
		c, info, &dto.OpenAIResponsesRequest{},
		[]byte(`{"tools":[{"type":"function","name":"lookup"}],"tool_choice":"auto"}`),
	)

	require.NotNil(t, apiErr)
	assert.Nil(t, body)
	assert.Equal(t, types.ErrorCodeChannelParamOverrideInvalid, apiErr.GetErrorCode())
	assert.True(t, types.IsSkipRetryError(apiErr))
	_, ok := toolpolicy.FromContext(c)
	assert.False(t, ok)
}

func TestGatewayDanglingRepairUsesPostOverrideFinalPolicy(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tests := []struct {
		name     string
		override map[string]any
	}{
		{
			name:     "choice changed to none",
			override: map[string]any{"tool_choice": "none"},
		},
		{
			name: "forced named choice mismatch",
			override: map[string]any{
				"tools": []any{
					map[string]any{"type": "function", "function": map[string]any{"name": "lookup"}},
					map[string]any{"type": "function", "function": map[string]any{"name": "save"}},
				},
				"tool_choice": map[string]any{"type": "function", "function": map[string]any{"name": "save"}},
			},
		},
		{
			name: "catalog replaced",
			override: map[string]any{
				"tools": []any{map[string]any{"type": "function", "function": map[string]any{"name": "save"}}},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
			info := &relaycommon.RelayInfo{
				RelayFormat: types.RelayFormatOpenAI,
				ChannelMeta: &relaycommon.ChannelMeta{
					ApiType:       constant.APITypeGateway,
					ParamOverride: tc.override,
				},
			}
			body := []byte(`{"tools":[{"type":"function","function":{"name":"lookup"}}],"tool_choice":"auto",` +
				`"messages":[{"role":"assistant","tool_calls":[{"id":"call_1","type":"function",` +
				`"function":{"name":"lookup","arguments":"{}"}}]}]}`)

			out, apiErr := finalizeOutboundToolPolicy(c, info, &dto.GeneralOpenAIRequest{}, body)

			assert.Nil(t, out)
			require.NotNil(t, apiErr)
			assert.Equal(t, http.StatusBadRequest, apiErr.StatusCode)
			assert.Equal(t, types.ErrorCodeInvalidRequest, apiErr.GetErrorCode())
			assert.True(t, types.IsSkipRetryError(apiErr))
			_, ok := toolpolicy.FromContext(c)
			assert.False(t, ok)
		})
	}
}
