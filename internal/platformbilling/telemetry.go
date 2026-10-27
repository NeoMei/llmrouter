package platformbilling

import (
	"context"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/pkg/metering"
	"gorm.io/gorm"
)

// VerifiedScope is copied from the admitted capability, never incoming headers.
type VerifiedScope struct {
	ActorID, AccountID, CredentialID, OriginProductID, FundingProductID string
	InstanceID, CallID, TaskID                                          string
}

func (s *Session) VerifiedScope() VerifiedScope {
	return VerifiedScope{s.call.ActorID, s.call.AccountID, s.call.CredentialID, s.call.OriginProductID, s.call.FundingProductID, s.call.InstanceID, s.call.CallID, s.call.TaskID}
}

// RecordTelemetry adds execution facts to the existing logical call. Updating
// that primary key cannot charge funds or turn retries into extra requests.
func (s *Session) RecordTelemetry(ctx context.Context, group string, latencyMS int64, stream bool) (bool, error) {
	if s == nil || s.adapter == nil || s.adapter.db == nil || latencyMS < 0 || len(group) > 128 {
		return false, ErrInvalid
	}
	result := s.adapter.db.WithContext(ctx).Model(&Call{}).Where("call_id = ? AND telemetry_completed_at = ?", s.call.CallID, 0).Updates(map[string]any{
		"telemetry_group": group, "telemetry_latency_ms": latencyMS, "telemetry_is_stream": stream, "telemetry_completed_at": time.Now().Unix(), "telemetry_outcome": "success",
	})
	return result.RowsAffected == 1, result.Error
}

// ConfirmedTokenUsage exposes only provider evidence accepted by the funding
// observer. The relay's subsequently estimated dto usage is never consulted.
func (s *Session) ConfirmedTokenUsage() *dto.Usage {
	if s == nil || s.confirmed == nil || s.confirmed.Metered != nil {
		return nil
	}
	prompt, output, ok := technicalTokenCounts(*s.confirmed)
	if !ok {
		return nil
	}
	return &dto.Usage{PromptTokens: prompt, CompletionTokens: output, TotalTokens: addTechnicalTokens(prompt, int64(output)), UsageSource: "provider"}
}

type TechnicalScope struct {
	ActorID    string
	InstanceID string
	AllActors  bool // only product log readers may request this server-side scope
}

// TechnicalUsage is a safe read model. Supplier bodies, funding allocations,
// raw errors, rate snapshots and model credentials never leave this boundary.
type TechnicalUsage struct {
	Scope                          VerifiedScope
	Model, Operation, Group        string
	Status, Outcome, UsageState    string
	Asset, Charged                 string
	PromptTokens, CompletionTokens int
	AttemptCount, ChannelID        int
	LatencyMS                      int64
	IsStream                       bool
	CreatedAt                      int64
	Measurements                   []metering.Measurement
}

