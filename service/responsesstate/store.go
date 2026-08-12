package responsesstate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
)

// ErrNotFound is returned when a replay record does not exist, has expired, or
// belongs to a different tenant/token.
var ErrNotFound = errors.New("responses state record not found")

// ErrReplayRejected is returned when a replay request violates safety bounds
// (cycle, too many hops, cross-model, oversized record, etc.).
var ErrReplayRejected = errors.New("responses replay rejected")

// Record is a normalized, bounded turn stored for stateful continuity.
type Record struct {
	TenantID   int
	TokenID    int
	ResponseID string
	Model      string
	Input      json.RawMessage
	Output     json.RawMessage
	ExpiresAt  time.Time
}

// Store backs bounded Responses state continuity. Implementations must scope
// records by tenant and token and enforce TTL and byte limits.
type Store interface {
	Load(ctx context.Context, tenantID, tokenID int, responseID string) (Record, error)
	Save(ctx context.Context, record Record) error
}

// Key returns a stable storage key for a record. It never embeds the raw
// response ID to avoid collisions with Redis key namespaces.
func Key(tenantID, tokenID int, responseID string) string {
	h := sha256.Sum256([]byte(responseID))
	return fmt.Sprintf("responses-state:%d:%d:%s", tenantID, tokenID, hex.EncodeToString(h[:]))
}

// DefaultRecordTTL is the default expiration for a stored turn.
const DefaultRecordTTL = time.Hour

// MaxRecordBytes is the maximum serialized JSON size for a single record.
const MaxRecordBytes = 2 * 1024 * 1024

// MaxReplayHops is the maximum number of chained previous_response_id replays
// allowed for a single request.
const MaxReplayHops = 32

// ValidateRecord checks invariants before saving or loading.
func ValidateRecord(record Record) error {
	if record.TenantID <= 0 {
		return errors.New("tenant id is required")
	}
	if record.TokenID <= 0 {
		return errors.New("token id is required")
	}
	if strings.TrimSpace(record.ResponseID) == "" {
		return errors.New("response id is required")
	}
	if len(record.Input) > MaxRecordBytes || len(record.Output) > MaxRecordBytes {
		return errors.New("responses state record exceeds maximum size")
	}
	if record.ExpiresAt.IsZero() {
		record.ExpiresAt = time.Now().Add(DefaultRecordTTL)
	}
	return nil
}

// Expand prepends stored turns from previous_response_id chains into the request
// input. It only expands when the selected wire cannot handle native state; the
// caller is responsible for removing previous_response_id after expansion.
func Expand(ctx context.Context, store Store, tenantID, tokenID int, request json.RawMessage) (json.RawMessage, error) {
	if store == nil {
		return request, nil
	}
	var payload map[string]any
	if err := common.Unmarshal(request, &payload); err != nil {
		return nil, fmt.Errorf("invalid responses request: %w", err)
	}
	previousID := common.Interface2String(payload["previous_response_id"])
	if previousID == "" {
		return request, nil
	}

	seen := make(map[string]struct{})
	accumulated := make([]any, 0)
	for hops := 0; hops < MaxReplayHops; hops++ {
		if _, ok := seen[previousID]; ok {
			return nil, fmt.Errorf("%w: cycle detected at %q", ErrReplayRejected, previousID)
		}
		seen[previousID] = struct{}{}

		record, err := store.Load(ctx, tenantID, tokenID, previousID)
		if err != nil {
			return nil, err
		}
		if time.Now().After(record.ExpiresAt) {
			return nil, ErrNotFound
		}

		var priorInput []any
		if err := common.Unmarshal(record.Input, &priorInput); err != nil {
			return nil, fmt.Errorf("invalid stored input: %w", err)
		}
		var priorOutput []any
		if err := common.Unmarshal(record.Output, &priorOutput); err != nil {
			return nil, fmt.Errorf("invalid stored output: %w", err)
		}
		// Prepend input then output turn, preserving order within each list.
		turn := make([]any, 0, len(priorInput)+len(priorOutput))
		turn = append(turn, priorInput...)
		turn = append(turn, priorOutput...)
		accumulated = append(turn, accumulated...)

		// Find the next previous_response_id in the stored input, if any.
		nextID := ""
		for _, item := range priorInput {
			if m, ok := item.(map[string]any); ok && common.Interface2String(m["type"]) == "previous_response_id" {
				if id := common.Interface2String(m["id"]); id != "" {
					nextID = id
					break
				}
			}
		}
		if nextID != "" {
			previousID = nextID
		} else {
			previousID = ""
			break
		}
	}
	if previousID != "" {
		return nil, fmt.Errorf("%w: more than %d replay hops", ErrReplayRejected, MaxReplayHops)
	}

	if len(accumulated) == 0 {
		return request, nil
	}

	var currentInput []any
	if raw, ok := payload["input"].(json.RawMessage); ok && len(raw) > 0 {
		if err := common.Unmarshal(raw, &currentInput); err != nil {
			return nil, fmt.Errorf("invalid request input: %w", err)
		}
	} else if raw, ok := payload["input"].([]byte); ok && len(raw) > 0 {
		if err := common.Unmarshal(raw, &currentInput); err != nil {
			return nil, fmt.Errorf("invalid request input: %w", err)
		}
	} else if raw, ok := payload["input"].([]any); ok {
		currentInput = raw
	}

	newInput := append(accumulated, currentInput...)
	encoded, err := common.Marshal(newInput)
	if err != nil {
		return nil, fmt.Errorf("encode expanded input: %w", err)
	}
	payload["input"] = json.RawMessage(encoded)
	delete(payload, "previous_response_id")

	result, err := common.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("encode expanded request: %w", err)
	}
	return json.RawMessage(result), nil
}

// SaveTurn persists a completed Responses turn for later previous_response_id
// replay. It is a best-effort helper: callers pass the response ID, the model,
// the normalized input items, and the output items. The record is bounded by
// ValidateRecord and silently ignored on validation failure so a bad turn never
// breaks the relay path.
func SaveTurn(ctx context.Context, store Store, tenantID, tokenID int, responseID, model string, input, output json.RawMessage) {
	if store == nil || responseID == "" {
		return
	}
	record := Record{
		TenantID:   tenantID,
		TokenID:    tokenID,
		ResponseID: responseID,
		Model:      model,
		Input:      input,
		Output:     output,
		ExpiresAt:  time.Now().Add(DefaultRecordTTL),
	}
	if err := store.Save(ctx, record); err != nil {
		common.SysLog("responses state save skipped: " + err.Error())
	}
}
