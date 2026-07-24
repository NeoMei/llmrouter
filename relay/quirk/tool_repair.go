package quirk

import (
	"fmt"

	"github.com/QuantumNous/new-api/dto"
)

// ponytail: 移植自 opencodex openai-chat.ts:messagesToChatFormat
// 修复消息历史中 assistant 的 tool_call 没有对应 tool result 的情况。
// 严格 provider（DeepSeek/Kimi）会对此返回 400，这里合成占位 tool result。

type pendingToolCall struct {
	ID   string
	Name string
}

// RepairDanglingToolCalls 遍历消息序列，为未应答的 tool_call 合成 tool result。
func RepairDanglingToolCalls(req *dto.GeneralOpenAIRequest) {
	var pending []pendingToolCall
	var repaired []dto.Message

	for _, msg := range req.Messages {
		switch msg.Role {
		case "assistant":
			// 先 flush 上一轮未应答的 calls，再追加本条 assistant
			if len(pending) > 0 {
				repaired = append(repaired, synthesizeToolResults(pending)...)
				pending = nil
			}
			repaired = append(repaired, msg)
			// 收集本条 assistant 的 tool_calls
			for _, tc := range msg.ParseToolCalls() {
				pending = append(pending, pendingToolCall{
					ID:   tc.ID,
					Name: tc.Function.Name,
				})
			}

		case "tool":
			// 匹配并从 pending 中移除已应答的 call
			for i, p := range pending {
				if p.ID == msg.ToolCallId {
					pending = append(pending[:i], pending[i+1:]...)
					break
				}
			}
			repaired = append(repaired, msg)

		default:
			// user/system：先 flush pending，避免 tool result 跨越 user 边界
			if len(pending) > 0 {
				repaired = append(repaired, synthesizeToolResults(pending)...)
				pending = nil
			}
			repaired = append(repaired, msg)
		}
	}

	// 尾部 flush：assistant 以 tool_call 结尾且无后续 result
	if len(pending) > 0 {
		repaired = append(repaired, synthesizeToolResults(pending)...)
	}

	req.Messages = repaired
}

func synthesizeToolResults(pending []pendingToolCall) []dto.Message {
	var results []dto.Message
	for _, p := range pending {
		results = append(results, dto.Message{
			Role:       "tool",
			ToolCallId: p.ID,
			Content:    fmt.Sprintf(`[gateway] no tool result was recorded for "%s"; execution status unknown.`, p.Name),
		})
	}
	return results
}
