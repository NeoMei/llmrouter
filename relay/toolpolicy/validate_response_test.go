package toolpolicy

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func responsePolicy(t *testing.T, names ...string) Policy {
	t.Helper()
	tools := make([]map[string]any, 0, len(names))
	for _, name := range names {
		tools = append(tools, map[string]any{"type": "function", "name": name})
	}
	raw, err := common.Marshal(tools)
	require.NoError(t, err)
	policy, err := BuildFromResponses(raw, nil)
	require.NoError(t, err)
	return policy
}

func forcedResponsePolicy(t *testing.T, forced string, names ...string) Policy {
	t.Helper()
	tools := make([]map[string]any, 0, len(names))
	for _, name := range names {
		tools = append(tools, map[string]any{"type": "function", "name": name})
	}
	raw, err := common.Marshal(tools)
	require.NoError(t, err)
	choice, err := common.Marshal(map[string]any{"type": "function", "name": forced})
	require.NoError(t, err)
	policy, err := BuildFromResponses(raw, choice)
	require.NoError(t, err)
	return policy
}

func TestValidateChatResponseChecksEveryChoiceAndCall(t *testing.T) {
	policy := responsePolicy(t, "lookup")
	valid := dto.Message{Role: "assistant"}
	valid.SetToolCalls([]dto.ToolCallResponse{{
		ID: "call_1", Type: "function",
		Function: dto.FunctionResponse{Name: "lookup", Arguments: `{"q":"x"}`},
	}})
	require.NoError(t, ValidateChatResponse(policy, &dto.OpenAITextResponse{
		Choices: []dto.OpenAITextResponseChoice{{Index: 0, Message: dto.Message{Role: "assistant", Content: "text"}}, {Index: 1, Message: valid}},
	}))

	invalid := dto.Message{Role: "assistant"}
	invalid.SetToolCalls([]dto.ToolCallResponse{{
		ID: "call_2", Type: "function",
		Function: dto.FunctionResponse{Name: "delete_all", Arguments: `{}`},
	}})
	err := ValidateChatResponse(policy, &dto.OpenAITextResponse{
		Choices: []dto.OpenAITextResponseChoice{{Index: 0, Message: valid}, {Index: 1, Message: invalid}},
	})
	assert.ErrorIs(t, err, ErrUndeclaredTool)
}

func TestValidateChatResponseRejectsMalformedNativeCalls(t *testing.T) {
	policy := responsePolicy(t, "lookup")
	tests := []struct {
		name  string
		calls json.RawMessage
		is    error
	}{
		{name: "wrong tool calls type", calls: json.RawMessage(`{"id":"call_1"}`), is: ErrMalformedToolCall},
		{name: "missing id", calls: json.RawMessage(`[{"type":"function","function":{"name":"lookup","arguments":"{}"}}]`), is: ErrMalformedToolCall},
		{name: "missing name", calls: json.RawMessage(`[{"id":"call_1","type":"function","function":{"arguments":"{}"}}]`), is: ErrMalformedToolCall},
		{name: "missing call type", calls: json.RawMessage(`[{"id":"call_1","function":{"name":"lookup","arguments":"{}"}}]`), is: ErrMalformedToolCall},
		{name: "wrong call type", calls: json.RawMessage(`[{"id":"call_1","type":"computer","function":{"name":"lookup","arguments":"{}"}}]`), is: ErrMalformedToolCall},
		{name: "malformed arguments", calls: json.RawMessage(`[{"id":"call_1","type":"function","function":{"name":"lookup","arguments":"{"}}]`), is: ErrMalformedArguments},
		{name: "wrong argument type", calls: json.RawMessage(`[{"id":"call_1","type":"function","function":{"name":"lookup","arguments":"[]"}}]`), is: ErrMalformedArguments},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			response := &dto.OpenAITextResponse{Choices: []dto.OpenAITextResponseChoice{{Message: dto.Message{ToolCalls: tt.calls}}}}
			assert.ErrorIs(t, ValidateChatResponse(policy, response), tt.is)
		})
	}
}

