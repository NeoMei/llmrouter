package model

import (
	"context"
	"errors"
	"math/big"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/metering"
	"github.com/go-redis/redis/v8"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// NativeAdmission is trusted service input, never a client-selected funding owner.
// Reservation is a bounded estimate and is stored separately from final evidence.
type NativeAdmission struct {
	CallID                     string
	UserID, TokenID, ChannelID int
	Model, Group, Preference   string
	Price                      metering.Price
	GroupRatio, QuotaPerUnit   string
	Reservation                metering.Usage
	Platform                   bool
}

// NativeBilling keeps the immutable contract and verified evidence separate from
// the OCR financial ledger reused underneath. All JSON columns are portable TEXT.
type NativeBilling struct {
	LastObservation           string `gorm:"type:text" json:"-"`
	ConflictEvidence          string `gorm:"type:text" json:"-"`
	AdminCommand              string `gorm:"type:text" json:"-"`
	ID                        string `gorm:"primaryKey;size:36"`
	UserID                    int    `gorm:"uniqueIndex:idx_native_call,priority:1"`
	CallID                    string `gorm:"size:128;uniqueIndex:idx_native_call,priority:2"`
	AttemptID                 string `gorm:"size:36"`
	Contract                  string `gorm:"type:text" json:"-"`
	Evidence                  string `gorm:"type:text" json:"-"`
	State                     string `gorm:"size:32;index"`
	Reason                    string `gorm:"size:64"`
	Dispatched                bool
	Platform                  bool
	ReservedQuota, FinalQuota int
	Outcome                   string `gorm:"size:24"`
	AdminActionID             string `gorm:"size:128"`
	AdminActor                int
	AdminReason               string `gorm:"type:text"`
	CreatedAt, UpdatedAt      int64
}
type NativeBillingStore struct{ ledger *OCRBillingStore }

func NewNativeBillingStore(db *gorm.DB, cache *redis.Client) *NativeBillingStore {
	return &NativeBillingStore{ledger: NewOCRBillingStore(db, cache)}
}
func (s *NativeBillingStore) Close() error { return s.ledger.Close() }
func (s *NativeBillingStore) Inspect(ctx context.Context, id string) (NativeBilling, error) {
	var n NativeBilling
	err := s.ledger.db.WithContext(ctx).First(&n, "id = ?", id).Error
	return n, err
}

// NativeQuota performs exact rational aggregation and rounds once. Conversion
// to the bounded database quota still uses the shared checked boundary helper.
func NativeQuota(a NativeAdmission, u metering.Usage, reserve bool) (int, *common.QuotaClamp, error) {
	// Reservation vectors are intentionally incomplete; they are bounded
	// estimates and never eligible as final evidence.
	calcUsage := u
	if reserve && calcUsage.State != "confirmed" {
		calcUsage.State = "confirmed"
	}
	cost, err := metering.Calculate(a.Price, calcUsage)
	if err != nil {
		return 0, nil, err
	}
	for index, raw := range []string{a.GroupRatio, a.QuotaPerUnit} {
		if len(raw) == 0 || len(raw) > 128 {
			return 0, nil, ErrOCRConflict
		}
		v, ok := new(big.Rat).SetString(raw)
		if !ok || v.Sign() < 0 {
			return 0, nil, ErrOCRConflict
		}
		if index == 1 && v.Sign() == 0 {
			return 0, nil, ErrOCRConflict
		}
		cost.Mul(cost, v)
	}
	whole, rem := new(big.Int), new(big.Int)
	whole.QuoRem(cost.Num(), cost.Denom(), rem)
	if (reserve && rem.Sign() > 0) || (!reserve && new(big.Int).Lsh(rem, 1).Cmp(cost.Denom()) >= 0) {
		whole.Add(whole, big.NewInt(1))
	}
	q, clamp := common.QuotaFromDecimalChecked(decimal.NewFromBigInt(whole, 0))
	return q, clamp, nil
}
func (s *NativeBillingStore) Begin(ctx context.Context, a NativeAdmission) (NativeBilling, error) {
	if a.UserID <= 0 || a.TokenID <= 0 || a.ChannelID <= 0 || strings.TrimSpace(a.CallID) == "" || len(a.CallID) > 128 || len(a.Model) == 0 || len(a.Model) > 128 || len(a.Group) > 128 {
		return NativeBilling{}, ErrOCRConflict
	}
	a.Preference = common.NormalizeBillingPreference(a.Preference)
	q, clamp, err := NativeQuota(a, a.Reservation, true)
	if err != nil {
		return NativeBilling{}, err
	}
	if clamp != nil {
		_ = s.ledger.RecordAdmissionRejection(ctx, a.UserID, a.TokenID, a.Model, map[string]any{"admin_info": map[string]any{"quota_saturation": clamp.AuditMap()}})
		return NativeBilling{}, clamp
	}
	raw, err := common.Marshal(a)
	if err != nil {
		return NativeBilling{}, err
	}
	now := time.Now().Unix()
	n := NativeBilling{ID: uuid.NewString(), UserID: a.UserID, CallID: a.CallID, AttemptID: uuid.NewString(), Contract: string(raw), State: "reserving", Platform: a.Platform, ReservedQuota: q, CreatedAt: now, UpdatedAt: now}
	err = s.ledger.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&n).Error; err != nil {
			return err
		}
		var existing NativeBilling
		if err := lockForUpdate(tx).Where("user_id = ? AND call_id = ?", a.UserID, a.CallID).First(&existing).Error; err != nil {
			return err
		}
		if existing.Contract != string(raw) {
			return ErrOCRConflict
		}
		n = existing
		if a.Platform {
			return nil
		}
		j := OCRJob{ID: n.ID, UserID: a.UserID, TokenID: a.TokenID, ChannelID: a.ChannelID, IdempotencyKey: "native:" + n.ID, Contract: string(raw), Model: a.Model, BillingGroup: a.Group, Preference: a.Preference, Quota: q, Redis: s.ledger.cache != nil, BillingState: "pending", SubmissionState: "native", CreatedAt: now, UpdatedAt: now}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&j).Error; err != nil {
			return err
		}
		for _, kind := range []string{"wallet", "subscription", "token"} {
			owner := a.UserID
			if kind == "token" {
				owner = a.TokenID
			}
			op := OCROperation{ID: uuid.NewString(), JobID: n.ID, Kind: kind, Phase: "reserve", Amount: q, OwnerID: owner, State: "prepared"}
			if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&op).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return n, err
	}
	if n.State != "reserving" {
		return n, nil
	}
	if !a.Platform {
		if _, err = s.ledger.Reserve(ctx, n.ID); err != nil {
			return n, err
		}
	}
	err = s.ledger.db.WithContext(ctx).Model(&NativeBilling{}).Where("id = ? AND state = ?", n.ID, "reserving").Updates(map[string]any{"state": "reserved", "updated_at": time.Now().Unix()}).Error
	if err != nil {
		return n, err
	}
	return s.Inspect(ctx, n.ID)
}

