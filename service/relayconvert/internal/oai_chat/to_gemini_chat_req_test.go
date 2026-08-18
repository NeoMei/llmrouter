package oaichat

import (
	"encoding/json"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOpenAIChatRequestToGeminiNormalizesToolIDsAndPreservesBigInt(t *testing.T) {
	request := chatRequestWithToolHistory("bad/id", `{"n":184467440737095516160}`)
	got, err := OpenAIChatRequestToGeminiGenerateContent(nil, request, nil)
	require.NoError(t, err)
	require.Len(t, got.Contents, 2)
	call := got.Contents[0].Parts[0].FunctionCall
	result := got.Contents[1].Parts[0].FunctionResponse
	require.NotNil(t, call)
	require.NotNil(t, result)
	assert.NotEqual(t, "bad/id", string(call.ID))
	assert.Equal(t, call.ID, result.ID)
	raw, err := common.Marshal(call.Arguments)
	require.NoError(t, err)
	assert.Equal(t, `{"n":184467440737095516160}`, string(raw))
}

func TestOpenAIChatRequestToGeminiRejectsMalformedArguments(t *testing.T) {
	for _, args := range []string{`{bad-json`, `[]`, `{"x":1,"x":2}`} {
		t.Run(args, func(t *testing.T) {
			_, err := OpenAIChatRequestToGeminiGenerateContent(nil, chatRequestWithToolHistory("call_1", args), nil)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "arguments")
		})
	}
}

func TestOpenAIChatRequestToGeminiRejectsUnknownToolResultID(t *testing.T) {
	request := chatRequestWithToolHistory("call_1", `{}`)
	request.Messages[1].ToolCallId = "unknown"
	_, err := OpenAIChatRequestToGeminiGenerateContent(nil, request, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown tool result id")
}

func TestOpenAIChatRequestToGeminiRejectsEmptyToolResultID(t *testing.T) {
	_, err := OpenAIChatRequestToGeminiGenerateContent(nil, chatRequestWithToolHistory("", `{}`), nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown tool result id")
}

func TestOpenAIChatRequestToGeminiRejectsDuplicateToolCallID(t *testing.T) {
	_, err := OpenAIChatRequestToGeminiGenerateContent(nil, chatRequestWithCalls(
		dto.ToolCallRequest{ID: "call_1", Type: "function", Function: dto.FunctionRequest{Name: "first", Arguments: `{}`}},
		dto.ToolCallRequest{ID: "call_1", Type: "function", Function: dto.FunctionRequest{Name: "second", Arguments: `{}`}},
	), nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "duplicate tool call id")
}

func TestOpenAIChatRequestToGeminiAssignsUniqueOrdinalsToEmptyToolCallIDs(t *testing.T) {
	got, err := OpenAIChatRequestToGeminiGenerateContent(nil, chatRequestWithCalls(
		dto.ToolCallRequest{Type: "function", Function: dto.FunctionRequest{Name: "first", Arguments: `{}`}},
		dto.ToolCallRequest{Type: "function", Function: dto.FunctionRequest{Name: "second", Arguments: `{}`}},
	), nil)
	require.NoError(t, err)
	require.Len(t, got.Contents, 1)
	require.Len(t, got.Contents[0].Parts, 2)
	assert.NotEmpty(t, got.Contents[0].Parts[0].FunctionCall.ID)
	assert.NotEqual(t, got.Contents[0].Parts[0].FunctionCall.ID, got.Contents[0].Parts[1].FunctionCall.ID)
}

func chatRequestWithToolHistory(id, args string) dto.GeneralOpenAIRequest {
	toolCalls, _ := common.Marshal([]dto.ToolCallRequest{{
		ID: id, Type: "function", Function: dto.FunctionRequest{Name: "lookup", Arguments: args},
	}})
	name := "lookup"
	return dto.GeneralOpenAIRequest{
		Model: "gemini-test",
		Messages: []dto.Message{
			{Role: "assistant", Content: "", ToolCalls: json.RawMessage(toolCalls)},
			{Role: "tool", Content: `{"ok":true}`, ToolCallId: id, Name: &name},
		},
	}
}

func chatRequestWithCalls(calls ...dto.ToolCallRequest) dto.GeneralOpenAIRequest {
	toolCalls, _ := common.Marshal(calls)
	return dto.GeneralOpenAIRequest{
		Model:    "test-model",
		Messages: []dto.Message{{Role: "assistant", Content: "", ToolCalls: json.RawMessage(toolCalls)}},
	}
}