func TestValidateResponsesResponseChecksCallsAndAllowsRefusalOrIncompleteText(t *testing.T) {
	policy := responsePolicy(t, "lookup")
	valid := &dto.OpenAIResponsesResponse{
		Status: json.RawMessage(`"completed"`),
		Output: []dto.ResponsesOutput{{Type: "function_call", CallId: "call_1", Name: "lookup", Arguments: json.RawMessage(`"{\"q\":\"x\"}"`), Status: "completed"}},
	}
	require.NoError(t, ValidateResponsesResponse(policy, valid))

	undeclared := *valid
	undeclared.Output = append([]dto.ResponsesOutput(nil), valid.Output...)
	undeclared.Output[0].Name = "delete_all"
	assert.ErrorIs(t, ValidateResponsesResponse(policy, &undeclared), ErrUndeclaredTool)

	incompleteCall := *valid
	incompleteCall.Output = append([]dto.ResponsesOutput(nil), valid.Output...)
	incompleteCall.Output[0].Status = "in_progress"
	assert.ErrorIs(t, ValidateResponsesResponse(policy, &incompleteCall), ErrIncompleteToolCall)

	refusal := &dto.OpenAIResponsesResponse{Status: json.RawMessage(`"completed"`), Output: []dto.ResponsesOutput{{Type: "message", Content: []dto.ResponsesOutputContent{{Type: "refusal"}}}}}
	require.NoError(t, ValidateResponsesResponse(policy, refusal))
	incompleteText := &dto.OpenAIResponsesResponse{Status: json.RawMessage(`"incomplete"`), Output: []dto.ResponsesOutput{{Type: "message", Content: []dto.ResponsesOutputContent{{Type: "output_text", Text: "partial"}}}}}
	require.NoError(t, ValidateResponsesResponse(policy, incompleteText))
}

func TestValidateResponsesResponseUsesCustomInputNotFunctionArguments(t *testing.T) {
	policy, err := BuildFromResponses(
		json.RawMessage(`[{"type":"custom","name":"apply_patch"}]`),
		nil,
	)
	require.NoError(t, err)
	require.NoError(t, ValidateResponsesResponse(policy, &dto.OpenAIResponsesResponse{Output: []dto.ResponsesOutput{{
		Type: "custom_tool_call", CallId: "call_1", Name: "apply_patch", Input: json.RawMessage(`"patch body"`), Status: "completed",
	}}}))
	assert.ErrorIs(t, ValidateResponsesResponse(policy, &dto.OpenAIResponsesResponse{Output: []dto.ResponsesOutput{{
		Type: "custom_tool_call", CallId: "call_2", Name: "apply_patch", Input: json.RawMessage(`[]`), Arguments: json.RawMessage(`{}`), Status: "completed",
	}}}), ErrMalformedArguments)
}

func TestValidateResponsesResponseChecksBuiltInCallsExactly(t *testing.T) {
	tools := json.RawMessage(`[{"type":"web_search_preview"}]`)
	policy, err := BuildFromResponses(tools, nil)
	require.NoError(t, err)
	require.NoError(t, ValidateResponsesResponse(policy, &dto.OpenAIResponsesResponse{Output: []dto.ResponsesOutput{{
		Type: "web_search_call", ID: "ws_1", Status: "completed",
	}}}))

	other, err := BuildFromResponses(json.RawMessage(`[{"type":"file_search"}]`), nil)
	require.NoError(t, err)
	assert.ErrorIs(t, ValidateResponsesResponse(other, &dto.OpenAIResponsesResponse{Output: []dto.ResponsesOutput{{
		Type: "web_search_call", ID: "ws_1", Status: "completed",
	}}}), ErrUndeclaredTool)

	image, err := BuildFromResponses(json.RawMessage(`[{"type":"image_generation"}]`), nil)
	require.NoError(t, err)
	require.NoError(t, ValidateResponsesResponse(image, &dto.OpenAIResponsesResponse{Output: []dto.ResponsesOutput{{
		Type: "image_generation_call", ID: "image_1", Status: "completed",
	}}}))

	// A built-in result/output is not a call and remains pass-through.
	require.NoError(t, ValidateResponsesResponse(policy, &dto.OpenAIResponsesResponse{Output: []dto.ResponsesOutput{{
		Type: "web_search_result", ID: "ws_1", Status: "completed",
	}}}))
}

