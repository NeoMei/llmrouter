package quirk

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/relay/toolpolicy"
)

// ponytail: 移植自 opencodex openai-chat.ts:messagesToChatFormat
// 修复消息历史中 assistant 的 tool_call 没有对应 tool result 的情况。
// 严格 provider（DeepSeek/Kimi）会对此返回 400，这里合成占位 tool result。

type pendingToolCall struct {
	ID   string
	Name string
}

// RepairDanglingToolCalls validates every historical assistant call against the
// effective outbound policy, then fills only genuinely missing results. The
// request is assigned only after the complete scan succeeds.
func RepairDanglingToolCalls(req *dto.GeneralOpenAIRequest, policy toolpolicy.Policy) error {
	if req == nil {
		return fmt.Errorf("%w: request is nil", toolpolicy.ErrMalformedToolCall)
	}
	var pending []pendingToolCall
	repaired := make([]dto.Message, 0, len(req.Messages))
	seenIDs := make(map[string]struct{})

	for _, msg := range req.Messages {
		switch msg.Role {
		case "assistant":
			// 先 flush 上一轮未应答的 calls，再追加本条 assistant
			if len(pending) > 0 {
				repaired = append(repaired, synthesizeToolResults(pending)...)
				pending = nil
			}
			repaired = append(repaired, msg)
			calls, err := validatedToolCalls(msg.ToolCalls, policy, seenIDs)
			if err != nil {
				return err
			}
			// 收集本条 assistant 的 tool_calls
			pending = append(pending, calls...)

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
	return nil
}

func validatedToolCalls(raw []byte, policy toolpolicy.Policy, seenIDs map[string]struct{}) ([]pendingToolCall, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	if common.GetJsonType(raw) != "array" {
		return nil, fmt.Errorf("%w: tool_calls must be an array", toolpolicy.ErrMalformedToolCall)
	}
	var calls []dto.ToolCallRequest
	if err := common.UnmarshalUnique(raw, &calls); err != nil {
		return nil, fmt.Errorf("%w: invalid tool_calls: %v", toolpolicy.ErrMalformedToolCall, err)
	}
	validated := make([]pendingToolCall, 0, len(calls))
	for _, call := range calls {
		kind := toolpolicy.ToolKind(strings.TrimSpace(call.Type))
		if kind == "" {
			kind = toolpolicy.ToolKindFunction
		}
		name := call.Function.Name
		arguments := []byte(call.Function.Arguments)
		if kind == toolpolicy.ToolKindCustom {
			var custom struct {
				Name  string          `json:"name"`
				Input json.RawMessage `json:"input"`
			}
			if err := common.UnmarshalUnique(call.Custom, &custom); err != nil {
				return nil, fmt.Errorf("%w: invalid custom tool call: %v", toolpolicy.ErrMalformedToolCall, err)
			}
			name = custom.Name
			arguments = custom.Input
		}
		if err := policy.ValidateCall(toolpolicy.Call{
			ID:        call.ID,
			Name:      name,
			Kind:      kind,
			Arguments: arguments,
		}); err != nil {
			return nil, err
		}
		id := strings.TrimSpace(call.ID)
		if _, duplicate := seenIDs[id]; duplicate {
			return nil, fmt.Errorf("%w: duplicate tool call id %q", toolpolicy.ErrMalformedToolCall, id)
		}
		seenIDs[id] = struct{}{}
		validated = append(validated, pendingToolCall{ID: call.ID, Name: name})
	}
	return validated, nil
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
