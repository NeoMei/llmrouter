package model

import (
	"context"
	"errors"
	"github.com/QuantumNous/new-api/internal/platformaccount"
	"gorm.io/gorm"
	"time"
)

type ProductLegacyFundingLease struct {
	ID                   string `gorm:"primaryKey;size:128"`
	UserID               int    `gorm:"index"`
	State                string `gorm:"size:24;index"`
	CreatedAt, UpdatedAt int64
}

// The user-row lock is also held by Import. A legacy call either obtains its
// durable lease before cutover, or is rejected before taking any local funds.
func BeginProductLegacyFundingLease(ctx context.Context, id string, userID int) error {
	if DB == nil {
		return ErrProductFundingPending
	}
	if !DB.Migrator().HasTable(&ProductLegacyFundingLease{}) {
		if !platformaccount.RuntimeEnabled() {
			return nil
		}
		return ErrProductFundingPending
	}
	if id == "" || len(id) > 128 || userID <= 0 {
		return ErrProductFundingConflict
	}
	return DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var user User
		if e := lockForUpdate(tx).First(&user, userID).Error; e != nil {
			return e
		}
		var account ProductFundingAccount
		e := tx.First(&account, "user_id = ?", userID).Error
		if e == nil && account.State != "local" {
			return ErrProductFundingPending
		}
		if e != nil && !errors.Is(e, gorm.ErrRecordNotFound) {
			return e
		}
		var old ProductLegacyFundingLease
		e = tx.First(&old, "id = ?", id).Error
		if e == nil {
			if old.UserID != userID || old.State != "active" {
				return ErrProductFundingConflict
			}
			return nil
		}
		if !errors.Is(e, gorm.ErrRecordNotFound) {
			return e
		}
		return tx.Create(&ProductLegacyFundingLease{ID: id, UserID: userID, State: "active", CreatedAt: time.Now().Unix(), UpdatedAt: time.Now().Unix()}).Error
	})
}
func FinishProductLegacyFundingLease(ctx context.Context, id, state string) error {
	if DB == nil {
		return ErrProductFundingPending
	}
	if !DB.Migrator().HasTable(&ProductLegacyFundingLease{}) {
		if !platformaccount.RuntimeEnabled() {
			return nil
		}
		return ErrProductFundingPending
	}
	if state != "settled" && state != "refunded" {
		return ErrProductFundingConflict
	}
	return DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var lease ProductLegacyFundingLease
		if e := lockForUpdate(tx).First(&lease, "id = ?", id).Error; e != nil {
			return e
		}
		if lease.State == state {
			return nil
		}
		if lease.State != "active" {
			return ErrProductFundingConflict
		}
		return tx.Model(&lease).Updates(map[string]any{"state": state, "updated_at": time.Now().Unix()}).Error
	})
}

func productLegacyDrained(tx *gorm.DB, userID int) error {
	checks := []struct {
		model any
		query string
		args  []any
	}{
		{&ProductLegacyFundingLease{}, "user_id = ? AND state NOT IN ?", []any{userID, []string{"settled", "refunded"}}},
		{&NativeBilling{}, "user_id = ? AND platform = ? AND state NOT IN ?", []any{userID, false, []string{"settled", "released", "rejected"}}},
		{&OCRJob{}, "user_id = ? AND funding NOT IN ? AND billing_state NOT IN ?", []any{userID, []string{"platform", "unihub"}, []string{"settled", "refunded", "rejected"}}},
		{&Task{}, "user_id = ? AND (product_call_id IS NULL OR product_call_id = '') AND (funding_owner IS NULL OR funding_owner NOT IN ?) AND (status NOT IN ? OR (status = ? AND quota > 0))", []any{userID, []string{"native", "platform"}, []string{TaskStatusSuccess, TaskStatusFailure}, TaskStatusFailure}},
		{&Midjourney{}, "user_id = ? AND (product_call_id IS NULL OR product_call_id = '') AND (status NOT IN ? OR (status = ? AND quota > 0))", []any{userID, []string{"SUCCESS", "FAILURE"}, "FAILURE"}},
	}
	for _, check := range checks {
		if !tx.Migrator().HasTable(check.model) {
			continue
		}
		var n int64
		if e := tx.Model(check.model).Where(check.query, check.args...).Count(&n).Error; e != nil {
			return e
		}
		if n > 0 {
			return ErrProductFundingPending
		}
	}
	return nil
}
