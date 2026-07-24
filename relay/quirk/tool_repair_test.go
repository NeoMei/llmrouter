package quirk

import (
	"encoding/json"
	"testing"

	"github.com/QuantumNous/new-api/dto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func mustToolCalls(t *testing.T, calls ...dto.ToolCallRequest) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(calls)
	require.NoError(t, err)
	return raw
}

func TestRepairDanglingToolCalls_SynthesizesBeforeUser(t *testing.T) {
	req := &dto.GeneralOpenAIRequest{
		Messages: []dto.Message{
			{Role: "user", Content: "use the tool"},
			{Role: "assistant", ToolCalls: mustToolCalls(t,
				dto.ToolCallRequest{ID: "call_1", Function: dto.FunctionRequest{Name: "calc"}},
			)},
			// 没有 tool result，直接进了下一个 user
			{Role: "user", Content: "what's next?"},
		},
	}

	RepairDanglingToolCalls(req)

	// assistant(0:idx1) 之后、第二个 user 之前应插入一条 tool result
	var roles []string
	for _, m := range req.Messages {
		roles = append(roles, m.Role)
	}
	// 期望顺序: user, assistant, tool, user
	assert.Equal(t, []string{"user", "assistant", "tool", "user"}, roles)

	tool := req.Messages[2]
	assert.Equal(t, "call_1", tool.ToolCallId)
	assert.Contains(t, tool.StringContent(), `"calc"`)
	assert.Contains(t, tool.StringContent(), "[gateway]")
}

func TestRepairDanglingToolCalls_NoOpWhenComplete(t *testing.T) {
	req := &dto.GeneralOpenAIRequest{
		Messages: []dto.Message{
			{Role: "assistant", ToolCalls: mustToolCalls(t,
				dto.ToolCallRequest{ID: "call_1", Function: dto.FunctionRequest{Name: "calc"}},
			)},
			{Role: "tool", ToolCallId: "call_1", Content: "42"},
		},
	}
	want := make([]dto.Message, len(req.Messages))
	copy(want, req.Messages)

	RepairDanglingToolCalls(req)

	require.Len(t, req.Messages, 2)
	assert.Equal(t, "tool", req.Messages[1].Role)
	assert.Equal(t, "42", req.Messages[1].StringContent())
	// 已应答的 call 不应被再次合成
	for _, m := range req.Messages {
		if m.Role == "tool" {
			assert.NotContains(t, m.StringContent(), "[gateway]")
		}
	}
}

func TestRepairDanglingToolCalls_SynthesizesAtTail(t *testing.T) {
	// 最后一条是带 tool_call 的 assistant，无后续 result
	req := &dto.GeneralOpenAIRequest{
		Messages: []dto.Message{
			{Role: "user", Content: "go"},
			{Role: "assistant", ToolCalls: mustToolCalls(t,
				dto.ToolCallRequest{ID: "tail_call", Function: dto.FunctionRequest{Name: "search"}},
			)},
		},
	}

	RepairDanglingToolCalls(req)

	require.Len(t, req.Messages, 3)
	assert.Equal(t, "tool", req.Messages[2].Role)
	assert.Equal(t, "tail_call", req.Messages[2].ToolCallId)
}

func TestRepairDanglingToolCalls_PartiallyAnswered(t *testing.T) {
	// 两个 call，只有 call_2 有 result，应只补 call_1
	req := &dto.GeneralOpenAIRequest{
		Messages: []dto.Message{
			{Role: "assistant", ToolCalls: mustToolCalls(t,
				dto.ToolCallRequest{ID: "call_1", Function: dto.FunctionRequest{Name: "a"}},
				dto.ToolCallRequest{ID: "call_2", Function: dto.FunctionRequest{Name: "b"}},
			)},
			{Role: "tool", ToolCallId: "call_2", Content: "done"},
		},
	}

	RepairDanglingToolCalls(req)

	// 期望: assistant, tool(call_2 已有), tool(call_1 合成)
	require.Len(t, req.Messages, 3)
	assert.Equal(t, "call_2", req.Messages[1].ToolCallId)
	assert.Equal(t, "call_1", req.Messages[2].ToolCallId)
	assert.Contains(t, req.Messages[2].StringContent(), "[gateway]")
}

func TestRepairDanglingToolCalls_PreservesOrderAcrossTurns(t *testing.T) {
	// 第一轮 dangling，第二轮完整：第一轮在 user 前补，第二轮不动
	req := &dto.GeneralOpenAIRequest{
		Messages: []dto.Message{
			{Role: "user", Content: "q1"},
			{Role: "assistant", ToolCalls: mustToolCalls(t,
				dto.ToolCallRequest{ID: "c1", Function: dto.FunctionRequest{Name: "f1"}},
			)},
			{Role: "user", Content: "q2"},
			{Role: "assistant", ToolCalls: mustToolCalls(t,
				dto.ToolCallRequest{ID: "c2", Function: dto.FunctionRequest{Name: "f2"}},
			)},
			{Role: "tool", ToolCallId: "c2", Content: "r2"},
		},
	}

	RepairDanglingToolCalls(req)

	var roles []string
	for _, m := range req.Messages {
		roles = append(roles, m.Role)
	}
	assert.Equal(t,
		[]string{"user", "assistant", "tool", "user", "assistant", "tool"},
		roles,
	)
	// 合成的 tool（idx 2）对应 c1
	assert.Equal(t, "c1", req.Messages[2].ToolCallId)
	assert.Contains(t, req.Messages[2].StringContent(), "[gateway]")
	// 已有的 tool（idx 5）对应 c2，未被改写
	assert.Equal(t, "r2", req.Messages[5].StringContent())
}

func TestRepairDanglingToolCalls_NoToolCalls_NoOp(t *testing.T) {
	req := &dto.GeneralOpenAIRequest{
		Messages: []dto.Message{
			{Role: "user", Content: "hi"},
			{Role: "assistant", Content: "hello"},
		},
	}
	RepairDanglingToolCalls(req)
	require.Len(t, req.Messages, 2)
}

func TestRepairDanglingToolCalls_NilMessages_NoPanic(t *testing.T) {
	req := &dto.GeneralOpenAIRequest{}
	RepairDanglingToolCalls(req) // must not panic
	assert.Empty(t, req.Messages)
}
