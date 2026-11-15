package openai

import (
	"testing"

	"github.com/QuantumNous/new-api/dto"
	"github.com/stretchr/testify/assert"
)

// A generated failure can use the Chat Completions wire (prompt_tokens /
// completion_tokens) or the Responses wire (input_tokens / output_tokens).
// Normalising must never zero out the vendor's confirmed counters.
func TestResponsesUsagePreservesBothWireSpellings(t *testing.T) {
	chatWire := &dto.Usage{PromptTokens: 37, CompletionTokens: 11, TotalTokens: 48}
	got := responsesUsage(chatWire)
	assert.Equal(t, 37, got.PromptTokens)
	assert.Equal(t, 11, got.CompletionTokens)
	assert.Equal(t, 48, got.TotalTokens)
	assert.Equal(t, 37, got.InputTokens)
	assert.Equal(t, 11, got.OutputTokens)

	responsesWire := &dto.Usage{InputTokens: 23, OutputTokens: 7, TotalTokens: 30}
	got = responsesUsage(responsesWire)
	assert.Equal(t, 23, got.PromptTokens)
	assert.Equal(t, 7, got.CompletionTokens)
	assert.Equal(t, 30, got.TotalTokens)

	partial := &dto.Usage{PromptTokens: 5, OutputTokens: 2}
	got = responsesUsage(partial)
	assert.Equal(t, 5, got.PromptTokens)
	assert.Equal(t, 2, got.CompletionTokens)
	assert.Equal(t, 7, got.TotalTokens)
	assert.Equal(t, 5, got.InputTokens)
	assert.Equal(t, 2, got.OutputTokens)
}
