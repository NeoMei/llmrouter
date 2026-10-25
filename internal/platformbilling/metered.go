package platformbilling

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"math/big"
	"net/http"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/metering"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func (a *Adapter) AdmitOperation(ctx context.Context, credential, task, id, operation, contentType string, body []byte) (*Session, error) {
	if !a.Enabled() || !stableID.MatchString(id) || !stableID.MatchString(task) || !strings.HasPrefix(credential, "uh_model_") || len(credential) > 1024 || int64(len(body)) > a.OperationBodyLimit(operation) {
		return nil, ErrInvalid
	}
	req, err := parseOperation(a.config, operation, contentType, body)
	if err != nil {
		return nil, err
	}
	var grant Claims
	if err = a.client.do(ctx, http.MethodPost, "/internal/v1/model-credentials/verify", "", map[string]string{"token": credential, "model": req.Model}, &grant); err != nil {
		return nil, err
	}
	if grant.ID == "" || grant.Audience != "llmrouter" || grant.ActorID == "" || grant.AccountID == "" || grant.OriginProductID == "" || grant.TaskID != task || !grant.ExpiresAt.After(time.Now()) || !slices.Contains(grant.Models, req.Model) {
		return nil, ErrInvalid
	}
	asset, mode, err := normalizeFunding(grant.Asset, grant.FundingMode)
	if err != nil || grant.BillingProfileVersion < 0 {
		return nil, ErrInvalid
	}
	digestData, _ := common.Marshal(struct {
		Version, Operation, Task, Actor, Account, Origin, Asset, Funding string
		Profile                                                          int
		Body                                                             string
	}{"platform-metered-v2", operation, task, grant.ActorID, grant.AccountID, grant.OriginProductID, asset, mode, grant.BillingProfileVersion, string(req.Canonical)})
	sum := sha256.Sum256(digestData)
	row := Call{CallID: id, Operation: operation, ActorID: grant.ActorID, AccountID: grant.AccountID, OriginProductID: grant.OriginProductID, CredentialID: grant.ID, TaskID: task, Model: req.Model, Digest: hex.EncodeToString(sum[:]), Asset: asset, FundingMode: mode, FundingProductID: "llmrouter", BillingProfileVersion: grant.BillingProfileVersion, Status: "RESERVING"}
	var old Call
	if err = a.db.WithContext(ctx).First(&old, "call_id = ?", id).Error; err == nil {
		return nil, compareCall(old, row)
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	var rates []Rate
	if err = a.client.do(ctx, http.MethodGet, "/internal/v1/rates", "", nil, &rates); err != nil {
		return nil, err
	}
	var chosen *Rate
	for i := range rates {
		r := &rates[i]
		if r.Model != req.Model || r.Semantics != "metered_v2" || r.Status != "published" || r.Metered == nil || r.Metered.Operation != operation {
			continue
		}
		ra, e := normalizeAsset(r.Asset)
		if e != nil {
			return nil, e
		}
		if ra != asset {
			continue
		}
		pin := a.config.OperationRateIDs[operation][asset][req.Model]
		if pin != "" && pin != r.ID {
			continue
		}
		if chosen != nil {
			return nil, ErrInvalid
		}
		chosen = r
	}
	if chosen == nil {
		return nil, ErrInvalid
	}
	if err = metering.ValidatePrice(*chosen.Metered); err != nil {
		return nil, ErrInvalid
	}
	if limits, ok := a.config.OperationLimits[operation][req.Model]; ok && limits.Policy != "" && limits.Policy != chosen.Metered.Policy {
		return nil, ErrInvalid
	}
	amount, err := meteredReserve(*chosen.Metered, req)
	if err != nil {
		return nil, err
	}
	snapshot, err := common.Marshal(chosen)
	if err != nil {
		return nil, err
	}
	row.RateSnapshot = string(snapshot)
	row.RateID = chosen.ID
	row.Amount = amount
	created := a.db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&row)
	if created.Error != nil {
		return nil, created.Error
	}
	if created.RowsAffected != 1 {
		if err = a.db.WithContext(ctx).First(&old, "call_id = ?", id).Error; err != nil {
			return nil, err
		}
		return nil, compareCall(old, row)
	}
	var remote RemoteCall
	err = a.client.do(ctx, http.MethodPost, "/internal/v1/calls/reserve", id, map[string]string{"credential": credential, "task_id": task, "call_id": id, "request_digest": row.Digest, "rate_id": row.RateID, "amount": amount}, &remote)
	if err != nil || !remote.matches(row) || remote.Status != "RESERVED" {
		_ = a.db.WithContext(ctx).Model(&Call{}).Where("call_id = ?", id).Update("status", "RESERVE_UNKNOWN").Error
		if err != nil {
			return nil, err
		}
		return nil, ErrPending
	}
	row.FundingSnapshot = remote.fundingSnapshot()
	saved := a.db.WithContext(ctx).Model(&Call{}).Where("call_id = ? AND status = ?", id, "RESERVING").Updates(map[string]any{"status": "RESERVED", "funding_snapshot": row.FundingSnapshot})
	if saved.Error != nil {
		return nil, saved.Error
	}
	if saved.RowsAffected != 1 {
		return nil, ErrPending
	}
	row.Status = "RESERVED"
	return &Session{adapter: a, call: row, safeFailure: true}, nil
}