func TestValidateClaudeAndGeminiResponses(t *testing.T) {
	policy := responsePolicy(t, "lookup")
	require.NoError(t, ValidateClaudeResponse(policy, &dto.ClaudeResponse{Content: []dto.ClaudeMediaMessage{
		{Type: "text"},
		{Type: "tool_use", Id: "toolu_1", Name: "lookup", Input: map[string]any{"q": "x"}},
	}}))
	assert.ErrorIs(t, ValidateClaudeResponse(policy, &dto.ClaudeResponse{Content: []dto.ClaudeMediaMessage{
		{Type: "tool_use", Id: "toolu_1", Name: "lookup", Input: []any{"wrong"}},
	}}), ErrMalformedArguments)

	require.NoError(t, ValidateGeminiResponse(policy, &dto.GeminiChatResponse{Candidates: []dto.GeminiChatCandidate{
		{Index: 0, Content: dto.GeminiChatContent{Parts: []dto.GeminiPart{{Text: "text"}}}},
		{Index: 1, Content: dto.GeminiChatContent{Parts: []dto.GeminiPart{{FunctionCall: &dto.FunctionCall{FunctionName: "lookup", Arguments: map[string]any{"q": "x"}}}}}},
	}}))
	assert.ErrorIs(t, ValidateGeminiResponse(policy, &dto.GeminiChatResponse{Candidates: []dto.GeminiChatCandidate{
		{Index: 0, Content: dto.GeminiChatContent{Parts: []dto.GeminiPart{{FunctionCall: &dto.FunctionCall{FunctionName: "delete_all", Arguments: map[string]any{}}}}}},
	}}), ErrUndeclaredTool)
}

func TestRequiredAndNamedPoliciesRejectTextOnlyTerminalResponses(t *testing.T) {
	required, err := BuildFromChat([]dto.ToolCallRequest{{Type: "function", Function: dto.FunctionRequest{Name: "lookup"}}}, "required")
	require.NoError(t, err)
	named, err := BuildFromChat([]dto.ToolCallRequest{{Type: "function", Function: dto.FunctionRequest{Name: "lookup"}}}, map[string]any{"type": "function", "function": map[string]any{"name": "lookup"}})
	require.NoError(t, err)
	auto, err := BuildFromChat([]dto.ToolCallRequest{{Type: "function", Function: dto.FunctionRequest{Name: "lookup"}}}, nil)
	require.NoError(t, err)

	validators := []func(Policy) error{
		func(policy Policy) error {
			return ValidateChatResponse(policy, &dto.OpenAITextResponse{Choices: []dto.OpenAITextResponseChoice{{Message: dto.Message{Role: "assistant", Content: "text"}}}})
		},
		func(policy Policy) error {
			return ValidateResponsesResponse(policy, &dto.OpenAIResponsesResponse{Output: []dto.ResponsesOutput{{Type: "message"}}})
		},
		func(policy Policy) error {
			return ValidateClaudeResponse(policy, &dto.ClaudeResponse{Content: []dto.ClaudeMediaMessage{{Type: "text"}}})
		},
		func(policy Policy) error {
			return ValidateGeminiResponse(policy, &dto.GeminiChatResponse{Candidates: []dto.GeminiChatCandidate{{Content: dto.GeminiChatContent{Parts: []dto.GeminiPart{{Text: "text"}}}}}})
		},
	}
	for _, validate := range validators {
		require.ErrorIs(t, validate(required), ErrDisallowedTool)
		require.ErrorIs(t, validate(named), ErrDisallowedTool)
		require.NoError(t, validate(auto))
	}
}

