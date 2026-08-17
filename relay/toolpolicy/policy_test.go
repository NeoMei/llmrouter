package toolpolicy

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPolicyRejectsUndeclaredAndForcedChoiceMismatch(t *testing.T) {
	policy, err := BuildFromResponses(
		json.RawMessage(`[{"type":"function","name":"lookup"},{"type":"function","name":"save"}]`),
		json.RawMessage(`{"type":"function","name":"lookup"}`),
	)
	require.NoError(t, err)
	require.NoError(t, policy.ValidateCall(Call{ID: "call_1", Name: "lookup", Arguments: json.RawMessage(`{"q":"x"}`)}))
	assert.ErrorIs(t, policy.ValidateCall(Call{ID: "call_2", Name: "save", Arguments: json.RawMessage(`{}`)}), ErrDisallowedTool)
	assert.ErrorIs(t, policy.ValidateCall(Call{ID: "call_3", Name: "missing", Arguments: json.RawMessage(`{}`)}), ErrUndeclaredTool)
}

func TestPolicyWithoutCatalogDoesNotWildcardToolCalls(t *testing.T) {
	policy, err := BuildFromResponses(nil, nil)
	require.NoError(t, err)
	assert.False(t, policy.HasCatalog())
	assert.ErrorIs(t, policy.ValidateCall(Call{ID: "call_1", Name: "unexpected", Arguments: json.RawMessage(`{}`)}), ErrUndeclaredTool)
}

func TestPolicyValidatesCallShapeAndKind(t *testing.T) {
	policy, err := BuildFromResponses(json.RawMessage(`[
		{"type":"function","name":"lookup"},
		{"type":"custom","name":"shell"}
	]`), nil)
	require.NoError(t, err)

	tests := []struct {
		name string
		call Call
		want error
	}{
		{name: "empty id", call: Call{Name: "lookup", Arguments: json.RawMessage(`{}`)}, want: ErrMalformedToolCall},
		{name: "blank id", call: Call{ID: " \t", Name: "lookup", Arguments: json.RawMessage(`{}`)}, want: ErrMalformedToolCall},
		{name: "empty name", call: Call{ID: "call_1", Arguments: json.RawMessage(`{}`)}, want: ErrMalformedToolCall},
		{name: "wrong kind", call: Call{ID: "call_1", Name: "lookup", Kind: ToolKindCustom, Arguments: json.RawMessage(`{}`)}, want: ErrToolKindMismatch},
		{name: "malformed function arguments", call: Call{ID: "call_1", Name: "lookup", Arguments: json.RawMessage(`{bad`)}, want: ErrMalformedArguments},
		{name: "function arguments must be object", call: Call{ID: "call_1", Name: "lookup", Arguments: json.RawMessage(`"x"`)}, want: ErrMalformedArguments},
		{name: "custom accepts string", call: Call{ID: "call_1", Name: "shell", Kind: ToolKindCustom, Arguments: json.RawMessage(`"pwd"`)}, want: nil},
		{name: "custom accepts object", call: Call{ID: "call_1", Name: "shell", Kind: ToolKindCustom, Arguments: json.RawMessage(`{"cmd":"pwd"}`)}, want: nil},
		{name: "custom rejects number", call: Call{ID: "call_1", Name: "shell", Kind: ToolKindCustom, Arguments: json.RawMessage(`184467440737095516160`)}, want: ErrMalformedArguments},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := policy.ValidateCall(tc.call)
			if tc.want == nil {
				require.NoError(t, err)
				return
			}
			assert.ErrorIs(t, err, tc.want)
		})
	}
}

func TestPolicyChoiceNoneRejectsEveryCall(t *testing.T) {
	policy, err := BuildFromChat([]dto.ToolCallRequest{{Type: "function", Function: dto.FunctionRequest{Name: "lookup"}}}, "none")
	require.NoError(t, err)
	assert.ErrorIs(t, policy.ValidateCall(Call{ID: "call_1", Name: "lookup", Arguments: json.RawMessage(`{}`)}), ErrDisallowedTool)
}

func TestPolicyDeclarationsNormalizeDuplicatesButPreserveCase(t *testing.T) {
	policy, err := BuildFromResponses(json.RawMessage(`[
		{"type":"function","name":" lookup ","parameters":{"maximum":184467440737095516160}},
		{"type":"function","name":"lookup"},
		{"type":"function","name":"Lookup"}
	]`), nil)
	require.NoError(t, err)
	assert.Len(t, policy.allowed, 2)
	require.NoError(t, policy.ValidateCall(Call{ID: " call_1 ", Name: " lookup ", Arguments: json.RawMessage(`{"n":184467440737095516160}`)}))
	require.NoError(t, policy.ValidateCall(Call{ID: "call_2", Name: "Lookup", Arguments: json.RawMessage(`{}`)}))
	assert.ErrorIs(t, policy.ValidateCall(Call{ID: "call_3", Name: "LOOKUP", Arguments: json.RawMessage(`{}`)}), ErrUndeclaredTool)
}

