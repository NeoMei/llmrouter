package quirk

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
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
	ids     *bufferIDNormalizer
}

func NewToolCallBuffer() *ToolCallBuffer {
	return &ToolCallBuffer{ids: newBufferIDNormalizer("call_gw")}
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

// Flush validates and returns complete tool calls, then clears the buffer.
// A failed flush leaves pending calls untouched so callers cannot lose the
// malformed terminal state while handling the error.
func (b *ToolCallBuffer) Flush() ([]BufferedToolCall, error) {
	for i := range b.pending {
		e := &b.pending[i]
		if strings.TrimSpace(e.name) == "" {
			return nil, fmt.Errorf("tool call %d is missing function name", i)
		}
		if strings.TrimSpace(e.args) == "" || common.GetJsonType([]byte(e.args)) != "object" {
			return nil, fmt.Errorf("tool call %d arguments must be a JSON object", i)
		}
		var object map[string]any
		if err := common.UnmarshalUnique([]byte(e.args), &object); err != nil || object == nil {
			if err == nil {
				err = fmt.Errorf("arguments must be a JSON object")
			}
			return nil, fmt.Errorf("tool call %d has invalid arguments: %w", i, err)
		}
	}

	result := make([]BufferedToolCall, len(b.pending))
	for i := range b.pending {
		e := &b.pending[i]
		result[i] = BufferedToolCall{
			ID:   b.ids.normalize(e.id),
			Name: strings.TrimSpace(e.name),
			Args: e.args,
		}
	}
	b.pending = nil
	return result, nil
}

type bufferIDNormalizer struct {
	prefix  string
	forward map[string]string
	reverse map[string]string
	seq     uint64
}

func newBufferIDNormalizer(prefix string) *bufferIDNormalizer {
	return &bufferIDNormalizer{
		prefix:  prefix,
		forward: make(map[string]string),
		reverse: make(map[string]string),
	}
}

func (n *bufferIDNormalizer) normalize(source string) string {
	if source != "" {
		if normalized, ok := n.forward[source]; ok {
			return normalized
		}
	}
	seed := source
	if seed == "" {
		n.seq++
		seed = "empty:" + strconv.FormatUint(n.seq, 10)
	}
	candidate := source
	if !validBufferedToolCallID(candidate) {
		sum := sha256.Sum256([]byte(seed))
		candidate = n.prefix + "_" + hex.EncodeToString(sum[:])[:24]
	}
	base := candidate
	for collision := 2; ; collision++ {
		owner, exists := n.reverse[candidate]
		if !exists || owner == source {
			break
		}
		suffix := "_" + strconv.Itoa(collision)
		trimmed := base
		if len(trimmed)+len(suffix) > 64 {
			trimmed = trimmed[:64-len(suffix)]
		}
		candidate = trimmed + suffix
	}
	if source != "" {
		n.forward[source] = candidate
	}
	n.reverse[candidate] = source
	return candidate
}

func validBufferedToolCallID(id string) bool {
	if id == "" || len(id) > 64 {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_' || c == '-' {
			continue
		}
		return false
	}
	return true
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
