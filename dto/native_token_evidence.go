package dto

import (
	"math/big"
	"strings"

	"github.com/QuantumNous/new-api/pkg/metering"
	"github.com/tidwall/gjson"
)

// NativeTokenEvidence retains provider counters before legacy fallback or conversion.
// Stream counters are cumulative snapshots, never additive deltas.
type NativeTokenEvidence struct {
	Source    string
	Operation string
	values    map[string]int64
	invalid   bool
	seen      bool
	terminal  bool
}

func (e *NativeTokenEvidence) Observe(raw []byte, stream bool) {
	if !gjson.ValidBytes(raw) {
		e.invalid = true
		return
	}
	root := gjson.ParseBytes(raw)
	if duplicateUsageKeys(root) {
		e.invalid = true
		return
	}
	u := root.Get("usage")
	switch e.Source {
	case BillingUsageSourceOAIResponses:
		if stream {
			if root.Get("type").String() != "response.completed" {
				return
			}
			u = root.Get("response.usage")
		}
	case BillingUsageSourceClaudeMessages:
		if stream {
			switch root.Get("type").String() {
			case "message_start":
				u = root.Get("message.usage")
			case "message_delta":
			case "message_stop":
				e.terminal = true
				return
			default:
				return
			}
		}
	case BillingUsageSourceGeminiChat:
		u = root.Get("usageMetadata")
	}
	if !u.Exists() || u.Type == gjson.Null {
		return
	}
	e.seen = true
	if !u.IsObject() {
		e.invalid = true
		return
	}
	if e.values == nil {
		e.values = map[string]int64{}
	}
	names := []string{"prompt_tokens", "completion_tokens", "total_tokens", "prompt_tokens_details.cached_tokens", "prompt_tokens_details.cache_write_tokens", "completion_tokens_details.reasoning_tokens"}
	switch e.Source {
	case BillingUsageSourceOAIResponses:
		names = []string{"input_tokens", "output_tokens", "total_tokens", "input_tokens_details.cached_tokens", "input_tokens_details.cache_write_tokens", "output_tokens_details.reasoning_tokens"}
	case BillingUsageSourceClaudeMessages:
		names = []string{"input_tokens", "output_tokens", "cache_read_input_tokens", "cache_creation_input_tokens"}
	case BillingUsageSourceGeminiChat:
		names = []string{"promptTokenCount", "candidatesTokenCount", "totalTokenCount", "cachedContentTokenCount", "thoughtsTokenCount", "toolUsePromptTokenCount"}
	}
	for _, name := range names {
		v := u.Get(name)
		if !v.Exists() {
			continue
		}
		s := v.Raw
		if v.Type != gjson.Number || s == "" || strings.Trim(s, "0123456789") != "" {
			e.invalid = true
			continue
		}
		n, ok := new(big.Int).SetString(s, 10)
		if !ok || !n.IsInt64() {
			e.invalid = true
			continue
		}
		value := n.Int64()
		if previous, exists := e.values[name]; exists && (value < previous || (e.terminal && value != previous)) {
			e.invalid = true
		}
		e.values[name] = value
	}
	if !stream {
		e.terminal = true
	} else if e.Source == BillingUsageSourceOAIResponses && root.Get("type").String() == "response.completed" {
		e.terminal = true
	} else if e.Source == BillingUsageSourceOAIChat && root.Get("choices").Exists() {
		e.terminal = true
	} else if e.Source == BillingUsageSourceGeminiChat && root.Get("candidates").Exists() {
		e.terminal = true
	}
}

// Duplicate counters must not silently become last-key-wins billing evidence.
func duplicateUsageKeys(v gjson.Result) bool {
	duplicate := false
	if v.IsObject() {
		keys := map[string]bool{}
		v.ForEach(func(k, child gjson.Result) bool {
			if keys[k.String()] || duplicateUsageKeys(child) {
				duplicate = true
				return false
			}
			keys[k.String()] = true
			return true
		})
	} else if v.IsArray() {
		v.ForEach(func(_, child gjson.Result) bool {
			if duplicateUsageKeys(child) {
				duplicate = true
				return false
			}
			return true
		})
	}
	return duplicate
}