func TestPolicyRejectsConflictingDuplicateDeclaration(t *testing.T) {
	_, err := BuildFromResponses(json.RawMessage(`[
		{"type":"function","name":"lookup"},
		{"type":"custom","name":" lookup "}
	]`), nil)
	assert.ErrorIs(t, err, ErrInvalidPolicy)
}

func TestBuildFromChatNormalizesFunctionAndCustomChoices(t *testing.T) {
	tools := []dto.ToolCallRequest{
		{Type: "function", Function: dto.FunctionRequest{Name: "lookup"}},
		{Type: "custom", Custom: json.RawMessage(`{"name":"shell","format":{"type":"text"}}`)},
	}
	functionPolicy, err := BuildFromChat(tools, map[string]any{
		"type":     "function",
		"function": map[string]any{"name": "lookup"},
	})
	require.NoError(t, err)
	require.NoError(t, functionPolicy.ValidateCall(Call{ID: "call_1", Name: "lookup", Arguments: json.RawMessage(`{}`)}))
	assert.ErrorIs(t, functionPolicy.ValidateCall(Call{ID: "call_2", Name: "shell", Kind: ToolKindCustom, Arguments: json.RawMessage(`"pwd"`)}), ErrDisallowedTool)

	customPolicy, err := BuildFromChat(tools, map[string]any{
		"type":   "custom",
		"custom": map[string]any{"name": "shell"},
	})
	require.NoError(t, err)
	require.NoError(t, customPolicy.ValidateCall(Call{ID: "call_3", Name: "shell", Kind: ToolKindCustom, Arguments: json.RawMessage(`"pwd"`)}))
}

func TestBuildFromClaudeNormalizesNativeChoice(t *testing.T) {
	tools := []any{
		map[string]any{"name": "lookup", "input_schema": map[string]any{"type": "object"}},
		map[string]any{"name": "save", "input_schema": map[string]any{"type": "object"}},
	}
	policy, err := BuildFromClaude(tools, map[string]any{"type": "tool", "name": "lookup"})
	require.NoError(t, err)
	require.NoError(t, policy.ValidateCall(Call{ID: "toolu_1", Name: "lookup", Arguments: json.RawMessage(`{}`)}))
	assert.ErrorIs(t, policy.ValidateCall(Call{ID: "toolu_2", Name: "save", Arguments: json.RawMessage(`{}`)}), ErrDisallowedTool)

	required, err := BuildFromClaude(tools, &dto.ClaudeToolChoice{Type: "any"})
	require.NoError(t, err)
	assert.Equal(t, ChoiceRequired, required.choice)
}

func TestBuildFromGeminiRestrictsAllowedFunctionNames(t *testing.T) {
	tools := json.RawMessage(`[{"functionDeclarations":[{"name":"lookup"},{"name":"save"}]}]`)
	policy, err := BuildFromGemini(tools, &dto.ToolConfig{FunctionCallingConfig: &dto.FunctionCallingConfig{
		Mode:                 "ANY",
		AllowedFunctionNames: []string{"lookup"},
	}})
	require.NoError(t, err)
	require.NoError(t, policy.ValidateCall(Call{ID: "call_1", Name: "lookup", Arguments: json.RawMessage(`{}`)}))
	assert.ErrorIs(t, policy.ValidateCall(Call{ID: "call_2", Name: "save", Arguments: json.RawMessage(`{}`)}), ErrDisallowedTool)
}

func TestBuildersRecordProtocolBuiltInDeclarationsWithoutWildcarding(t *testing.T) {
	responses, err := BuildFromResponses(
		json.RawMessage(`[{"type":"web_search_preview"}]`),
		json.RawMessage(`{"type":"web_search_preview"}`),
	)
	require.NoError(t, err)
	assert.True(t, responses.HasCatalog())
	require.NoError(t, responses.ValidateCall(Call{
		ID: "call_1", Name: "web_search_preview", Kind: ToolKindCustom, Arguments: json.RawMessage(`{}`),
	}))

	claude, err := BuildFromClaude(
		[]any{map[string]any{"type": "web_search_20250305", "name": "web_search"}},
		map[string]any{"type": "tool", "name": "web_search"},
	)
	require.NoError(t, err)
	require.NoError(t, claude.ValidateCall(Call{
		ID: "srvtoolu_1", Name: "web_search", Kind: ToolKindCustom, Arguments: json.RawMessage(`{"query":"x"}`),
	}))

	gemini, err := BuildFromGemini(json.RawMessage(`[{"googleSearch":{}}]`), nil)
	require.NoError(t, err)
	assert.True(t, gemini.HasCatalog())
	require.NoError(t, gemini.ValidateCall(Call{
		ID: "call_2", Name: "googleSearch", Kind: ToolKindCustom, Arguments: json.RawMessage(`{}`),
	}))
}

