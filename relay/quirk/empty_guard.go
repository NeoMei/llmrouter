package quirk

import "github.com/QuantumNous/new-api/dto"

// ponytail: 移植自 opencodex openai-chat.ts
// DeepSeek 等严格 provider 拒绝没有 content/tool_calls/reasoning_content 的 assistant 消息，
// 这里给完全空的 assistant 消息补一个显式的空字符串 content。
func GuardEmptyAssistant(req *dto.GeneralOpenAIRequest) {
	for i := range req.Messages {
		msg := &req.Messages[i]
		if msg.Role != "assistant" {
			continue
		}
		hasContent := msg.StringContent() != ""
		hasTools := len(msg.ToolCalls) > 0
		hasReasoning := msg.GetReasoningContent() != ""
		if !hasContent && !hasTools && !hasReasoning {
			req.Messages[i].SetStringContent("")
		}
	}
}
