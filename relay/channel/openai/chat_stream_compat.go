package openai

import (
	"bytes"
	"encoding/json"

	"github.com/QuantumNous/new-api/common"
)

// normalizeChatToolDeltaPlaceholders removes null placeholders that some
// OpenAI-compatible providers use for absent tool-call delta fields. The
// validator still checks the complete call and rejects all other bad types.
func normalizeChatToolDeltaPlaceholders(raw []byte) []byte {
	if !bytes.Contains(raw, []byte("null")) || !bytes.Contains(raw, []byte(`"tool_calls"`)) {
		return raw
	}
	var event map[string]json.RawMessage
	if common.UnmarshalUnique(raw, &event) != nil {
		return raw
	}
	var choices []map[string]json.RawMessage
	if common.UnmarshalUnique(event["choices"], &choices) != nil {
		return raw
	}
	changed := false
	for _, choice := range choices {
		var delta map[string]json.RawMessage
		if common.UnmarshalUnique(choice["delta"], &delta) != nil {
			continue
		}
		var calls []map[string]json.RawMessage
		if common.UnmarshalUnique(delta["tool_calls"], &calls) != nil {
			continue
		}
		deltaChanged := false
		for _, call := range calls {
			if bytes.Equal(bytes.TrimSpace(call["id"]), []byte("null")) {
				delete(call, "id")
				deltaChanged = true
			}
			var function map[string]json.RawMessage
			if common.UnmarshalUnique(call["function"], &function) == nil && bytes.Equal(bytes.TrimSpace(function["arguments"]), []byte("null")) {
				delete(function, "arguments")
				call["function"], _ = common.Marshal(function)
				deltaChanged = true
			}
		}
		if deltaChanged {
			delta["tool_calls"], _ = common.Marshal(calls)
			choice["delta"], _ = common.Marshal(delta)
			changed = true
		}
	}
	if !changed {
		return raw
	}
	event["choices"], _ = common.Marshal(choices)
	result, err := common.Marshal(event)
	if err != nil {
		return raw
	}
	return result
}