// MarkDispatched must commit before the first upstream send. An uncertain send
// is never made eligible for a second attempt by recovery.
func (s *NativeBillingStore) MarkDispatched(ctx context.Context, id, attempt string) error {
	return s.ledger.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var n NativeBilling
		if err := lockForUpdate(tx).First(&n, "id = ?", id).Error; err != nil {
			return err
		}
		if n.AttemptID != attempt || n.State != "reserved" || n.Dispatched {
			return ErrOCRConflict
		}
		return tx.Model(&n).Updates(map[string]any{"dispatched": true, "state": "pending_measurement"}).Error
	})
}
func (s *NativeBillingStore) RecordUsage(ctx context.Context, id string, u metering.Usage) error {
	var conflict bool
	err := s.ledger.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var n NativeBilling
		if err := lockForUpdate(tx).First(&n, "id = ?", id).Error; err != nil {
			return err
		}
		var a NativeAdmission
		if err := common.UnmarshalJsonStr(n.Contract, &a); err != nil {
			return err
		}
		raw, err := common.Marshal(u)
		if err != nil {
			return err
		}
		if n.Evidence != "" && n.Evidence != string(raw) {
			conflict = true
			if n.State == "settled" || n.State == "released" {
				return tx.Model(&n).Updates(map[string]any{"conflict_evidence": string(raw), "reason": "conflicting_evidence", "updated_at": time.Now().Unix()}).Error
			}
			return tx.Model(&n).Updates(map[string]any{"state": "pending_measurement", "reason": "conflicting_evidence", "conflict_evidence": string(raw), "updated_at": time.Now().Unix()}).Error
		}
		if n.Outcome != "" {
			if n.Evidence == string(raw) {
				return nil
			}
			return ErrOCRConflict
		}
		if metering.ValidateUsage(a.Price, u) != nil {
			return tx.Model(&n).Updates(map[string]any{"state": "pending_measurement", "reason": "unconfirmed_evidence", "last_observation": string(raw), "updated_at": time.Now().Unix()}).Error
		}
		// Supplier-defined character/provider units use explicit server ceilings,
		// not a token estimate. Preserve over-cap receipts for reconciliation
		// instead of silently charging beyond the frozen admission contract.
		if (a.Price.Operation == "speech" && a.Price.Policy == "provider_reported_v1") || (a.Price.Operation == "video" && a.Price.Terms[0].Unit == "provider_unit") {
			for _, measurement := range u.Measurements {
				for _, reserved := range a.Reservation.Measurements {
					if measurement.Key != reserved.Key {
						continue
					}
					actual, actualErr := decimal.NewFromString(measurement.Quantity.Value)
					limit, limitErr := decimal.NewFromString(reserved.Quantity.Value)
					if actualErr != nil || limitErr != nil || actual.Shift(-int32(measurement.Quantity.Scale)).GreaterThan(limit.Shift(-int32(reserved.Quantity.Scale))) {
						return tx.Model(&n).Updates(map[string]any{"state": "pending_measurement", "reason": "reservation_limit_exceeded", "last_observation": string(raw), "updated_at": time.Now().Unix()}).Error
					}
				}
			}
		}
		if n.Reason == "conflicting_evidence" {
			conflict = true
			return nil
		}
		return tx.Model(&n).Updates(map[string]any{"evidence": string(raw), "state": "measured", "reason": "", "updated_at": time.Now().Unix()}).Error
	})
	if err == nil && conflict {
		return ErrOCRConflict
	}
	return err
}