func meteredReserve(price metering.Price, req operationRequest) (string, error) {
	u := metering.Usage{Version: "metered_v2", Operation: price.Operation, State: "confirmed", Source: "reservation_bound"}
	// Sum every tariff dimension at its maximum. This intentionally over-reserves
	// overlapping token dimensions and image tiers, so no rate choice under-reserves.
	for _, term := range price.Terms {
		n := req.Quantity
		if term.Key == "output" || term.Key == "reasoning" {
			n = req.Output
		}
		if n < 0 {
			return "", ErrInvalid
		}
		u.Measurements = append(u.Measurements, metering.Measurement{Key: term.Key, Unit: term.Unit, ProviderUnit: term.ProviderUnit, Quantity: metering.Quantity{Value: strconv.Itoa(n)}})
	}
	amount, err := metering.Calculate(price, u)
	if err != nil {
		return "", ErrInvalid
	}
	q, rem := new(big.Int), new(big.Int)
	q.QuoRem(amount.Num(), amount.Denom(), rem)
	if rem.Sign() > 0 {
		q.Add(q, big.NewInt(1))
	}
	if q.Sign() == 0 {
		q.SetInt64(1)
	}
	if q.BitLen() > 63 {
		return "", ErrInvalid
	}
	return q.String(), nil
}
func (s *Session) MeteredPrice() (metering.Price, bool) {
	var r Rate
	if s.call.Operation == "" || common.Unmarshal([]byte(s.call.RateSnapshot), &r) != nil || r.Metered == nil {
		return metering.Price{}, false
	}
	return *r.Metered, true
}
func (s *Session) CallID() string { return s.call.CallID }

// ObserveMetering persists evidence before settlement. Once conflicting evidence
// is seen, a later valid observation cannot erase that conflict.
func (s *Session) ObserveMetering(ctx context.Context, u metering.Usage) error {
	price, ok := s.MeteredPrice()
	if !ok {
		return ErrInvalid
	}
	if u.Version != "metered_v2" || u.Operation != price.Operation {
		return ErrInvalid
	}
	if u.State != "confirmed" && u.State != "missing" && u.State != "invalid" && u.State != "incomplete" {
		return ErrInvalid
	}
	if u.State == "confirmed" {
		if err := metering.ValidateUsage(price, u); err != nil {
			return ErrInvalid
		}
	}
	b, err := common.Marshal(u)
	if err != nil {
		return err
	}
	var conflict bool
	err = s.adapter.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row Call
		if e := tx.First(&row, "call_id = ?", s.call.CallID).Error; e != nil {
			return e
		}
		if row.EvidenceConflict {
			return ErrConflict
		}
		if row.Status != "UNKNOWN" {
			if row.Evidence == string(b) {
				return nil
			}
			return ErrPending
		}
		if row.Evidence != "" {
			var previous metering.Usage
			if common.Unmarshal([]byte(row.Evidence), &previous) != nil {
				return ErrInvalid
			}
			if previous.State == "confirmed" && !reflect.DeepEqual(previous, u) {
				conflict = true
				return tx.Model(&Call{}).Where("call_id = ? AND evidence = ?", row.CallID, row.Evidence).Update("evidence_conflict", true).Error
			}
		}
		saved := tx.Model(&Call{}).Where("call_id = ? AND evidence = ? AND evidence_conflict = ?", row.CallID, row.Evidence, false).Update("evidence", string(b))
		if saved.Error != nil {
			return saved.Error
		}
		if saved.RowsAffected != 1 {
			return ErrPending
		}
		return nil
	})
	if err != nil {
		return err
	}
	if conflict {
		return ErrConflict
	}
	return nil
}
func (s *Session) finishMetered(ctx context.Context) error {
	var row Call
	if err := s.adapter.db.WithContext(ctx).First(&row, "call_id = ?", s.call.CallID).Error; err != nil {
		return err
	}
	if row.Status == "SETTLED" || row.Status == "RELEASED" {
		return nil
	}
	if row.Status == "SETTLE_PENDING" || row.Status == "RELEASE_PENDING" {
		return s.adapter.deliver(ctx, row)
	}
	if row.EvidenceConflict {
		return ErrConflict
	}
	var usage metering.Usage
	status := "UNKNOWN"
	var intent any
	if row.Evidence != "" && common.Unmarshal([]byte(row.Evidence), &usage) == nil && usage.State == "confirmed" {
		price, ok := s.MeteredPrice()
		if !ok || metering.ValidateUsage(price, usage) != nil {
			return ErrInvalid
		}
		status = "SETTLE_PENDING"
		intent = struct {
			Usage        Usage  `json:"usage"`
			ProviderCost string `json:"provider_cost"`
		}{Usage{Metered: &usage}, ""}
	} else if row.Status == "RESERVED" && s.safeFailure {
		status = "RELEASE_PENDING"
		intent = map[string]string{"reason": "definitive_no_output_failure"}
	}
	if status == "UNKNOWN" {
		return nil
	}
	b, err := common.Marshal(intent)
	if err != nil {
		return err
	}
	saved := s.adapter.db.WithContext(ctx).Model(&Call{}).Where("call_id = ? AND status = ? AND evidence = ? AND evidence_conflict = ?", row.CallID, row.Status, row.Evidence, false).Updates(map[string]any{"status": status, "intent": string(b)})
	if saved.Error != nil {
		return saved.Error
	}
	if saved.RowsAffected != 1 {
		return ErrPending
	}
	row.Status = status
	row.Intent = string(b)
	_ = s.adapter.deliver(ctx, row)
	return nil
}

