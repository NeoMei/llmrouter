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
// cannot be loaded from the configured store.
var ErrNotFound = errors.New("responses state record not found")

// ErrReplayRejected is returned when a replay request violates safety bounds
// (cycle, too many hops, cross-model, oversized record, etc.).
var ErrReplayRejected = errors.New("responses replay rejected")

// ErrLegacyScope is returned when only a legacy, incompletely scoped record
// exists for a response ID. Legacy state is never replayed.
var ErrLegacyScope = errors.New("responses state uses a legacy scope")

// ErrScopeMismatch is returned when a v2 record exists but belongs to a
// different continuity identity.
var ErrScopeMismatch = errors.New("responses state scope mismatch")

// CurrentVersion is the only record version accepted for new continuity
// state. Version changes are fail-closed rather than implicitly migrated.
const CurrentVersion = 2

// Scope binds a continuity record to the complete routing and caller identity
// that produced it.
type Scope struct {
	TenantID     int    `json:"tenant_id"`
	UserID       int    `json:"user_id"`
	TokenID      int    `json:"token_id"`
	RouteID      int    `json:"route_id"`
	TargetID     int    `json:"target_id"`
	ChannelID    int    `json:"channel_id"`
	ClientTaskID string `json:"client_task_id"`
	Model        string `json:"model"`
}

// Record is a normalized, bounded turn stored for stateful continuity.
type Record struct {
	Version          int             `json:"version"`
	Scope            Scope           `json:"scope"`
	ResponseID       string          `json:"response_id"`
	ParentResponseID string          `json:"parent_response_id,omitempty"`
	Input            json.RawMessage `json:"input"`
	Output           json.RawMessage `json:"output"`
	ExpiresAt        time.Time       `json:"expires_at"`
}

// Store backs bounded Responses state continuity. Implementations enforce the
// complete expected scope, TTL, and byte limits.
type Store interface {
	Load(ctx context.Context, expected Scope, responseID string) (Record, error)
	Save(ctx context.Context, record Record) error
}

// Key returns a stable storage key for a record. It never embeds the raw
// response ID to avoid collisions with Redis key namespaces.
func Key(responseID string) string {
	h := sha256.Sum256([]byte(responseID))
	return "responses-state:v2:" + hex.EncodeToString(h[:])
}

// legacyKey reproduces the former tenant/token key solely so loads can detect
// and explicitly reject old records instead of treating them as absent.
func legacyKey(tenantID, tokenID int, responseID string) string {
	h := sha256.Sum256([]byte(responseID))
	return fmt.Sprintf("responses-state:%d:%d:%s", tenantID, tokenID, hex.EncodeToString(h[:]))
}

func scopeDigest(scope Scope) string {
	data, _ := common.Marshal(scope)
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

func scopedKey(scope Scope, responseID string) string {
	return Key(responseID) + ":" + scopeDigest(scope)
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
	if record.Version != CurrentVersion {
		return fmt.Errorf("%w: unsupported record version %d", ErrLegacyScope, record.Version)
	}
	if record.Scope.TenantID <= 0 {
		return fmt.Errorf("%w: tenant id is required", ErrScopeMismatch)
	}
	if record.Scope.UserID <= 0 {
		return fmt.Errorf("%w: user id is required", ErrScopeMismatch)
	}
	if record.Scope.TokenID <= 0 {
		return fmt.Errorf("%w: token id is required", ErrScopeMismatch)
	}
	if record.Scope.RouteID <= 0 {
		return fmt.Errorf("%w: route id is required", ErrScopeMismatch)
	}
	if record.Scope.TargetID <= 0 {
		return fmt.Errorf("%w: target id is required", ErrScopeMismatch)
	}
	if record.Scope.ChannelID <= 0 {
		return fmt.Errorf("%w: channel id is required", ErrScopeMismatch)
	}
	if strings.TrimSpace(record.Scope.Model) == "" {
		return fmt.Errorf("%w: model is required", ErrScopeMismatch)
	}
	if strings.TrimSpace(record.ResponseID) == "" {
		return errors.New("response id is required")
	}
	if len(record.Input) > MaxRecordBytes || len(record.Output) > MaxRecordBytes {
		return errors.New("responses state record exceeds maximum size")
	}
	return nil
}

func scopeMatches(expected, actual Scope) bool {
	return expected.TenantID == actual.TenantID &&
		expected.UserID == actual.UserID &&
		expected.TokenID == actual.TokenID &&
		expected.RouteID == actual.RouteID &&
		expected.TargetID == actual.TargetID &&
		expected.ChannelID == actual.ChannelID &&
		expected.Model == actual.Model &&
		(expected.ClientTaskID == "" || expected.ClientTaskID == actual.ClientTaskID)
}

// Expand prepends stored turns from previous_response_id chains into the request
// input. It only expands when the selected wire cannot handle native state; the
// caller is responsible for removing previous_response_id after expansion.
func Expand(ctx context.Context, store Store, expected Scope, request json.RawMessage) (json.RawMessage, error) {
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

		record, err := store.Load(ctx, expected, previousID)
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
// replay. It is a best-effort helper: callers pass the complete route scope,
// response ID, normalized input items, and output items. The record is bounded
// by ValidateRecord and silently ignored on validation failure so a bad turn
// never breaks the relay path.
func SaveTurn(ctx context.Context, store Store, scope Scope, responseID string, input, output json.RawMessage) {
	if store == nil || responseID == "" {
		return
	}
	if scope.ClientTaskID == "" {
		scope.ClientTaskID = responseID
	}
	record := Record{
		Version:    CurrentVersion,
		Scope:      scope,
		ResponseID: responseID,
		Input:      input,
		Output:     output,
		ExpiresAt:  time.Now().Add(DefaultRecordTTL),
	}
	if err := store.Save(ctx, record); err != nil {
		common.SysLog("responses state save skipped: " + err.Error())
	}
}