// Finish first persists the decision and component intents, then replays only
// those operation IDs. Missing/invalid evidence leaves the reservation untouched.
func (s *NativeBillingStore) Finish(ctx context.Context, id, outcome string) (NativeBilling, error) {
	if outcome != "measured" && outcome != "no_output" && outcome != "pre_dispatch_failure" && outcome != "loss_release" {
		return NativeBilling{}, ErrOCRConflict
	}
	err := s.ledger.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var n NativeBilling
		if err := lockForUpdate(tx).First(&n, "id = ?", id).Error; err != nil {
			return err
		}
		if n.Platform {
			return errors.New("native platform evidence must be settled by platform authority")
		}
		if n.Outcome != "" {
			if n.Outcome != outcome {
				return ErrOCRConflict
			}
			return nil
		}
		if n.State == "reserving" {
			return ErrOCRPending
		}
		if outcome == "pre_dispatch_failure" && n.Dispatched {
			return ErrOCRConflict
		}
		if outcome == "loss_release" && n.AdminActionID == "" {
			return ErrOCRConflict
		}
		var a NativeAdmission
		if err := common.UnmarshalJsonStr(n.Contract, &a); err != nil {
			return err
		}
		q := 0
		if outcome == "measured" {
			if n.State != "measured" || n.Evidence == "" {
				return ErrOCRPending
			}
			var u metering.Usage
			if err := common.UnmarshalJsonStr(n.Evidence, &u); err != nil {
				return err
			}
			var clamp *common.QuotaClamp
			var err error
			q, clamp, err = NativeQuota(a, u, false)
			if err != nil {
				return err
			}
			if clamp != nil {
				return clamp
			}
		}
		j, _, err := ocrLockJobForFunding(tx, id)
		if err != nil {
			return err
		}
		if j.Redis != (s.ledger.cache != nil) {
			return ErrOCRReconciliation
		}
		if j.BillingState != "reserved" {
			return ErrOCRPending
		}
		phase := "native_credit"
		delta := n.ReservedQuota - q
		if delta < 0 {
			phase = "native_debit"
			delta = -delta
		}
		for _, kind := range []string{j.Funding, "token"} {
			reserve, err := ocrOperation(tx, id, kind, "reserve")
			if err != nil {
				return err
			}
			if reserve.State != "applied" {
				return ErrOCRPending
			}
			op := OCROperation{ID: uuid.NewString(), JobID: id, Kind: kind, Phase: phase, Amount: delta, OwnerID: reserve.OwnerID, State: "prepared", ReserveID: reserve.ID, SubscriptionGeneration: reserve.SubscriptionGeneration}
			if err := tx.Create(&op).Error; err != nil {
				return err
			}
		}
		n.Outcome = outcome
		n.FinalQuota = q
		n.State = "settlement_pending"
		n.UpdatedAt = time.Now().Unix()
		return tx.Save(&n).Error
	})
	if err != nil {
		n, _ := s.Inspect(ctx, id)
		return n, err
	}
	return s.recoverSettlement(ctx, id)
}
func (s *NativeBillingStore) recoverSettlement(ctx context.Context, id string) (NativeBilling, error) {
	n, err := s.Inspect(ctx, id)
	if err != nil || n.State == "settled" || n.State == "released" {
		return n, err
	}
	if n.State != "settlement_pending" {
		return n, ErrOCRPending
	}
	snap, err := s.ledger.Inspect(ctx, id)
	if err != nil {
		return n, err
	}
	if snap.Job.Redis != (s.ledger.cache != nil) {
		return n, ErrOCRReconciliation
	}
	if snap.Job.Redis {
		if err = s.ledger.validateCacheReceipts(ctx, id); err != nil {
			return n, err
		}
		for _, op := range snap.Operations {
			if (op.Phase == "native_credit" || op.Phase == "native_debit") && op.Kind != "subscription" {
				if err = s.ledger.applyOCRCacheOperation(ctx, id, op.Kind, op.Phase); err != nil {
					return n, err
				}
			}
		}
	}
	err = s.ledger.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var current NativeBilling
		if err := lockForUpdate(tx).First(&current, "id = ?", id).Error; err != nil {
			return err
		}
		if current.State == "settled" || current.State == "released" {
			return nil
		}
		j, _, err := ocrLockJobForFunding(tx, id)
		if err != nil {
			return err
		}
		var ops []OCROperation
		if err := lockForUpdate(tx).Where("job_id = ? AND phase IN ?", id, []string{"native_credit", "native_debit"}).Order("kind").Find(&ops).Error; err != nil {
			return err
		}
		if len(ops) != 2 {
			return ErrOCRPending
		}
		for i := range ops {
			op := &ops[i]
			if op.State == "applied" {
				continue
			}
			if op.Kind == "subscription" {
				if op.Phase == "native_credit" {
					if err := refundOCRSubscriptionTx(tx, op); err != nil {
						return err
					}
				} else {
					var sub UserSubscription
					if err := lockForUpdate(tx).First(&sub, op.OwnerID).Error; err != nil {
						return err
					}
					if sub.ResetGeneration != op.SubscriptionGeneration {
						return ErrOCRReconciliation
					}
					if sub.AmountTotal > 0 && sub.AmountTotal-sub.AmountUsed < int64(op.Amount) {
						return ErrOCRInsufficient
					}
					if sub.AmountUsed > int64(common.MaxQuota)-int64(op.Amount) {
						return ErrOCRReconciliation
					}
					if err := tx.Model(&sub).Update("amount_used", gorm.Expr("amount_used + ?", op.Amount)).Error; err != nil {
						return err
					}
					if err := ocrApplied(tx, op); err != nil {
						return err
					}
				}
			} else {
				if j.Redis {
					return ErrOCRPending
				}
				if err := applyOCRQuotaTx(tx, j, op, false); err != nil {
					return err
				}
			}
		}
		// All component receipts are committed with the terminal counters and audit.
		j.Quota = current.FinalQuota
		j.Decision = "success"
		if current.Outcome != "measured" {
			j.Decision = "cancel"
		}
		if err := ocrFinalizeTx(tx, j); err != nil {
			return err
		}
		current.State = "settled"
		if current.Outcome != "measured" {
			current.State = "released"
		}
		current.UpdatedAt = time.Now().Unix()
		return tx.Save(&current).Error
	})
	if err != nil {
		return n, err
	}
	return s.Inspect(ctx, id)
}

