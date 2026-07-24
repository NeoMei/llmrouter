package quirk

import (
	"fmt"

	"github.com/QuantumNous/new-api/dto"
)

// ponytail: 移植自 opencodex openai-chat.ts:parseStream
// 流式 tool call 分片缓存：function name 和 arguments 分片到达，
// 按 index/id 累积，finish_reason/[DONE] 后原子输出。
//
// relayconvert 管线已内置等价的累积逻辑
// (to_oai_responses_stream_resp.go:appendToolCallDelta)，此 buffer
// 供 relayconvert 管线之外、需要独立累积 tool call 的路径使用。

// BufferedToolCall 是 Flush 返回的一个完整 tool call。
type BufferedToolCall struct {
	ID   string
	Name string
	Args string
}

type bufferedEntry struct {
	key  string // 匹配键：i:<index> 或 id:<id>
	id   string
	name string
	args string
}

// ToolCallBuffer 累积流式 tool call delta，在 Flush 时原子输出。
type ToolCallBuffer struct {
	pending []bufferedEntry
	seq     int
}

func NewToolCallBuffer() *ToolCallBuffer {
	return &ToolCallBuffer{}
}

// Has 返回是否有未 flush 的 tool call。
func (b *ToolCallBuffer) Has() bool {
	return len(b.pending) > 0
}

// Accumulate 处理一个 delta chunk 中的 tool_calls，按 index/id 累积。
// Index 是跨分片的稳定键（所有分片共享同一 Index），ID 仅作二级匹配。
func (b *ToolCallBuffer) Accumulate(toolCalls []dto.ToolCallResponse) {
	for i := range toolCalls {
		tc := &toolCalls[i]
		key := fmt.Sprintf("i:%d", indexOrZero(tc.Index))
		entry := b.findOrCreate(key, tc.ID)
		if tc.ID != "" && entry.id == "" {
			entry.id = tc.ID
		}
		if tc.Function.Name != "" && entry.name == "" {
			entry.name = tc.Function.Name
		}
		if tc.Function.Arguments != "" {
			entry.args += tc.Function.Arguments
		}
	}
}

// Flush 返回累积的 tool calls（缺失 ID 自动生成），然后清空 buffer。
func (b *ToolCallBuffer) Flush() []BufferedToolCall {
	result := make([]BufferedToolCall, len(b.pending))
	for i := range b.pending {
		e := &b.pending[i]
		id := e.id
		if id == "" {
			b.seq++
			id = fmt.Sprintf("call_gw_%d", b.seq)
		}
		result[i] = BufferedToolCall{ID: id, Name: e.name, Args: e.args}
	}
	b.pending = nil
	return result
}

func (b *ToolCallBuffer) findOrCreate(key, id string) *bufferedEntry {
	for i := range b.pending {
		if b.pending[i].key == key {
			return &b.pending[i]
		}
		if id != "" && b.pending[i].id == id {
			return &b.pending[i]
		}
	}
	b.pending = append(b.pending, bufferedEntry{key: key})
	return &b.pending[len(b.pending)-1]
}

func indexOrZero(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}
