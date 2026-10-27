package model

import (
	"context"
	"strconv"
	"time"

	"errors"
	userkit "github.com/NeoMei/UniHub/packages/userkit-go"
	"github.com/QuantumNous/new-api/common"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func (s *OCRBillingStore) reserveManagedProductFunding(ctx context.Context, j OCRJob) (OCRJob, error) {
	if j.Funding == "subscription" && j.BillingState == "reserved" {
		return j, nil
	}
	if j.Preference == "subscription_first" || j.Preference == "subscription_only" {
		out, selected, err := s.reserveManagedSubscription(ctx, j)
		if err != nil || selected {
			return out, err
		}
	}
	out, err := s.reserveProductFunding(ctx, j)
	var failure *userkit.RuntimeError
	if err != nil && j.Preference == "wallet_first" && errors.As(err, &failure) && failure.Status == 402 {
		fallback, selected, e := s.reserveManagedSubscription(ctx, j)
		if e != nil {
			return fallback, e
		}
		if selected {
			return fallback, nil
		}
	}
	return out, err
}

// A purchased subscription is a product entitlement, not another cash wallet.
// Consuming it keeps original limits and does not debit the central wallet again.
func (s *OCRBillingStore) reserveManagedSubscription(ctx context.Context, j OCRJob) (OCRJob, bool, error) {
	store, _, err := ProductFundingStoreForUser(ctx, j.UserID)
	if err != nil {
		return j, false, err
	}
	account, err := store.Account(ctx, j.UserID)
	if err != nil {
		return j, false, err
	}
	selected := false
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		current, subs, err := ocrLockJobForFunding(tx, j.ID)
		if err != nil {
			return err
		}
		if current.BillingState == "reserved" && current.Funding == "subscription" {
			selected = true
			return nil
		}
		if current.BillingState != "pending" {
			return ErrOCRConflict
		}
		selected, err = reserveOCRSubscriptionTx(tx, current, subs)
		if err != nil {
			return err
		}
		if !selected {
			return nil
		}
		op, err := ocrOperation(tx, j.ID, "token", "reserve")
		if err != nil {
			return err
		}
		if err = applyOCRQuotaTx(tx, current, op, false); err != nil {
			return err
		}
		current.Redis = false
		current.CentralAccountID = account.AccountID
		current.BillingState = "reserved"
		return ocrSaveJob(tx, current)
	})
	if err != nil {
		return j, false, err
	}
	if !selected {
		return j, false, nil
	}
	_ = InvalidateUserTokensCache(j.UserID)
	snap, err := s.Inspect(ctx, j.ID)
	return snap.Job, true, err
}

// The existing OCR/native journal continues to own provider dispatch, evidence,
// counters and audit. Only its financial component is delegated to UniHub.
func (s *OCRBillingStore) reserveProductFunding(ctx context.Context, j OCRJob) (OCRJob, error) {
	store, managed, err := ProductFundingStoreForUser(ctx, j.UserID)
	if err != nil {
		return j, err
	}
	if !managed {
		return j, ErrProductFundingConflict
	}
	operation := "ocr"
	var native NativeAdmission
	if common.UnmarshalJsonStr(j.Contract, &native) == nil && native.Price.Operation != "" {
		operation = native.Price.Operation
	}
	_, err = store.Reserve(ctx, ProductFundingAdmission{CallID: j.ID, UserID: j.UserID, TokenID: j.TokenID, Model: j.Model, Operation: operation, PriceSnapshot: j.Contract, Quota: j.Quota, ExternalCounters: true})
	if err != nil {
		return j, err
	}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		current, err := ocrLockJob(tx, j.ID)
		if err != nil {
			return err
		}
		if current.BillingState == "reserved" && current.Funding == "unihub" {
			return nil
		}
		if current.BillingState != "pending" || current.Decision != "" {
			return ErrOCRConflict
		}
		op := OCROperation{ID: uuid.NewString(), JobID: j.ID, Kind: "unihub", Phase: "reserve", Amount: j.Quota, OwnerID: j.UserID, State: "applied", SQLAppliedAt: time.Now().Unix()}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&op).Error; err != nil {
			return err
		}
		if err := tx.Model(&OCROperation{}).Where("job_id = ? AND kind = ? AND phase = ?", j.ID, "token", "reserve").Updates(map[string]any{"state": "applied", "sql_applied_at": time.Now().Unix()}).Error; err != nil {
			return err
		}
		current.Funding = "unihub"
		current.Redis = false
		current.BillingState = "reserved"
		return ocrSaveJob(tx, current)
	})
	if err != nil {
		return j, err
	}
	snap, err := s.Inspect(ctx, j.ID)
	return snap.Job, err
}