func (e *NativeTokenEvidence) Usage(price metering.Price) metering.Usage {
	u := metering.Usage{Version: "metered_v2", Operation: price.Operation, Source: e.Source, State: "missing"}
	if e.invalid {
		u.State = "invalid"
		return u
	}
	if !e.seen {
		return u
	}
	u.State = "incomplete"
	if !e.terminal {
		return u
	}
	p, c, total, cr, cw, r := "prompt_tokens", "completion_tokens", "total_tokens", "prompt_tokens_details.cached_tokens", "prompt_tokens_details.cache_write_tokens", "completion_tokens_details.reasoning_tokens"
	switch e.Source {
	case BillingUsageSourceOAIResponses:
		p, c, cr, cw, r = "input_tokens", "output_tokens", "input_tokens_details.cached_tokens", "input_tokens_details.cache_write_tokens", "output_tokens_details.reasoning_tokens"
	case BillingUsageSourceClaudeMessages:
		p, c, cr, cw, r = "input_tokens", "output_tokens", "cache_read_input_tokens", "cache_creation_input_tokens", ""
	case BillingUsageSourceGeminiChat:
		p, c, total, cr, cw, r = "promptTokenCount", "candidatesTokenCount", "totalTokenCount", "cachedContentTokenCount", "", "thoughtsTokenCount"
	}
	input, hasInput := e.values[p]
	output, hasOutput := e.values[c]
	if price.Operation == "embedding" {
		output = 0
		hasOutput = true
	}
	if !hasInput || !hasOutput {
		return u
	}
	cacheRead, cacheWrite, reasoning := e.values[cr], e.values[cw], e.values[r]
	sum := new(big.Int).Add(big.NewInt(input), big.NewInt(output))
	if e.Source == BillingUsageSourceGeminiChat {
		sum.Add(sum, big.NewInt(reasoning))
		if e.values["toolUsePromptTokenCount"] != 0 {
			u.State = "invalid"
			return u
		}
	}
	if actual, exists := e.values[total]; exists && e.Source != BillingUsageSourceClaudeMessages && sum.Cmp(big.NewInt(actual)) != 0 {
		u.State = "invalid"
		return u
	}
	if e.Source != BillingUsageSourceClaudeMessages {
		if cacheRead > input || cacheWrite > input-cacheRead {
			u.State = "invalid"
			return u
		}
		input -= cacheRead + cacheWrite
	}
	if e.Source != BillingUsageSourceGeminiChat {
		if reasoning > output {
			u.State = "invalid"
			return u
		}
		output -= reasoning
	}
	values := map[string]int64{"input": input, "output": output, "cache_read": cacheRead, "cache_write": cacheWrite, "reasoning": reasoning}
	for _, term := range price.Terms {
		value, known := values[term.Key]
		if !known || term.Unit != "token" {
			u.State = "invalid"
			return u
		}
		// A separately priced optional counter must be present, including explicit zero.
		field := map[string]string{"cache_read": cr, "cache_write": cw, "reasoning": r}[term.Key]
		if term.Key != "input" && term.Key != "output" {
			if _, exists := e.values[field]; !exists {
				return u
			}
		}
		u.Measurements = append(u.Measurements, metering.Measurement{Key: term.Key, Unit: "token", Quantity: metering.Quantity{Value: big.NewInt(value).String()}})
	}
	// Never silently discard a nonzero dimension omitted by the frozen tariff.
	for key, value := range values {
		if value == 0 {
			continue
		}
		found := false
		for _, term := range price.Terms {
			if term.Key == key {
				found = true
			}
		}
		if !found {
			u.State = "incomplete"
			return u
		}
	}
	u.State = "confirmed"
	return u
}
