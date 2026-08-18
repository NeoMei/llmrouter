package oaichat

import (
	"encoding/json"
	"fmt"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/service/relayconvert/internal/toolcallid"
)

func parseMessageToolCalls(raw json.RawMessage) ([]dto.ToolCallRequest, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var calls []dto.ToolCallRequest
	if err := common.UnmarshalUnique(raw, &calls); err != nil {
		return nil, fmt.Errorf("invalid tool_calls: %w", err)
	}
	return calls, nil
}

func normalizeToolResultID(ids *toolcallid.Normalizer, sourceID string) (string, error) {
	if sourceID == "" {
		return "", fmt.Errorf("unknown tool result id: id is empty")
	}
	normalized, ok := ids.Lookup(sourceID)
	if !ok {
		return "", fmt.Errorf("unknown tool result id %q", sourceID)
	}
	return normalized, nil
}

func toolCallIDJSON(id string) json.RawMessage {
	raw, _ := common.Marshal(id)
	return raw
}