// Recover is bounded and never dispatches provider work or invents usage.
func (s *NativeBillingStore) Recover(ctx context.Context, limit int) error {
	if limit < 1 || limit > 100 {
		return ErrOCRConflict
	}
	var rows []NativeBilling
	if err := s.ledger.db.WithContext(ctx).Where("platform = ?", false).Where("state IN ? OR (admin_action_id <> ? AND outcome = ?)", []string{"reserving", "measured", "settlement_pending"}, "", "").Order("created_at,id").Limit(limit).Find(&rows).Error; err != nil {
		return err
	}
	var first error
	for _, n := range rows {
		var err error
		if n.State == "reserving" {
			var a NativeAdmission
			err = common.UnmarshalJsonStr(n.Contract, &a)
			if err == nil {
				_, err = s.Begin(ctx, a)
			}

		} else if n.AdminActionID != "" && n.Outcome == "" {
			var command struct {
				Action string
				Usage  *metering.Usage
				Reason string
				Actor  int
			}
			if e := common.UnmarshalJsonStr(n.AdminCommand, &command); e != nil {
				err = e
			} else {
				_, err = s.ResolveMeasurement(ctx, n.ID, n.AdminActionID, command.Actor, command.Action, command.Usage, command.Reason)
			}
		} else if n.State == "measured" {
			_, err = s.Finish(ctx, n.ID, "measured")
		} else {
			_, err = s.recoverSettlement(ctx, n.ID)
		}
		if first == nil {
			first = err
		}
	}
	return first
}