func (s *Session) BindTask(ctx context.Context, providerTaskID string) error {
	if providerTaskID == "" || len(providerTaskID) > 255 || s.call.Operation == "" {
		return ErrInvalid
	}
	saved := s.adapter.db.WithContext(ctx).Model(&Call{}).Where("call_id = ? AND status = ? AND (provider_task_id = ? OR provider_task_id = ?)", s.call.CallID, "UNKNOWN", "", providerTaskID).Update("provider_task_id", providerTaskID)
	if saved.Error != nil {
		return saved.Error
	}
	if saved.RowsAffected != 1 {
		return ErrConflict
	}
	return nil
}

// ObserveTask is service-only. It neither verifies nor persists a reusable model
// credential; workers must supply the durable exact provider task binding.
func (a *Adapter) ObserveTask(ctx context.Context, callID, providerTaskID string, u metering.Usage) error {
	var row Call
	if providerTaskID == "" {
		return ErrInvalid
	}
	if err := a.db.WithContext(ctx).Where("call_id = ? AND provider_task_id = ?", callID, providerTaskID).First(&row).Error; err != nil {
		return ErrInvalid
	}
	s := &Session{adapter: a, call: row}
	if err := s.ObserveMetering(ctx, u); err != nil {
		return err
	}
	return s.Finish(ctx)
}

// Task requires a fresh capability with the original actor/account/origin/task.
func (a *Adapter) Task(ctx context.Context, credential, task, callID string) (Call, error) {
	if _, err := a.Receipt(ctx, credential, task, callID); err != nil {
		return Call{}, err
	}
	var row Call
	err := a.db.WithContext(ctx).First(&row, "call_id = ?", callID).Error
	return row, err
}

// ReleaseTask is callable only with the immutable provider task binding and a
// supplier-confirmed no-output terminal. It writes RELEASE_PENDING intent first.
func (a *Adapter) ReleaseTask(ctx context.Context, callID, providerTaskID string) error {
	if providerTaskID == "" {
		return ErrInvalid
	}
	var row Call
	if err := a.db.WithContext(ctx).Where("call_id = ? AND provider_task_id = ?", callID, providerTaskID).First(&row).Error; err != nil {
		return ErrInvalid
	}
	if row.EvidenceConflict {
		return ErrConflict
	}
	var evidence metering.Usage
	if row.Evidence != "" {
		if common.UnmarshalJsonStr(row.Evidence, &evidence) != nil {
			return ErrInvalid
		}
		if evidence.State == "confirmed" {
			return ErrConflict
		}
	}
	if row.Status == "RELEASED" {
		return nil
	}
	if row.Status != "UNKNOWN" && row.Status != "RESERVED" {
		return ErrPending
	}
	b, _ := common.Marshal(map[string]string{"reason": "provider_confirmed_no_output"})
	saved := a.db.WithContext(ctx).Model(&Call{}).Where("call_id = ? AND provider_task_id = ? AND status IN ? AND evidence = ? AND evidence_conflict = ?", callID, providerTaskID, []string{"UNKNOWN", "RESERVED"}, row.Evidence, false).Updates(map[string]any{"status": "RELEASE_PENDING", "intent": string(b)})
	if saved.Error != nil {
		return saved.Error
	}
	if saved.RowsAffected != 1 {
		return ErrPending
	}
	row.Status = "RELEASE_PENDING"
	row.Intent = string(b)
	return a.deliver(ctx, row)
}
