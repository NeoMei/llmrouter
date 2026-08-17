package oaichat

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/samber/lo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestChatCompletionsRequestToResponsesRequestInstructionsAndTools(t *testing.T) {
	req := &dto.GeneralOpenAIRequest{
		Model: "gpt-test",
		N:     lo.ToPtr(1),
		Messages: []dto.Message{
			{Role: "system", Content: "system rules"},
			{Role: "developer", Content: "developer rules"},
			{Role: "user", Content: []any{
				map[string]any{"type": "text", "text": "look"},
				map[string]any{"type": "image_url", "image_url": map[string]any{"url": "https://example.test/a.png"}},
			}},
			assistantMessageWithTool("partial text", "call_1", "lookup", `{"q":"x"}`),
			{Role: "tool", ToolCallId: "call_1", Content: "tool result"},
		},
	}

	got, err := ChatCompletionsRequestToResponsesRequest(req)
	require.NoError(t, err)

	assert.Equal(t, "gpt-test", got.Model)
	assert.Equal(t, `"system rules\n\ndeveloper rules"`, string(got.Instructions))
	assert.Equal(t, "input_image", gjson.GetBytes(got.Input, "0.content.1.type").String())
	assert.Equal(t, "function_call", gjson.GetBytes(got.Input, "2.type").String())
	assert.Equal(t, "call_1", gjson.GetBytes(got.Input, "2.call_id").String())
	assert.Equal(t, "function_call_output", gjson.GetBytes(got.Input, "3.type").String())
}

func TestChatCompletionsRequestToResponsesRequestRejectsMultipleChoices(t *testing.T) {
	_, err := ChatCompletionsRequestToResponsesRequest(&dto.GeneralOpenAIRequest{
		Model: "gpt-test",
		N:     lo.ToPtr(2),
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "n>1")
}

func TestChatCompletionsRequestToResponsesRequestPreservesPenalties(t *testing.T) {
	tests := []struct {
		name          string
		frequency     *float64
		frequencyWant string
		presence      *float64
		presenceWant  string
	}{
		{name: "positive values", frequency: lo.ToPtr(0.5), frequencyWant: "0.5", presence: lo.ToPtr(1.5), presenceWant: "1.5"},
		{name: "explicit zero values", frequency: lo.ToPtr(0.0), frequencyWant: "0", presence: lo.ToPtr(0.0), presenceWant: "0"},
		{name: "unset stays nil"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ChatCompletionsRequestToResponsesRequest(&dto.GeneralOpenAIRequest{
				Model:            "gpt-test",
				Messages:         []dto.Message{{Role: "user", Content: "hello"}},
				FrequencyPenalty: tt.frequency,
				PresencePenalty:  tt.presence,
			})
			require.NoError(t, err)

			encoded, err := common.Marshal(got)
			require.NoError(t, err)
			if tt.frequencyWant == "" {
				assert.False(t, gjson.GetBytes(encoded, "frequency_penalty").Exists())
			} else {
				assert.Equal(t, tt.frequencyWant, gjson.GetBytes(encoded, "frequency_penalty").Raw)
			}
			if tt.presenceWant == "" {
				assert.False(t, gjson.GetBytes(encoded, "presence_penalty").Exists())
			} else {
				assert.Equal(t, tt.presenceWant, gjson.GetBytes(encoded, "presence_penalty").Raw)
			}
		})
	}
}

func TestChatCompletionsRequestToResponsesRequestPreservesPromptCacheKey(t *testing.T) {
	t.Run("present", func(t *testing.T) {
		key := "session-\"quoted\"\\path\n世界"
		got, err := ChatCompletionsRequestToResponsesRequest(&dto.GeneralOpenAIRequest{
			Model:          "gpt-test",
			Messages:       []dto.Message{{Role: "user", Content: "hello"}},
			PromptCacheKey: key,
		})
		require.NoError(t, err)

		encoded, err := common.Marshal(got)
		require.NoError(t, err)
		assert.Equal(t, key, gjson.GetBytes(encoded, "prompt_cache_key").String())
	})
	t.Run("absent", func(t *testing.T) {
		got, err := ChatCompletionsRequestToResponsesRequest(&dto.GeneralOpenAIRequest{
			Model:    "gpt-test",
			Messages: []dto.Message{{Role: "user", Content: "hello"}},
		})
		require.NoError(t, err)

		encoded, err := common.Marshal(got)
		require.NoError(t, err)
		assert.False(t, gjson.GetBytes(encoded, "prompt_cache_key").Exists())
	})
}

func assistantMessageWithTool(content string, id string, name string, args string) dto.Message {
	msg := dto.Message{Role: "assistant", Content: content}
	msg.SetToolCalls([]dto.ToolCallRequest{
		{
			ID:   id,
			Type: "function",
			Function: dto.FunctionRequest{
				Name:      name,
				Arguments: args,
			},
		},
	})
	return msg
}