// ResolveMeasurement is an audited, idempotent operator action. It never
// guesses a quantity: confirmed requires a complete Usage, no_output releases,
// and loss_release records platform/operator loss without changing evidence.
func (s *NativeBillingStore) ResolveMeasurement(ctx context.Context, id, actionID string, actor int, action string, usage *metering.Usage, reason string) (NativeBilling, error) {
	if strings.TrimSpace(actionID) == "" || len(actionID) > 128 || actor <= 0 || strings.TrimSpace(reason) == "" || len(reason) > 1024 {
		return NativeBilling{}, ErrOCRConflict
	}
	if action != "confirm_measurement" && action != "confirm_no_output" && action != "operator_loss_release" {
		return NativeBilling{}, ErrOCRConflict
	}
	command, err := common.Marshal(struct {
		Action string
		Usage  *metering.Usage
		Reason string
		Actor  int
	}{action, usage, reason, actor})
	if err != nil {
		return NativeBilling{}, err
	}
	err = s.ledger.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var n NativeBilling
		if err := lockForUpdate(tx).First(&n, "id = ?", id).Error; err != nil {
			return err
		}
		if n.Platform {
			return ErrOCRConflict
		}
		if n.AdminActionID != "" {
			if n.AdminActionID != actionID || n.AdminCommand != string(command) {
				return ErrOCRConflict
			}
			return nil
		}
		if n.Outcome != "" {
			return ErrOCRConflict
		}
		if action == "confirm_measurement" {
			if usage == nil {
				return ErrOCRConflict
			}
			var a NativeAdmission
			if err := common.UnmarshalJsonStr(n.Contract, &a); err != nil {
				return err
			}
			if err := metering.ValidateUsage(a.Price, *usage); err != nil {
				return err
			}
			raw, err := common.Marshal(usage)
			if err != nil {
				return err
			}
			n.Evidence = string(raw)
			n.State = "measured"
			n.Reason = "operator_confirmed"
		}
		n.AdminActionID = actionID
		n.AdminActor = actor
		n.AdminReason = reason
		n.AdminCommand = string(command)
		n.UpdatedAt = time.Now().Unix()
		return tx.Save(&n).Error
	})
	if err != nil {
		n, _ := s.Inspect(ctx, id)
		return n, err
	}
	outcome := "no_output"
	if action == "operator_loss_release" {
		outcome = "loss_release"
	}
	if action == "confirm_measurement" {
		outcome = "measured"
	}
	return s.Finish(ctx, id, outcome)
}

func (s *NativeBillingStore) ProjectAuditLogs(ctx context.Context, logDB *gorm.DB, limit int) error {
	if limit < 1 || limit > 100 {
		return ErrOCRConflict
	}
	query := s.ledger.db.WithContext(ctx).Where("projected_at = ?", 0).Where("job_id IN (?)", s.ledger.db.Model(&NativeBilling{}).Select("id"))
	return s.ledger.projectAudit(ctx, query, limit, func(ctx context.Context, event OCRAuditEvent) error {
		return s.ledger.DeliverAuditLog(ctx, logDB, event)
	})
}
