package dto

import (
	"github.com/QuantumNous/new-api/pkg/metering"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestNativeActualTokenEvidence(t *testing.T) {
	for _, tc := range []struct {
		name, source, raw, state string
		input, output            string
	}{
		{"actual counters", BillingUsageSourceOAIChat, `{"usage":{"prompt_tokens":13,"completion_tokens":7,"total_tokens":20}}`, "confirmed", "13", "7"},
		{"explicit zero", BillingUsageSourceOAIChat, `{"usage":{"prompt_tokens":0,"completion_tokens":0,"total_tokens":0}}`, "confirmed", "0", "0"},
		{"missing", BillingUsageSourceOAIChat, `{}`, "missing", "", ""},
		{"null", BillingUsageSourceOAIChat, `{"usage":null}`, "missing", "", ""},
		{"negative", BillingUsageSourceOAIChat, `{"usage":{"prompt_tokens":-1,"completion_tokens":0}}`, "invalid", "", ""},
		{"overflow", BillingUsageSourceOAIChat, `{"usage":{"prompt_tokens":9223372036854775808,"completion_tokens":0}}`, "invalid", "", ""},
		{"duplicate", BillingUsageSourceOAIChat, `{"usage":{"prompt_tokens":1,"prompt_tokens":2,"completion_tokens":0}}`, "invalid", "", ""},
		{"inconsistent total", BillingUsageSourceOAIChat, `{"usage":{"prompt_tokens":1,"completion_tokens":2,"total_tokens":1}}`, "invalid", "", ""},
		{"absent output", BillingUsageSourceOAIChat, `{"usage":{"prompt_tokens":1}}`, "incomplete", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := NativeTokenEvidence{Source: tc.source}
			e.Observe([]byte(tc.raw), false)
			p := nativeTokenTestPrice("chat", "input", "output")
			u := e.Usage(p)
			assert.Equal(t, tc.state, u.State)
			if tc.state == "confirmed" {
				require.NoError(t, metering.ValidateUsage(p, u))
				require.Len(t, u.Measurements, 2)
				assert.Equal(t, tc.input, u.Measurements[0].Quantity.Value)
				assert.Equal(t, tc.output, u.Measurements[1].Quantity.Value)
			}
		})
	}
}

func nativeTokenTestPrice(op string, keys ...string) metering.Price {
	p := metering.Price{Version: "metered_v2", Operation: op, Policy: "actual"}
	for _, key := range keys {
		p.Terms = append(p.Terms, metering.Term{Key: key, Unit: "token", Price: metering.Rational{Numerator: "1", Denominator: "1"}})
	}
	return p
}

func TestNativeCumulativeAndExclusiveCounters(t *testing.T) {
	e := NativeTokenEvidence{Source: BillingUsageSourceOAIChat}
	e.Observe([]byte(`{"usage":{"prompt_tokens":10,"completion_tokens":4}}`), true)
	e.Observe([]byte(`{"usage":{"prompt_tokens":10,"completion_tokens":8,"total_tokens":18,"prompt_tokens_details":{"cached_tokens":3},"completion_tokens_details":{"reasoning_tokens":2}}}`), true)
	u := e.Usage(nativeTokenTestPrice("chat", "input", "output", "cache_read", "reasoning"))
	require.Equal(t, "confirmed", u.State)
	for i, want := range []string{"7", "6", "3", "2"} {
		assert.Equal(t, want, u.Measurements[i].Quantity.Value)
	}
	e.Observe([]byte(`{"usage":{"prompt_tokens":10,"completion_tokens":7}}`), true)
	assert.Equal(t, "invalid", e.Usage(nativeTokenTestPrice("chat", "input", "output")).State)
}

func TestNativeClaudeCacheOnlyAndGeminiThoughts(t *testing.T) {
	e := NativeTokenEvidence{Source: BillingUsageSourceClaudeMessages}
	e.Observe([]byte(`{"type":"message_start","message":{"usage":{"input_tokens":0,"output_tokens":0,"cache_read_input_tokens":8,"cache_creation_input_tokens":0}}}`), true)
	assert.Equal(t, "incomplete", e.Usage(nativeTokenTestPrice("chat", "input", "output", "cache_read", "cache_write")).State)
	e.Observe([]byte(`{"type":"message_stop"}`), true)
	u := e.Usage(nativeTokenTestPrice("chat", "input", "output", "cache_read", "cache_write"))
	require.Equal(t, "confirmed", u.State)
	assert.Equal(t, "8", u.Measurements[2].Quantity.Value)
	g := NativeTokenEvidence{Source: BillingUsageSourceGeminiChat}
	g.Observe([]byte(`{"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":4,"thoughtsTokenCount":3,"cachedContentTokenCount":2,"totalTokenCount":17}}`), false)
	u = g.Usage(nativeTokenTestPrice("chat", "input", "output", "cache_read", "reasoning"))
	require.Equal(t, "confirmed", u.State)
	for i, want := range []string{"8", "4", "2", "3"} {
		assert.Equal(t, want, u.Measurements[i].Quantity.Value)
	}
}

func TestNativeEmbeddingBatchUsesProviderAggregateOnce(t *testing.T) {
	e := NativeTokenEvidence{Source: BillingUsageSourceOAIChat}
	e.Observe([]byte(`{"data":[{"embedding":[1]},{"embedding":[2]}],"usage":{"prompt_tokens":23,"total_tokens":23}}`), false)
	u := e.Usage(nativeTokenTestPrice("embedding", "input"))
	require.Equal(t, "confirmed", u.State)
	require.Len(t, u.Measurements, 1)
	assert.Equal(t, "23", u.Measurements[0].Quantity.Value)
}
