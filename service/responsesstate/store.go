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
	"github.com/QuantumNous/new-api/dto"
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

// ErrInvalidClientTaskID is returned when client_metadata.task_id cannot be
// used as a bounded continuity identity.
var ErrInvalidClientTaskID = errors.New("invalid responses client task id")

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

// Expansion contains both the materialized request sent upstream and the
// unmaterialized continuity metadata needed to persist the current turn.
type Expansion struct {
	Request          json.RawMessage
	Scope            Scope
	ParentResponseID string
	CurrentInput     json.RawMessage
}

// ContinuityContext is the request-scoped, unmaterialized turn state passed
// from request expansion to response persistence.
type ContinuityContext struct {
	Scope            Scope
	ParentResponseID string
	CurrentInput     json.RawMessage
}

// ParseClientTaskID extracts the only client-controlled continuity identity.
// Other client_metadata fields are deliberately ignored.
func ParseClientTaskID(metadata json.RawMessage) (string, error) {
	if len(metadata) == 0 {
		return "", nil
	}
	var fields map[string]json.RawMessage
	if err := common.Unmarshal(metadata, &fields); err != nil {
		return "", fmt.Errorf("%w: %v", ErrInvalidClientTaskID, err)
	}
	rawTaskID, exists := fields["task_id"]
	if !exists || string(rawTaskID) == "null" {
		return "", nil
	}
	var parsedTaskID string
	if err := common.Unmarshal(rawTaskID, &parsedTaskID); err != nil {
		return "", fmt.Errorf("%w: %v", ErrInvalidClientTaskID, err)
	}
	taskID := strings.TrimSpace(parsedTaskID)
	if taskID == "" {
		return "", nil
	}
	if len(taskID) > 128 {
		return "", fmt.Errorf("%w: task_id exceeds 128 bytes", ErrInvalidClientTaskID)
	}
	for _, ch := range []byte(taskID) {
		if (ch >= 'A' && ch <= 'Z') || (ch >= 'a' && ch <= 'z') ||
			(ch >= '0' && ch <= '9') || ch == '.' || ch == '_' || ch == ':' || ch == '-' {
			continue
		}
		return "", fmt.Errorf("%w: task_id contains unsupported characters", ErrInvalidClientTaskID)
	}
	return taskID, nil
}

// EligibleCompletedResponse reports whether a response is safe to retain for
// future continuity replay.
func EligibleCompletedResponse(response *dto.OpenAIResponsesResponse) bool {
	if response == nil || strings.TrimSpace(response.ID) == "" || response.Error != nil || response.IncompleteDetails != nil {
		return false
	}
	var status string
	if err := common.Unmarshal(response.Status, &status); err != nil || status != "completed" {
		return false
	}
	for _, output := range response.Output {
		if output.Type == "refusal" {
			return false
		}
		for _, content := range output.Content {
			if content.Type == "refusal" {
				return false
			}
		}
	}
	return true
}

func materializedInputItems(raw json.RawMessage, label string) ([]any, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	switch common.GetJsonType(raw) {
	case "array":
		var items []any
		if err := common.Unmarshal(raw, &items); err != nil {
			return nil, fmt.Errorf("invalid %s input: %w", label, err)
		}
		return items, nil
	case "string":
		var text string
		if err := common.Unmarshal(raw, &text); err != nil {
			return nil, fmt.Errorf("invalid %s input: %w", label, err)
		}
		return []any{map[string]any{
			"type":    "message",
			"role":    "user",
			"content": text,
		}}, nil
	default:
		return nil, fmt.Errorf("invalid %s input: expected string or array", label)
	}
}

// Expand prepends stored turns from a bounded ParentResponseID chain into the
// request input. Stored turns contain only their own input and output, so
// replay and persistence grow linearly with the number of retained turns.
func Expand(ctx context.Context, store Store, expected Scope, request json.RawMessage) (Expansion, error) {
	var payload map[string]any
	if err := common.Unmarshal(request, &payload); err != nil {
		return Expansion{}, fmt.Errorf("invalid responses request: %w", err)
	}
	var envelope struct {
		PreviousResponseID string          `json:"previous_response_id"`
		Input              json.RawMessage `json:"input"`
	}
	if err := common.Unmarshal(request, &envelope); err != nil {
		return Expansion{}, fmt.Errorf("invalid responses request: %w", err)
	}
	expansion := Expansion{
		Request:          append(json.RawMessage(nil), request...),
		Scope:            expected,
		ParentResponseID: envelope.PreviousResponseID,
		CurrentInput:     append(json.RawMessage(nil), envelope.Input...),
	}
	previousID := envelope.PreviousResponseID
	if previousID == "" {
		return expansion, nil
	}
	if store == nil {
		return Expansion{}, errors.New("responses state store is nil")
	}

	seen := make(map[string]struct{})
	records := make([]Record, 0)
	loadScope := expected
	for hops := 0; hops < MaxReplayHops; hops++ {
		if _, ok := seen[previousID]; ok {
			return Expansion{}, fmt.Errorf("%w: cycle detected at %q", ErrReplayRejected, previousID)
		}
		seen[previousID] = struct{}{}

		record, err := store.Load(ctx, loadScope, previousID)
		if err != nil {
			return Expansion{}, err
		}
		if time.Now().After(record.ExpiresAt) {
			return Expansion{}, ErrNotFound
		}
		records = append(records, record)
		if len(records) == 1 {
			expansion.Scope = record.Scope
			loadScope = record.Scope
		}
		if record.ParentResponseID == "" {
			previousID = ""
			break
		}
		previousID = record.ParentResponseID
	}
	if previousID != "" {
		return Expansion{}, fmt.Errorf("%w: more than %d replay hops", ErrReplayRejected, MaxReplayHops)
	}

	currentInput, err := materializedInputItems(expansion.CurrentInput, "request")
	if err != nil {
		return Expansion{}, err
	}

	accumulated := make([]any, 0)
	for i := len(records) - 1; i >= 0; i-- {
		priorInput, err := materializedInputItems(records[i].Input, "stored")
		if err != nil {
			return Expansion{}, err
		}
		var priorOutput []any
		if err := common.Unmarshal(records[i].Output, &priorOutput); err != nil {
			return Expansion{}, fmt.Errorf("invalid stored output: %w", err)
		}
		accumulated = append(accumulated, priorInput...)
		accumulated = append(accumulated, priorOutput...)
	}
	newInput := append(accumulated, currentInput...)
	encoded, err := common.Marshal(newInput)
	if err != nil {
		return Expansion{}, fmt.Errorf("encode expanded input: %w", err)
	}
	payload["input"] = json.RawMessage(encoded)
	delete(payload, "previous_response_id")

	result, err := common.Marshal(payload)
	if err != nil {
		return Expansion{}, fmt.Errorf("encode expanded request: %w", err)
	}
	expansion.Request = json.RawMessage(result)
	return expansion, nil
}

// SaveTurn persists a completed Responses turn for later previous_response_id
// replay. Callers provide a complete v2 record and decide whether a returned
// persistence error is best-effort for their request path.
func SaveTurn(ctx context.Context, store Store, record Record) error {
	if store == nil {
		return errors.New("responses state store is nil")
	}
	if record.Scope.ClientTaskID == "" {
		record.Scope.ClientTaskID = record.ResponseID
	}
	if record.ExpiresAt.IsZero() {
		record.ExpiresAt = time.Now().Add(DefaultRecordTTL)
	}
	return store.Save(ctx, record)
}