func TestToolPolicyErrorCodesAreStable(t *testing.T) {
	assert.Equal(t, types.ErrorCode("upstream_tool_policy_violation"), types.ErrorCodeUpstreamToolPolicyViolation)
	assert.Equal(t, types.ErrorCode("upstream_tool_protocol_invalid"), types.ErrorCodeUpstreamToolProtocolInvalid)
}

func TestBuildersRejectInvalidToolDefinitionsAndChoices(t *testing.T) {
	tests := []struct {
		name  string
		build func() (Policy, error)
	}{
		{name: "chat empty function name", build: func() (Policy, error) {
			return BuildFromChat([]dto.ToolCallRequest{{Type: "function"}}, nil)
		}},
		{name: "chat unknown type", build: func() (Policy, error) {
			return BuildFromChat([]dto.ToolCallRequest{{Type: "other", Function: dto.FunctionRequest{Name: "lookup"}}}, nil)
		}},
		{name: "chat malformed named choice", build: func() (Policy, error) {
			return BuildFromChat([]dto.ToolCallRequest{{Type: "function", Function: dto.FunctionRequest{Name: "lookup"}}}, map[string]any{"type": "function"})
		}},
		{name: "responses malformed tools", build: func() (Policy, error) {
			return BuildFromResponses(json.RawMessage(`[{`), nil)
		}},
		{name: "responses duplicate object key", build: func() (Policy, error) {
			return BuildFromResponses(json.RawMessage(`[{"type":"function","name":"a","name":"b"}]`), nil)
		}},
		{name: "responses unknown choice", build: func() (Policy, error) {
			return BuildFromResponses(json.RawMessage(`[{"type":"function","name":"lookup"}]`), json.RawMessage(`"sometimes"`))
		}},
		{name: "responses empty explicit choice", build: func() (Policy, error) {
			return BuildFromResponses(json.RawMessage(`[{"type":"function","name":"lookup"}]`), json.RawMessage(`""`))
		}},
		{name: "responses named choice missing declaration", build: func() (Policy, error) {
			return BuildFromResponses(json.RawMessage(`[{"type":"function","name":"lookup"}]`), json.RawMessage(`{"type":"function","name":"save"}`))
		}},
		{name: "claude malformed tool", build: func() (Policy, error) {
			return BuildFromClaude([]any{map[string]any{"input_schema": map[string]any{"type": "object"}}}, nil)
		}},
		{name: "claude invalid choice", build: func() (Policy, error) {
			return BuildFromClaude([]any{map[string]any{"name": "lookup"}}, map[string]any{"type": "tool"})
		}},
		{name: "gemini malformed tools", build: func() (Policy, error) {
			return BuildFromGemini(json.RawMessage(`{"functionDeclarations":"bad"}`), nil)
		}},
		{name: "gemini invalid mode", build: func() (Policy, error) {
			return BuildFromGemini(json.RawMessage(`[{"functionDeclarations":[{"name":"lookup"}]}]`), &dto.ToolConfig{FunctionCallingConfig: &dto.FunctionCallingConfig{Mode: "MAYBE"}})
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := tc.build()
			assert.ErrorIs(t, err, ErrInvalidPolicy)
		})
	}
}

func TestContextPolicyIsDeepCopiedOnSetAndGet(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(nil)
	original, err := BuildFromResponses(json.RawMessage(`[{"type":"function","name":"lookup"}]`), nil)
	require.NoError(t, err)

	Set(c, original)
	original.allowed["mutated"] = Declaration{Name: "mutated", Kind: ToolKindFunction}

	first, ok := FromContext(c)
	require.True(t, ok)
	assert.NotContains(t, first.allowed, "mutated")
	first.allowed["another"] = Declaration{Name: "another", Kind: ToolKindFunction}

	second, ok := FromContext(c)
	require.True(t, ok)
	assert.NotContains(t, second.allowed, "another")
	assert.Contains(t, second.allowed, "lookup")

	Clear(c)
	_, ok = FromContext(c)
	assert.False(t, ok)
}

func TestInvalidPolicyErrorsRemainClassifiable(t *testing.T) {
	_, err := BuildFromResponses(json.RawMessage(`[{`), nil)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrInvalidPolicy))
}
