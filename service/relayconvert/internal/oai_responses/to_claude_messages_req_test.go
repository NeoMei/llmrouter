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

func responsesRequestWithToolHistory(id, args string) *dto.OpenAIResponsesRequest {
	input, _ := common.Marshal([]map[string]any{
		{"type": "function_call", "call_id": id, "name": "lookup", "arguments": args},
		{"type": "function_call_output", "call_id": id, "output": "ok"},
	})
	return &dto.OpenAIResponsesRequest{Model: "test-model", Input: input}
}