func (s *OCRBillingStore) finishProductFunding(ctx context.Context, id string, actual int, usage, outcome string) (OCRJob, error) {
	journal, err := s.Inspect(ctx, id)
	if err != nil {
		return journal.Job, err
	}
	j := journal.Job
	if j.Funding != "unihub" {
		return j, ErrOCRConflict
	}
	if j.Decision != "" && j.Decision != outcome {
		return j, ErrOCRConflict
	}
	if j.BillingState == "settled" || j.BillingState == "refunded" {
		return j, nil
	}
	store, _, err := ProductFundingStoreForUser(ctx, j.UserID)
	if err != nil {
		return j, err
	}
	if outcome == "success" {
		err = store.Settle(ctx, id, actual, usage)
	} else {
		err = store.Release(ctx, id, "original_product_"+outcome)
	}
	if err != nil {
		return j, err
	}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		current, err := ocrLockJob(tx, id)
		if err != nil {
			return err
		}
		if current.BillingState == "settled" || current.BillingState == "refunded" {
			return nil
		}
		if current.Decision != "" && current.Decision != outcome {
			return ErrOCRConflict
		}
		current.Quota = actual
		current.Decision = outcome
		if err := tx.Model(&OCROperation{}).Where("job_id = ? AND phase IN ?", id, []string{"native_credit", "native_debit", "refund"}).Updates(map[string]any{"state": "applied", "sql_applied_at": time.Now().Unix()}).Error; err != nil {
			return err
		}
		return ocrFinalizeTx(tx, current)
	})
	if err != nil {
		return j, err
	}
	snap, err := s.Inspect(ctx, id)
	return snap.Job, err
}

func (s *NativeBillingStore) recoverProductSettlement(ctx context.Context, n NativeBilling) (NativeBilling, error) {
	outcome := "cancel"
	if n.Outcome == "measured" {
		outcome = "success"
	}
	if _, err := s.ledger.finishProductFunding(ctx, n.ID, n.FinalQuota, n.Evidence, outcome); err != nil {
		return n, err
	}
	terminal := "released"
	if outcome == "success" {
		terminal = "settled"
	}
	if err := s.ledger.db.WithContext(ctx).Model(&NativeBilling{}).Where("id = ? AND state = ?", n.ID, "settlement_pending").Updates(map[string]any{"state": terminal, "updated_at": time.Now().Unix()}).Error; err != nil {
		return n, err
	}
	return s.Inspect(ctx, n.ID)
}

func (s *OCRBillingStore) finishProductOCR(ctx context.Context, j OCRJob, outcome string) (OCRJob, error) {
	if j.Decision != "" && j.Decision != outcome {
		return j, ErrOCRConflict
	}
	if outcome == "success" && (j.SubmissionState != "submitted" || j.ProviderState != "done") {
		return j, ErrOCRConflict
	}
	if outcome == "failure" && j.ProviderState != "failed" && j.SubmissionState != "rejected" {
		return j, ErrOCRConflict
	}
	if outcome == "cancel" && j.SubmissionState != "unclaimed" {
		return j, ErrOCRConflict
	}
	actual := 0
	if outcome == "success" {
		actual = j.Quota
	}
	usage := `{"operation":"ocr","quota":` + strconv.Itoa(actual) + `}`
	return s.finishProductFunding(ctx, j.ID, actual, usage, outcome)
}
