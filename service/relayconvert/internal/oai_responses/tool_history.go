package oairesponses

import (
	"encoding/json"
	"fmt"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/service/relayconvert/internal/toolcallid"
)

func parseResponsesFunctionArguments(value any) (map[string]any, error) {
	if arguments, ok := value.(string); ok {
		return toolcallid.ParseObjectArguments(arguments)
	}
	raw, err := common.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("invalid function arguments: %w", err)
	}
	return toolcallid.ParseObjectArguments(string(raw))
}

func normalizeResponsesResultID(ids *toolcallid.Normalizer, item map[string]any) (string, error) {
	sourceID := rawResponsesCallID(item)
	if sourceID == "" {
		return "", fmt.Errorf("unknown tool result id: id is empty")
	}
	normalized, ok := ids.LookupResult(sourceID)
	if !ok {
		return "", fmt.Errorf("unknown tool result id %q", sourceID)
	}
	return normalized, nil
}

func rawResponsesCallID(item map[string]any) string {
	if callID := common.Interface2String(item["call_id"]); callID != "" {
		return callID
	}
	return common.Interface2String(item["id"])
}

func responsesToolCallIDJSON(id string) json.RawMessage {
	raw, _ := common.Marshal(id)
	return raw
}
