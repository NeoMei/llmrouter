package quirk

import (
	"encoding/json"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/relay/toolpolicy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func mustToolCalls(t *testing.T, calls ...dto.ToolCallRequest) json.RawMessage {
	t.Helper()
	raw, err := common.Marshal(calls)
	require.NoError(t, err)
	return raw
}

func mustRepairPolicy(t *testing.T, choice any, names ...string) toolpolicy.Policy {
	t.Helper()
	tools := make([]dto.ToolCallRequest, 0, len(names))
	for _, name := range names {
		tools = append(tools, dto.ToolCallRequest{Type: "function", Function: dto.FunctionRequest{Name: name}})
	}
	policy, err := toolpolicy.BuildFromChat(tools, choice)
	require.NoError(t, err)
	return policy
}

func TestRepairDanglingToolCalls_SynthesizesBeforeUser(t *testing.T) {
	req := &dto.GeneralOpenAIRequest{
		Messages: []dto.Message{
			{Role: "user", Content: "use the tool"},
			{Role: "assistant", ToolCalls: mustToolCalls(t,
				dto.ToolCallRequest{ID: "call_1", Type: "function", Function: dto.FunctionRequest{Name: "calc", Arguments: `{}`}},
			)},
			// 没有 tool result，直接进了下一个 user
			{Role: "user", Content: "what's next?"},
		},
	}

	require.NoError(t, RepairDanglingToolCalls(req, mustRepairPolicy(t, nil, "calc")))

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
				dto.ToolCallRequest{ID: "call_1", Type: "function", Function: dto.FunctionRequest{Name: "calc", Arguments: `{}`}},
			)},
			{Role: "tool", ToolCallId: "call_1", Content: "42"},
		},
	}
	want := make([]dto.Message, len(req.Messages))
	copy(want, req.Messages)

	require.NoError(t, RepairDanglingToolCalls(req, mustRepairPolicy(t, nil, "calc")))

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
				dto.ToolCallRequest{ID: "tail_call", Type: "function", Function: dto.FunctionRequest{Name: "search", Arguments: `{}`}},
			)},
		},
	}

	require.NoError(t, RepairDanglingToolCalls(req, mustRepairPolicy(t, nil, "search")))

	require.Len(t, req.Messages, 3)
	assert.Equal(t, "tool", req.Messages[2].Role)
	assert.Equal(t, "tail_call", req.Messages[2].ToolCallId)
}

func TestRepairDanglingToolCalls_PartiallyAnswered(t *testing.T) {
	// 两个 call，只有 call_2 有 result，应只补 call_1
	req := &dto.GeneralOpenAIRequest{
		Messages: []dto.Message{
			{Role: "assistant", ToolCalls: mustToolCalls(t,
				dto.ToolCallRequest{ID: "call_1", Type: "function", Function: dto.FunctionRequest{Name: "a", Arguments: `{}`}},
				dto.ToolCallRequest{ID: "call_2", Type: "function", Function: dto.FunctionRequest{Name: "b", Arguments: `{}`}},
			)},
			{Role: "tool", ToolCallId: "call_2", Content: "done"},
		},
	}

	require.NoError(t, RepairDanglingToolCalls(req, mustRepairPolicy(t, nil, "a", "b")))

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
				dto.ToolCallRequest{ID: "c1", Type: "function", Function: dto.FunctionRequest{Name: "f1", Arguments: `{}`}},
			)},
			{Role: "user", Content: "q2"},
			{Role: "assistant", ToolCalls: mustToolCalls(t,
				dto.ToolCallRequest{ID: "c2", Type: "function", Function: dto.FunctionRequest{Name: "f2", Arguments: `{}`}},
			)},
			{Role: "tool", ToolCallId: "c2", Content: "r2"},
		},
	}

	require.NoError(t, RepairDanglingToolCalls(req, mustRepairPolicy(t, nil, "f1", "f2")))

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
	require.NoError(t, RepairDanglingToolCalls(req, mustRepairPolicy(t, nil)))
	require.Len(t, req.Messages, 2)
}

func TestRepairDanglingToolCalls_NilMessages_NoPanic(t *testing.T) {
	req := &dto.GeneralOpenAIRequest{}
	require.NoError(t, RepairDanglingToolCalls(req, mustRepairPolicy(t, nil))) // must not panic
	assert.Empty(t, req.Messages)
}