func TestValidateResponseForcedChoiceMismatchAndErrorClassification(t *testing.T) {
	policy := forcedResponsePolicy(t, "lookup", "lookup", "save")
	response := &dto.OpenAIResponsesResponse{Output: []dto.ResponsesOutput{{
		Type: "function_call", CallId: "call_1", Name: "save", Arguments: json.RawMessage(`{}`), Status: "completed",
	}}}
	err := ValidateResponsesResponse(policy, response)
	require.ErrorIs(t, err, ErrDisallowedTool)
	apiErr := NewResponseValidationError(err)
	assert.Equal(t, types.ErrorCodeUpstreamToolPolicyViolation, apiErr.GetErrorCode())
	assert.False(t, types.IsSkipRetryError(apiErr))

	protocolErr := NewResponseValidationError(ErrIncompleteToolCall)
	assert.Equal(t, types.ErrorCodeUpstreamToolProtocolInvalid, protocolErr.GetErrorCode())
	assert.False(t, types.IsSkipRetryError(protocolErr))
	assert.True(t, errors.Is(protocolErr, ErrIncompleteToolCall))
}

func TestContextValidationFailsClosedForToolCallWhenPolicyIsMissing(t *testing.T) {
	c, _ := gin.CreateTestContext(nil)
	toolMessage := dto.Message{Role: "assistant"}
	toolMessage.SetToolCalls([]dto.ToolCallResponse{{
		ID: "call_1", Type: "function", Function: dto.FunctionResponse{Name: "lookup", Arguments: `{}`},
	}})
	err := ValidateChatResponseContext(c, &dto.OpenAITextResponse{Choices: []dto.OpenAITextResponseChoice{{Message: toolMessage}}})
	require.ErrorIs(t, err, ErrMissingToolPolicy)
	assert.Equal(t, types.ErrorCodeUpstreamToolProtocolInvalid, NewResponseValidationError(err).GetErrorCode())

	text := &dto.OpenAITextResponse{Choices: []dto.OpenAITextResponseChoice{{Message: dto.Message{Role: "assistant", Content: "ok"}}}}
	require.NoError(t, ValidateChatResponseContext(c, text))
}

func TestValidateRawResponseJSONRejectsDuplicateKeysAtAnyDepth(t *testing.T) {
	err := ValidateRawResponseJSON([]byte(`{"choices":[{"message":{"tool_calls":[{"id":"call_1","id":"call_2"}]}}]}`))
	require.Error(t, err)
	assert.Equal(t, types.ErrorCodeUpstreamToolProtocolInvalid, NewResponseValidationError(err).GetErrorCode())
	require.NoError(t, ValidateRawResponseJSON([]byte(`{"choices":[]}`)))
}

func TestResponsesForChatRejectsValidCustomCallAsProtocolIncompatible(t *testing.T) {
	c, _ := gin.CreateTestContext(nil)
	policy, err := BuildFromResponses(json.RawMessage(`[{"type":"custom","name":"apply_patch"}]`), nil)
	require.NoError(t, err)
	Set(c, policy)
	err = ValidateResponsesResponseForChatContext(c, &dto.OpenAIResponsesResponse{Output: []dto.ResponsesOutput{{
		Type: "custom_tool_call", CallId: "call_1", Name: "apply_patch", Input: json.RawMessage(`"patch"`), Status: "completed",
	}}})
	require.ErrorIs(t, err, ErrUnsupportedToolCallConversion)
	assert.Equal(t, types.ErrorCodeUpstreamToolProtocolInvalid, NewResponseValidationError(err).GetErrorCode())
}