// ReadTechnicalUsage counts durable logical calls, including unknown outcomes
// and asynchronous tasks. It is independent of the shared routing principal.
func ReadTechnicalUsage(ctx context.Context, db *gorm.DB, scope TechnicalScope, start, end int64) ([]TechnicalUsage, error) {
	if db == nil || scope.InstanceID == "" || (!scope.AllActors && strings.TrimSpace(scope.ActorID) == "") || len(scope.ActorID) > 128 || start < 0 || end < start || end == math.MaxInt64 || end-start > 2592000 {
		return nil, ErrInvalid
	}
	rows := make([]TechnicalUsage, 0)
	if !db.Migrator().HasTable(&Call{}) {
		return rows, nil
	}
	query := db.WithContext(ctx).Where("funding_product_id = ?", "llmrouter")
	if db.Dialector.Name() == "sqlite" {
		// SQLite stores timestamp offsets in text. Compare instants so an UTC
		// importer and a local-time process share the same reporting interval.
		query = query.Where("julianday(created_at) >= julianday(?) AND julianday(created_at) < julianday(?)", time.Unix(start, 0).UTC().Format(time.RFC3339Nano), time.Unix(end+1, 0).UTC().Format(time.RFC3339Nano))
	} else {
		query = query.Where("created_at >= ? AND created_at < ?", time.Unix(start, 0), time.Unix(end+1, 0))
	}
	if scope.InstanceID == "default" {
		query = query.Where("instance_id = ? OR instance_id = ?", scope.InstanceID, "")
	} else {
		query = query.Where("instance_id = ?", scope.InstanceID)
	}
	if !scope.AllActors {
		query = query.Where("actor_id = ?", scope.ActorID)
	}
	var calls []Call
	if err := query.Order("created_at DESC, call_id DESC").Find(&calls).Error; err != nil {
		return nil, err
	}
	if len(calls) == 0 {
		return rows, nil
	}
	ids := make([]string, len(calls))
	for i := range calls {
		ids[i] = calls[i].CallID
	}
	var attempts []Attempt
	if err := db.WithContext(ctx).Where("call_id IN ?", ids).Order("id ASC").Find(&attempts).Error; err != nil {
		return nil, err
	}
	byCall := make(map[string][]Attempt)
	for _, attempt := range attempts {
		byCall[attempt.CallID] = append(byCall[attempt.CallID], attempt)
	}
	for _, call := range calls {
		r := TechnicalUsage{
			Scope: VerifiedScope{call.ActorID, call.AccountID, call.CredentialID, call.OriginProductID, call.FundingProductID, call.InstanceID, call.CallID, call.TaskID},
			Model: call.Model, Operation: call.Operation, Group: call.TelemetryGroup, Status: call.Status, UsageState: "missing", Outcome: "pending",
			Asset: call.Asset, Charged: call.Charged, CreatedAt: call.CreatedAt.Unix(), LatencyMS: call.TelemetryLatencyMS, IsStream: call.TelemetryIsStream,
		}
		if r.Operation == "" {
			r.Operation = "chat"
		}
		if r.Scope.InstanceID == "" {
			r.Scope.InstanceID = "default"
		}
		for _, attempt := range byCall[call.CallID] {
			r.AttemptCount++
			r.ChannelID = attempt.ChannelID
		}
		if call.Status == "RELEASED" || call.Status == "RELEASE_PENDING" || call.Status == "REJECTED" {
			r.Outcome = "failed"
		} else if call.TelemetryOutcome == "success" {
			r.Outcome = "success"
		}
		usage := Usage{}
		if call.Operation != "" && call.Evidence != "" {
			usage.Metered = &metering.Usage{}
			if common.UnmarshalJsonStr(call.Evidence, usage.Metered) != nil {
				usage.Metered = nil
			}
		} else if call.Intent != "" && (call.Status == "SETTLED" || call.Status == "SETTLE_PENDING") {
			var intent struct {
				Usage Usage `json:"usage"`
			}
			if common.UnmarshalJsonStr(call.Intent, &intent) == nil {
				usage = intent.Usage
			}
		}
		if call.EvidenceConflict {
			r.UsageState = "conflict"
		} else if usage.Metered != nil {
			r.UsageState = usage.Metered.State
			var rate Rate
			if common.UnmarshalJsonStr(call.RateSnapshot, &rate) == nil && rate.Metered != nil && metering.ValidateUsage(*rate.Metered, *usage.Metered) == nil {
				r.UsageState, r.Outcome = "confirmed", "success"
				r.Measurements = usage.Metered.Measurements
				for _, m := range r.Measurements {
					if m.Unit != "token" || m.Quantity.Scale != 0 {
						continue
					}
					n, err := strconv.ParseInt(m.Quantity.Value, 10, 32)
					if err != nil || n < 0 {
						continue
					}
					switch m.Key {
					case "input", "cache_read", "cache_write":
						r.PromptTokens = addTechnicalTokens(r.PromptTokens, n)
					case "output", "reasoning":
						r.CompletionTokens = addTechnicalTokens(r.CompletionTokens, n)
					}
				}
			} else if r.UsageState == "confirmed" {
				r.UsageState = "invalid"
			}
		} else if usage.Input != "" {
			prompt, output, valid := technicalTokenCounts(usage)
			if valid {
				r.PromptTokens, r.CompletionTokens = prompt, output
				r.UsageState, r.Outcome = "confirmed", "success"
			} else {
				r.UsageState, r.PromptTokens, r.CompletionTokens = "invalid", 0, 0
			}
		}
		rows = append(rows, r)
	}
	return rows, nil
}

func technicalTokenCounts(usage Usage) (int, int, bool) {
	prompt, output := 0, 0
	for i, value := range []string{usage.Input, usage.CacheRead, usage.CacheWrite, usage.Output, usage.Reasoning} {
		n, err := strconv.ParseInt(value, 10, 32)
		if err != nil || n < 0 {
			return 0, 0, false
		}
		if i < 3 {
			prompt = addTechnicalTokens(prompt, n)
		} else {
			output = addTechnicalTokens(output, n)
		}
	}
	return prompt, output, true
}

func addTechnicalTokens(current int, count int64) int {
	if count > math.MaxInt32-int64(current) {
		return math.MaxInt32
	}
	return current + int(count)
}
