package oaichat

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The Claude Messages API rejects an empty tools array, so the converter must
// omit the field entirely instead of forwarding an empty list.
func TestOpenAIChatRequestToClaudeMessagesOmitsEmptyTools(t *testing.T) {
	maxTokens := uint(16)
	tests := []struct {
		name      string
		request   dto.GeneralOpenAIRequest
		wantTools bool
	}{
		{
			name: "omitted tools",
			request: dto.GeneralOpenAIRequest{
				Model:     "claude-test",
				MaxTokens: &maxTokens,
				Messages:  []dto.Message{{Role: "user", Content: "hi"}},
			},
		},
		{
			name: "explicit empty tools",
			request: dto.GeneralOpenAIRequest{
				Model:     "claude-test",
				MaxTokens: &maxTokens,
				Messages:  []dto.Message{{Role: "user", Content: "hi"}},
				Tools:     []dto.ToolCallRequest{},
			},
		},
		{
			name: "function tool",
			request: dto.GeneralOpenAIRequest{
				Model:     "claude-test",
				MaxTokens: &maxTokens,
				Messages:  []dto.Message{{Role: "user", Content: "hi"}},
				Tools: []dto.ToolCallRequest{{
					Type: "function",
					Function: dto.FunctionRequest{
						Name:        "get_weather",
						Description: "Get weather by city",
						Parameters: map[string]any{
							"type":       "object",
							"properties": map[string]any{"city": map[string]any{"type": "string"}},
							"required":   []any{"city"},
						},
					},
				}},
			},
			wantTools: true,
		},
		{
			name: "web search only",
			request: dto.GeneralOpenAIRequest{
				Model:            "claude-test",
				MaxTokens:        &maxTokens,
				Messages:         []dto.Message{{Role: "user", Content: "hi"}},
				WebSearchOptions: &dto.WebSearchOptions{SearchContextSize: "low"},
			},
			wantTools: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := OpenAIChatRequestToClaudeMessages(nil, test.request)
			require.NoError(t, err)

			body, err := common.Marshal(got)
			require.NoError(t, err)

			if test.wantTools {
				assert.NotNil(t, got.Tools)
				assert.Contains(t, string(body), `"tools":`)
				return
			}
			assert.Nil(t, got.Tools)
			assert.NotContains(t, string(body), `"tools":`)
		})
	}
}

func TestOpenAIChatRequestToClaudeNormalizesToolIDsAndPreservesBigInt(t *testing.T) {
	request := chatRequestWithToolHistory("bad/id", `{"n":184467440737095516160}`)
	got, err := OpenAIChatRequestToClaudeMessages(nil, request)
	require.NoError(t, err)
	use := findClaudePart(t, got.Messages, "tool_use")
	result := findClaudePart(t, got.Messages, "tool_result")
	assert.NotEqual(t, "bad/id", use.Id)
	assert.Equal(t, use.Id, result.ToolUseId)
	raw, err := common.Marshal(use.Input)
	require.NoError(t, err)
	assert.Equal(t, `{"n":184467440737095516160}`, string(raw))
}

func findClaudePart(t *testing.T, messages []dto.ClaudeMessage, partType string) dto.ClaudeMediaMessage {
	t.Helper()
	for _, message := range messages {
		parts, ok := message.Content.([]dto.ClaudeMediaMessage)
		if !ok {
			continue
		}
		for _, part := range parts {
			if part.Type == partType {
				return part
			}
		}
	}
	t.Fatalf("missing Claude part %q", partType)
	return dto.ClaudeMediaMessage{}
}

func TestOpenAIChatRequestToClaudeRejectsMalformedArguments(t *testing.T) {
	for _, args := range []string{`{bad-json`, `[]`, `{"x":1,"x":2}`} {
		t.Run(args, func(t *testing.T) {
			_, err := OpenAIChatRequestToClaudeMessages(nil, chatRequestWithToolHistory("call_1", args))
			require.Error(t, err)
			assert.Contains(t, err.Error(), "arguments")
		})
	}
}

func TestOpenAIChatRequestToClaudeRejectsUnknownToolResultID(t *testing.T) {
	request := chatRequestWithToolHistory("call_1", `{}`)
	request.Messages[1].ToolCallId = "unknown"
	_, err := OpenAIChatRequestToClaudeMessages(nil, request)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown tool result id")
}

func TestOpenAIChatRequestToClaudeRejectsEmptyToolResultID(t *testing.T) {
	_, err := OpenAIChatRequestToClaudeMessages(nil, chatRequestWithToolHistory("", `{}`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown tool result id")
}

func TestOpenAIChatRequestToClaudeRejectsDuplicateToolCallID(t *testing.T) {
	_, err := OpenAIChatRequestToClaudeMessages(nil, chatRequestWithCalls(
		dto.ToolCallRequest{ID: "call_1", Type: "function", Function: dto.FunctionRequest{Name: "first", Arguments: `{}`}},
		dto.ToolCallRequest{ID: "call_1", Type: "function", Function: dto.FunctionRequest{Name: "second", Arguments: `{}`}},
	))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "duplicate tool call id")
}

func TestOpenAIChatRequestToClaudeAssignsUniqueOrdinalsToEmptyToolCallIDs(t *testing.T) {
	got, err := OpenAIChatRequestToClaudeMessages(nil, chatRequestWithCalls(
		dto.ToolCallRequest{Type: "function", Function: dto.FunctionRequest{Name: "first", Arguments: `{}`}},
		dto.ToolCallRequest{Type: "function", Function: dto.FunctionRequest{Name: "second", Arguments: `{}`}},
	))
	require.NoError(t, err)
	uses := collectClaudeParts(got.Messages, "tool_use")
	require.Len(t, uses, 2)
	assert.NotEmpty(t, uses[0].Id)
	assert.NotEqual(t, uses[0].Id, uses[1].Id)
}

func collectClaudeParts(messages []dto.ClaudeMessage, partType string) []dto.ClaudeMediaMessage {
	var found []dto.ClaudeMediaMessage
	for _, message := range messages {
		parts, ok := message.Content.([]dto.ClaudeMediaMessage)
		if !ok {
			continue
		}
		for _, part := range parts {
			if part.Type == partType {
				found = append(found, part)
			}
		}
	}
	return found
}
