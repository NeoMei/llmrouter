package relay

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/dto"
	"github.com/stretchr/testify/require"
)

func compactTestInput(t *testing.T, v any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(v)
	require.NoError(t, err)
	return raw
}

func TestBuildCompactViaChatRequestFiltersAndAppendsPrompt(t *testing.T) {
	req := &dto.OpenAIResponsesRequest{
		Model: "glm-5.2",
		Input: compactTestInput(t, []any{
			map[string]any{"type": "message", "role": "user", "content": []any{
				map[string]any{"type": "input_text", "text": "fix the login bug"},
				map[string]any{"type": "input_image", "image_url": "https://x.test/a.png"},
			}},
			map[string]any{"type": "compaction_trigger"},
			map[string]any{"type": "additional_tools"},
		}),
		Tools: json.RawMessage(`[{"type":"function","name":"shell"}]`),
	}

	chatReq, err := buildCompactViaChatRequest(req)
	require.NoError(t, err)
	require.Equal(t, "glm-5.2", chatReq.Model)
	require.False(t, *chatReq.Stream)
	require.Empty(t, chatReq.Tools, "compaction must drop the tool surface")
	require.Len(t, chatReq.Messages, 2)

	// first message: original user text with image replaced by placeholder
	first := chatReq.Messages[0]
	require.Equal(t, "user", first.Role)
	content := first.StringContent()
	require.Contains(t, content, "fix the login bug")
	require.Contains(t, content, compactImagePlaceholder)

	// second message: the summarization instruction
	second := chatReq.Messages[1]
	require.Equal(t, "user", second.Role)
	require.Contains(t, second.StringContent(), "CONTEXT CHECKPOINT COMPACTION")
}

func TestBuildCompactViaChatRequestStringInput(t *testing.T) {
	req := &dto.OpenAIResponsesRequest{
		Model: "kimi-k2.7-code",
		Input: json.RawMessage(`"just a string conversation"`),
	}
	chatReq, err := buildCompactViaChatRequest(req)
	require.NoError(t, err)
	require.Len(t, chatReq.Messages, 2)
	require.Contains(t, chatReq.Messages[0].StringContent(), "just a string conversation")
}

func TestBuildCompactViaChatRequestRejectsEmptyInput(t *testing.T) {
	_, err := buildCompactViaChatRequest(&dto.OpenAIResponsesRequest{Model: "m"})
	require.Error(t, err)
}

func TestExtractCompactUserMessages(t *testing.T) {
	input := compactTestInput(t, []any{
		map[string]any{"type": "message", "role": "user", "content": "hello world"},
		map[string]any{"type": "message", "role": "assistant", "content": "hi"},
		map[string]any{"role": "user", "content": []any{
			map[string]any{"type": "input_text", "text": "part one "},
			map[string]any{"type": "input_text", "text": "part two"},
		}},
		map[string]any{"type": "function_call", "call_id": "x"},
		map[string]any{"type": "message", "role": "user", "content": ""},
	})
	msgs := extractCompactUserMessages(input)
	require.Equal(t, []string{"hello world", "part one part two"}, msgs)
}

func TestBuildCompactV1OutputBudgetAndOrder(t *testing.T) {
	// two recent small messages + one huge older message exceeding the budget tail
	small1 := strings.Repeat("a", 100)
	small2 := strings.Repeat("b", 100)
	huge := strings.Repeat("c", compactV1RetainedCharBudget)
	out := buildCompactV1Output([]string{huge, small1, small2}, "the summary")

	require.GreaterOrEqual(t, len(out), 3)
	// chronological: partial huge tail, small1, small2, then the summary item
	getText := func(item map[string]any) string {
		content := item["content"].([]any)
		part := content[0].(map[string]any)
		return part["text"].(string)
	}
	require.Equal(t, small1, getText(out[1]))
	require.Equal(t, small2, getText(out[2]))
	summaryItem := getText(out[len(out)-1])
	require.True(t, strings.HasPrefix(summaryItem, compactSummaryPrefix))
	require.Contains(t, summaryItem, "the summary")
	require.Equal(t, "user", out[len(out)-1]["role"])
}

func TestBuildCompactV1OutputEmptySummary(t *testing.T) {
	out := buildCompactV1Output([]string{"q"}, "")
	content := out[1]["content"].([]any)
	text := content[0].(map[string]any)["text"].(string)
	require.Equal(t, "(no summary available)", text)
}
