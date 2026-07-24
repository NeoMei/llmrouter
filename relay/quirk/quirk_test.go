package quirk

import (
	"encoding/json"
	"testing"

	"github.com/QuantumNous/new-api/dto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNeutralizeIdentity_RemovesGPT5Line(t *testing.T) {
	req := &dto.GeneralOpenAIRequest{
		Messages: []dto.Message{
			{Role: "system", Content: "You are GPT-5, a helpful assistant.\nDo great work."},
		},
	}

	NeutralizeIdentity(req)

	content := req.Messages[0].StringContent()
	assert.NotContains(t, content, "GPT-5")
	assert.Contains(t, content, "Do great work")
}

func TestNeutralizeIdentity_Table(t *testing.T) {
	tests := []struct {
		name    string
		role    string
		input   string
		want    string
		changed bool
	}{
		{"gpt-5 system", "system", "You are GPT-5.\nKeep me.", "Keep me.", true},
		{"chatgpt system", "system", "You are ChatGPT.\nKeep me.", "Keep me.", true},
		{"openai assistant line", "system", "You are an AI assistant from OpenAI.\nKeep me.", "Keep me.", true},
		{"no identity", "system", "You are a coder.\nKeep me.", "You are a coder.\nKeep me.", false},
		{"developer role", "developer", "You are GPT-5.\nKeep me.", "Keep me.", true},
		{"user role untouched", "user", "You are GPT-5.", "You are GPT-5.", false},
		{"assistant role untouched", "assistant", "You are GPT-5.", "You are GPT-5.", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := &dto.GeneralOpenAIRequest{
				Messages: []dto.Message{{Role: tt.role, Content: tt.input}},
			}
			NeutralizeIdentity(req)
			assert.Equal(t, tt.want, req.Messages[0].StringContent())
		})
	}
}

func TestNeutralizeIdentity_SkipsStructuredContent(t *testing.T) {
	// 含图片的结构化 content 不应被展平成纯文本
	req := &dto.GeneralOpenAIRequest{
		Messages: []dto.Message{
			{Role: "system", Content: []any{
				map[string]any{"type": "text", "text": "You are GPT-5."},
				map[string]any{"type": "image_url", "image_url": map[string]any{"url": "http://x/a.png"}},
			}},
		},
	}

	NeutralizeIdentity(req)

	arr, ok := req.Messages[0].Content.([]any)
	require.True(t, ok, "structured content must be preserved, not flattened")
	assert.Len(t, arr, 2)
}

func TestNeutralizeIdentity_NilRequestMessages(t *testing.T) {
	req := &dto.GeneralOpenAIRequest{}
	NeutralizeIdentity(req) // must not panic
	assert.Empty(t, req.Messages)
}

func TestGuardEmptyAssistant_AddsEmptyContent(t *testing.T) {
	req := &dto.GeneralOpenAIRequest{
		Messages: []dto.Message{
			{Role: "assistant", Content: ""},
		},
	}

	GuardEmptyAssistant(req)

	// content 必须是显式空字符串 (非 nil)，序列化为 "content": ""
	assert.Equal(t, "", req.Messages[0].StringContent())
	assert.NotNil(t, req.Messages[0].Content)
}

func TestGuardEmptyAssistant_LeavesPopulatedAlone(t *testing.T) {
	toolCalls := json.RawMessage(`[{"id":"1","type":"function"}]`)
	req := &dto.GeneralOpenAIRequest{
		Messages: []dto.Message{
			{Role: "assistant", Content: "hello"},
			{Role: "assistant", Content: "", ToolCalls: toolCalls},
			{Role: "user", Content: ""},
		},
	}

	GuardEmptyAssistant(req)

	// assistant with content unchanged
	assert.Equal(t, "hello", req.Messages[0].StringContent())
	// assistant with tool_calls present -> not modified
	assert.Equal(t, "", req.Messages[1].StringContent())
	// user role untouched even if empty
	assert.Equal(t, "", req.Messages[2].StringContent())
}

func TestGuardEmptyAssistant_HasReasoningContent(t *testing.T) {
	reasoning := "thinking..."
	req := &dto.GeneralOpenAIRequest{
		Messages: []dto.Message{
			{Role: "assistant", Content: "", ReasoningContent: &reasoning},
		},
	}

	GuardEmptyAssistant(req)

	// reasoning_content present -> not an empty message, must not inject content
	assert.Equal(t, "", req.Messages[0].StringContent())
}
