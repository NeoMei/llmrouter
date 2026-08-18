package quirk

import (
	"fmt"
	"strings"

	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/internal/toolcallid"
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
	index *int
	id    string
	name  string
	args  string
}

// ToolCallBuffer 累积流式 tool call delta，在 Flush 时原子输出。
type ToolCallBuffer struct {
	pending []*bufferedEntry
	byIndex map[int]*bufferedEntry
	byID    map[string]*bufferedEntry
	ids     *toolcallid.Normalizer
	err     error
}

func NewToolCallBuffer() *ToolCallBuffer {
	return &ToolCallBuffer{
		byIndex: make(map[int]*bufferedEntry),
		byID:    make(map[string]*bufferedEntry),
		ids:     toolcallid.New("call_gw"),
	}
}

// Has 返回是否有未 flush 的 tool call。
func (b *ToolCallBuffer) Has() bool {
	return len(b.pending) > 0 || b.err != nil
}

// Accumulate 处理一个 delta chunk 中的 tool_calls，按 index/id 累积。
// Index 和 ID 是对等 alias；两者同时出现时建立桥接，冲突或无法
// 判定归属的无标识分片会在 Flush 时 fail closed。
func (b *ToolCallBuffer) Accumulate(toolCalls []dto.ToolCallResponse) {
	if b.err != nil {
		return
	}
	if len(toolCalls) > 1 {
		if len(b.pending) == 1 && b.pending[0].index == nil && b.pending[0].id == "" {
			b.err = fmt.Errorf("ambiguous tool call delta: an unidentified pending call cannot be matched to a multi-call chunk")
			return
		}
		for i := range toolCalls {
			if toolCalls[i].Index == nil && toolCalls[i].ID == "" {
				b.err = fmt.Errorf("ambiguous tool call delta: call %d in a multi-call chunk has neither index nor id", i)
				return
			}
		}
	}
	for i := range toolCalls {
		tc := &toolCalls[i]
		entry, err := b.resolve(tc.Index, tc.ID)
		if err != nil {
			b.err = err
			return
		}
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

// Flush validates and returns complete tool calls, then clears the buffer.
// A failed flush leaves pending calls untouched so callers cannot lose the
// malformed terminal state while handling the error.
func (b *ToolCallBuffer) Flush() ([]BufferedToolCall, error) {
	if b.err != nil {
		return nil, b.err
	}
	for i := range b.pending {
		e := b.pending[i]
		if strings.TrimSpace(e.name) == "" {
			return nil, fmt.Errorf("tool call %d is missing function name", i)
		}
		if _, err := toolcallid.ParseObjectArguments(e.args); err != nil {
			return nil, fmt.Errorf("tool call %d has invalid arguments: %w", i, err)
		}
	}

	result := make([]BufferedToolCall, len(b.pending))
	for i := range b.pending {
		e := b.pending[i]
		normalizedID, err := b.ids.RegisterCall(e.id)
		if err != nil {
			return nil, err
		}
		result[i] = BufferedToolCall{
			ID:   normalizedID,
			Name: strings.TrimSpace(e.name),
			Args: e.args,
		}
	}
	b.pending = nil
	b.byIndex = make(map[int]*bufferedEntry)
	b.byID = make(map[string]*bufferedEntry)
	return result, nil
}

func (b *ToolCallBuffer) resolve(index *int, id string) (*bufferedEntry, error) {
	var indexed, identified *bufferedEntry
	if index != nil {
		indexed = b.byIndex[*index]
	}
	if id != "" {
		identified = b.byID[id]
	}
	if indexed != nil && identified != nil && indexed != identified {
		return nil, fmt.Errorf("conflicting tool call aliases: index %d and id %q identify different calls", *index, id)
	}

	entry := indexed
	if entry == nil {
		entry = identified
	}
	if entry == nil {
		if index == nil && id == "" {
			switch len(b.pending) {
			case 0:
				entry = b.newEntry()
			case 1:
				entry = b.pending[0]
			default:
				return nil, fmt.Errorf("ambiguous tool call delta: missing both index and id with %d pending calls", len(b.pending))
			}
		} else if len(b.pending) == 1 && b.pending[0].index == nil && b.pending[0].id == "" {
			entry = b.pending[0]
		} else {
			entry = b.newEntry()
		}
	}

	if index != nil {
		if entry.index != nil && *entry.index != *index {
			return nil, fmt.Errorf("conflicting tool call aliases: id %q changed index from %d to %d", id, *entry.index, *index)
		}
		if existing := b.byIndex[*index]; existing != nil && existing != entry {
			return nil, fmt.Errorf("conflicting tool call aliases: index %d is already assigned", *index)
		}
		indexCopy := *index
		entry.index = &indexCopy
		b.byIndex[*index] = entry
	}
	if id != "" {
		if entry.id != "" && entry.id != id {
			return nil, fmt.Errorf("conflicting tool call aliases: index identifies id %q but received %q", entry.id, id)
		}
		if existing := b.byID[id]; existing != nil && existing != entry {
			return nil, fmt.Errorf("conflicting tool call aliases: id %q is already assigned", id)
		}
		entry.id = id
		b.byID[id] = entry
	}
	return entry, nil
}

func (b *ToolCallBuffer) newEntry() *bufferedEntry {
	entry := &bufferedEntry{}
	b.pending = append(b.pending, entry)
	return entry
}