func TestRepairDanglingToolCallsRejectsInvalidHistoryAtomically(t *testing.T) {
	tests := []struct {
		name   string
		calls  json.RawMessage
		policy toolpolicy.Policy
		want   error
	}{
		{
			name: "undeclared name",
			calls: mustToolCalls(t, dto.ToolCallRequest{ID: "call_1", Type: "function",
				Function: dto.FunctionRequest{Name: "delete_all", Arguments: `{}`}}),
			policy: mustRepairPolicy(t, nil, "lookup"), want: toolpolicy.ErrUndeclaredTool,
		},
		{
			name: "missing name",
			calls: mustToolCalls(t, dto.ToolCallRequest{ID: "call_1", Type: "function",
				Function: dto.FunctionRequest{Arguments: `{}`}}),
			policy: mustRepairPolicy(t, nil, "lookup"), want: toolpolicy.ErrMalformedToolCall,
		},
		{
			name: "missing id",
			calls: mustToolCalls(t, dto.ToolCallRequest{Type: "function",
				Function: dto.FunctionRequest{Name: "lookup", Arguments: `{}`}}),
			policy: mustRepairPolicy(t, nil, "lookup"), want: toolpolicy.ErrMalformedToolCall,
		},
		{
			name: "malformed arguments",
			calls: mustToolCalls(t, dto.ToolCallRequest{ID: "call_1", Type: "function",
				Function: dto.FunctionRequest{Name: "lookup", Arguments: `{"q":`}}),
			policy: mustRepairPolicy(t, nil, "lookup"), want: toolpolicy.ErrMalformedArguments,
		},
		{
			name: "wrong argument type",
			calls: mustToolCalls(t, dto.ToolCallRequest{ID: "call_1", Type: "function",
				Function: dto.FunctionRequest{Name: "lookup", Arguments: `[]`}}),
			policy: mustRepairPolicy(t, nil, "lookup"), want: toolpolicy.ErrMalformedArguments,
		},
		{
			name: "wrong tool type",
			calls: mustToolCalls(t, dto.ToolCallRequest{ID: "call_1", Type: "custom",
				Custom: json.RawMessage(`{"name":"lookup","input":{}}`)}),
			policy: mustRepairPolicy(t, nil, "lookup"), want: toolpolicy.ErrToolKindMismatch,
		},
		{
			name: "tool choice none",
			calls: mustToolCalls(t, dto.ToolCallRequest{ID: "call_1", Type: "function",
				Function: dto.FunctionRequest{Name: "lookup", Arguments: `{}`}}),
			policy: mustRepairPolicy(t, "none", "lookup"), want: toolpolicy.ErrDisallowedTool,
		},
		{
			name: "forced choice mismatch",
			calls: mustToolCalls(t, dto.ToolCallRequest{ID: "call_1", Type: "function",
				Function: dto.FunctionRequest{Name: "save", Arguments: `{}`}}),
			policy: mustRepairPolicy(t, map[string]any{"type": "function", "function": map[string]any{"name": "lookup"}}, "lookup", "save"),
			want:   toolpolicy.ErrDisallowedTool,
		},
		{
			name: "duplicate call id",
			calls: mustToolCalls(t,
				dto.ToolCallRequest{ID: "call_1", Type: "function", Function: dto.FunctionRequest{Name: "lookup", Arguments: `{}`}},
				dto.ToolCallRequest{ID: "call_1", Type: "function", Function: dto.FunctionRequest{Name: "lookup", Arguments: `{}`}},
			),
			policy: mustRepairPolicy(t, nil, "lookup"), want: toolpolicy.ErrMalformedToolCall,
		},
		{
			name:   "malformed tool calls container",
			calls:  json.RawMessage(`{"not":"an array"}`),
			policy: mustRepairPolicy(t, nil, "lookup"), want: toolpolicy.ErrMalformedToolCall,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := &dto.GeneralOpenAIRequest{Messages: []dto.Message{
				{Role: "assistant", Content: map[string]any{"huge": json.Number("9007199254740993123456789")}, ToolCalls: tc.calls},
				{Role: "user", Content: "continue"},
			}}
			before, err := common.Marshal(req)
			require.NoError(t, err)

			err = RepairDanglingToolCalls(req, tc.policy)

			require.ErrorIs(t, err, tc.want)
			after, marshalErr := common.Marshal(req)
			require.NoError(t, marshalErr)
			assert.JSONEq(t, string(before), string(after), "an invalid history must remain deeply unchanged")
		})
	}
}

func TestRepairDanglingToolCallsSupportsDeclaredCustomCall(t *testing.T) {
	policy, err := toolpolicy.BuildFromChat([]dto.ToolCallRequest{{
		Type: "custom", Custom: json.RawMessage(`{"name":"shell"}`),
	}}, nil)
	require.NoError(t, err)
	req := &dto.GeneralOpenAIRequest{Messages: []dto.Message{{
		Role: "assistant",
		ToolCalls: mustToolCalls(t, dto.ToolCallRequest{
			ID: "call_custom", Type: "custom",
			Custom: json.RawMessage(`{"name":"shell","input":"pwd"}`),
		}),
	}}}

	err = RepairDanglingToolCalls(req, policy)

	require.NoError(t, err)
	require.Len(t, req.Messages, 2)
	assert.Equal(t, "call_custom", req.Messages[1].ToolCallId)
	assert.Contains(t, req.Messages[1].StringContent(), `"shell"`)
}
