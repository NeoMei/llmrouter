package oairesponses

import (
	"encoding/json"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOpenAIResponsesRequestToClaudeNormalizesToolIDsAndPreservesBigInt(t *testing.T) {
	req := responsesRequestWithToolHistory("bad/id", `{"n":184467440737095516160}`)
	got, err := OpenAIResponsesRequestToClaudeMessages(nil, req)
	require.NoError(t, err)
	use := findResponsesClaudePart(t, got.Messages, "tool_use")
	result := findResponsesClaudePart(t, got.Messages, "tool_result")
	assert.NotEqual(t, "bad/id", use.Id)
	assert.Equal(t, use.Id, result.ToolUseId)
	raw, err := common.Marshal(use.Input)
	require.NoError(t, err)
	assert.Equal(t, `{"n":184467440737095516160}`, string(raw))
}

func findResponsesClaudePart(t *testing.T, messages []dto.ClaudeMessage, partType string) dto.ClaudeMediaMessage {
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

func TestOpenAIResponsesRequestToClaudeRejectsMalformedArguments(t *testing.T) {
	for _, args := range []string{`{bad-json`, `[]`, `{"x":1,"x":2}`} {
		t.Run(args, func(t *testing.T) {
			_, err := OpenAIResponsesRequestToClaudeMessages(nil, responsesRequestWithToolHistory("call_1", args))
			require.Error(t, err)
			assert.Contains(t, err.Error(), "arguments")
		})
	}
}

func TestOpenAIResponsesRequestToClaudeRejectsUnknownToolResultID(t *testing.T) {
	req := responsesRequestWithToolHistory("call_1", `{}`)
	req.Input = json.RawMessage(`[{"type":"function_call","call_id":"call_1","name":"lookup","arguments":"{}"},{"type":"function_call_output","call_id":"unknown","output":"ok"}]`)
	_, err := OpenAIResponsesRequestToClaudeMessages(nil, req)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown tool result id")
}

func TestOpenAIResponsesRequestToClaudeRejectsEmptyToolResultID(t *testing.T) {
	_, err := OpenAIResponsesRequestToClaudeMessages(nil, responsesRequestWithToolHistory("", `{}`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown tool result id")
}

func TestOpenAIResponsesRequestToClaudeRejectsDuplicateToolCallID(t *testing.T) {
	_, err := OpenAIResponsesRequestToClaudeMessages(nil, responsesRequestWithCalls("call_1", "call_1"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "duplicate tool call id")
}

func TestOpenAIResponsesRequestToClaudeAssignsUniqueOrdinalsToEmptyToolCallIDs(t *testing.T) {
	got, err := OpenAIResponsesRequestToClaudeMessages(nil, responsesRequestWithCalls("", ""))
	require.NoError(t, err)
	uses := collectResponsesClaudeParts(got.Messages, "tool_use")
	require.Len(t, uses, 2)
	assert.NotEmpty(t, uses[0].Id)
	assert.NotEqual(t, uses[0].Id, uses[1].Id)
}

func responsesRequestWithToolHistory(id, args string) *dto.OpenAIResponsesRequest {
	input, _ := common.Marshal([]map[string]any{
		{"type": "function_call", "call_id": id, "name": "lookup", "arguments": args},
		{"type": "function_call_output", "call_id": id, "output": "ok"},
	})
	return &dto.OpenAIResponsesRequest{Model: "test-model", Input: input}
}

func responsesRequestWithCalls(ids ...string) *dto.OpenAIResponsesRequest {
	items := make([]map[string]any, 0, len(ids))
	for i, id := range ids {
		items = append(items, map[string]any{
			"type": "function_call", "call_id": id, "name": "lookup", "arguments": `{}`, "ordinal": i,
		})
	}
	input, _ := common.Marshal(items)
	return &dto.OpenAIResponsesRequest{Model: "test-model", Input: input}
}

func collectResponsesClaudeParts(messages []dto.ClaudeMessage, partType string) []dto.ClaudeMediaMessage {
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

func TestConfigurationUpdateDoesNotCreateClaudePlaceholder(t *testing.T) {
	input := json.RawMessage(`[{"type":"message","role":"user","content":"go"},{"type":"configuration_update","reasoning":{"effort":"high"}}]`)
	got, err := OpenAIResponsesRequestToClaudeMessages(nil, &dto.OpenAIResponsesRequest{Model: "claude-test", Input: input, MaxOutputTokens: common.GetPointer(uint(32))})
	require.NoError(t, err)
	require.Len(t, got.Messages, 1)
	parts, err := got.Messages[0].ParseContent()
	require.NoError(t, err)
	require.Len(t, parts, 1)
	assert.Equal(t, "go", parts[0].GetText())
}
