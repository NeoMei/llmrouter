package dto

import (
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/pkg/metering"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestProductDeepSeekChatMetering(t *testing.T) {
	const source = "deepseek_chat_product_v1"
	const raw = `{"usage":{"prompt_tokens":22,"completion_tokens":17,"total_tokens":39,"prompt_cache_hit_tokens":13,"prompt_cache_miss_tokens":9,"prompt_tokens_details":{"cached_tokens":13},"completion_tokens_details":{"reasoning_tokens":5}}}`
	price := nativeTokenTestPrice("chat", "input", "output", "cache_read", "reasoning")
	for _, tt := range []struct {
		name  string
		raw   string
		state string
		want  []string
	}{
		{"counts each dimension once", raw, "confirmed", []string{"9", "12", "13", "5"}},
		{"cache-hit alias omitted", strings.Replace(raw, `,"prompt_tokens_details":{"cached_tokens":13}`, "", 1), "confirmed", []string{"9", "12", "13", "5"}},
		{"reasoning omitted", strings.Replace(raw, `,"completion_tokens_details":{"reasoning_tokens":5}`, "", 1), "confirmed", []string{"9", "17", "13", "0"}},
		{"cache-hit alias disagrees", strings.Replace(raw, `"cached_tokens":13`, `"cached_tokens":12`, 1), "invalid", nil},
		{"miss disagrees", strings.Replace(raw, `"prompt_cache_miss_tokens":9`, `"prompt_cache_miss_tokens":8`, 1), "invalid", nil},
		{"hit missing", strings.Replace(raw, `"prompt_cache_hit_tokens":13,`, "", 1), "incomplete", nil},
		{"miss missing", strings.Replace(raw, `"prompt_cache_miss_tokens":9,`, "", 1), "incomplete", nil},
		{"total missing", strings.Replace(raw, `"total_tokens":39,`, "", 1), "incomplete", nil},
		{"total disagrees", strings.Replace(raw, `"total_tokens":39`, `"total_tokens":40`, 1), "invalid", nil},
		{"duplicate cache hit", strings.Replace(raw, `"prompt_cache_hit_tokens":13`, `"prompt_cache_hit_tokens":13,"prompt_cache_hit_tokens":14`, 1), "invalid", nil},
		{"hosted tool usage", strings.Replace(raw, `"total_tokens":39`, `"total_tokens":39,"server_tool_use":{"tool_search":1}`, 1), "invalid", nil},
		{"positive cache write", strings.Replace(raw, `"cached_tokens":13`, `"cached_tokens":13,"cache_creation_input_tokens":1`, 1), "invalid", nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			evidence := NativeTokenEvidence{Source: source}
			evidence.Observe([]byte(tt.raw), false)
			usage := evidence.Usage(price)
			require.Equal(t, tt.state, usage.State)
			if tt.want == nil {
				return
			}
			require.NoError(t, metering.ValidateUsage(price, usage))
			require.Len(t, usage.Measurements, len(tt.want))
			for i, quantity := range tt.want {
				assert.Equal(t, quantity, usage.Measurements[i].Quantity.Value, usage.Measurements[i].Key)
			}
		})
	}
}

func TestProductDeepSeekStreamingNeedsTerminalUsageAndDone(t *testing.T) {
	const source = "deepseek_chat_product_v1"
	const usage = `"usage":{"prompt_tokens":7,"completion_tokens":11,"total_tokens":18,"prompt_cache_hit_tokens":0,"prompt_cache_miss_tokens":7}`
	const final = `{"choices":[{"index":0,"delta":{"content":null},"finish_reason":"stop"}],` + usage + `}`
	const prefix = `{"choices":[{"index":0,"delta":{"content":"hi"},"finish_reason":null}]}`
	price := nativeTokenTestPrice("chat", "input", "output", "cache_read")
	for _, tt := range []struct {
		name   string
		frames []string
		done   bool
		want   string
	}{
		{"complete", []string{prefix, final}, true, "confirmed"},
		{"truncation", []string{prefix, final}, false, "incomplete"},
		{"usage before final reason", []string{`{"choices":[{"delta":{},"finish_reason":null}],` + usage + `}`}, true, "invalid"},
		{"data after terminal usage", []string{prefix, final, prefix}, true, "invalid"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			evidence := NativeTokenEvidence{Source: source}
			for _, frame := range tt.frames {
				evidence.Observe([]byte(frame), true)
			}
			evidence.CompleteStream(tt.done)
			assert.Equal(t, tt.want, evidence.Usage(price).State)
		})
	}
}
