package model

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ErrWalletRefundConflict means a refund receipt with the same identity was
// already created with a different payload and must be reconciled manually.
var ErrWalletRefundConflict = errors.New("wallet refund receipt conflict")

// WalletRefundReceipt makes each legacy refund leg durable and idempotent.
// The quota mutation and the applied state commit in one transaction, so a
// process failure cannot leave an unrecorded or repeated wallet credit.
type WalletRefundReceipt struct {
	ID        string `gorm:"primaryKey;size:128"`
	RequestID string `gorm:"size:128;index"`
	LeaseID   string `gorm:"size:128;index"`
	UserID    int    `gorm:"index"`
	TokenID   int    `gorm:"index"`
	RefID     int    `gorm:"index"` // subscription id for subscription legs
	Amount    int
	Leg       string `gorm:"size:32"`
	State     string `gorm:"size:16;index"`
	CreatedAt int64
	UpdatedAt int64
}

func WalletRefundReceiptID(requestID, leg string, amount int) string {
	sum := sha256.Sum256([]byte("wallet-refund:" + requestID + ":" + leg + ":" + strconv.Itoa(amount)))
	return hex.EncodeToString(sum[:])
}

func walletRefundTableMissing() error {
	if DB == nil {
		return ErrProductFundingPending
	}
	if !DB.Migrator().HasTable(&WalletRefundReceipt{}) {
		// The receipt table is owned by the normal migrations, but a refund may
		// start in a process or test fixture whose schema predates it. Creating
		// the small append-only table here keeps the refund durable.
		if err := DB.AutoMigrate(&WalletRefundReceipt{}); err != nil {
			return ErrProductFundingPending
		}
	}
	return nil
}

// BeginWalletRefund creates the pending receipt before any quota is restored.
// A zero amount has nothing to refund and does not create a receipt.
func BeginWalletRefund(ctx context.Context, requestID, leaseID string, userID, tokenID, refID, amount int, leg string) (string, error) {
	if amount <= 0 {
		return "", nil
	}
	if err := walletRefundTableMissing(); err != nil {
		return "", err
	}
	id := WalletRefundReceiptID(requestID, leg, amount)
	now := common.GetTimestamp()
	receipt := WalletRefundReceipt{
		ID: id, RequestID: requestID, LeaseID: leaseID, UserID: userID,
		TokenID: tokenID, RefID: refID, Amount: amount, Leg: leg, State: "pending",
		CreatedAt: now, UpdatedAt: now,
	}
	if err := DB.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&receipt).Error; err != nil {
		return "", err
	}
	var stored WalletRefundReceipt
	if err := DB.WithContext(ctx).First(&stored, "id = ?", id).Error; err != nil {
		return "", err
	}
	if stored.UserID != userID || stored.Amount != amount || stored.Leg != leg || stored.TokenID != tokenID || stored.RefID != refID || stored.LeaseID != leaseID {
		return "", ErrWalletRefundConflict
	}
	return id, nil
}

// ApplyWalletRefund runs the supplied quota mutation and marks the receipt in
// one database transaction. An already-applied receipt is a no-op.
func ApplyWalletRefund(ctx context.Context, id string, apply func(tx *gorm.DB, receipt WalletRefundReceipt) error) error {
	if id == "" {
		return nil
	}
	if err := walletRefundTableMissing(); err != nil {
		return err
	}
	return DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var receipt WalletRefundReceipt
		if err := lockForUpdate(tx).First(&receipt, "id = ?", id).Error; err != nil {
			return err
		}
		if receipt.State == "applied" {
			return nil
		}
		if receipt.State != "pending" && receipt.State != "unknown" {
			return fmt.Errorf("wallet refund receipt state %q requires reconciliation", receipt.State)
		}
		if apply != nil {
			if err := apply(tx, receipt); err != nil {
				return err
			}
		}
		return tx.Model(&WalletRefundReceipt{}).Where("id = ?", id).
			Updates(map[string]any{"state": "applied", "updated_at": common.GetTimestamp()}).Error
	})
}

// applyWalletRefundQuota writes the quota columns directly. The cache and batch
// helpers are intentionally bypassed because they are outside this receipt's
// transaction and would make an interrupted refund ambiguous.
func ApplyWalletRefundQuotaTx(tx *gorm.DB, receipt WalletRefundReceipt) error {
	return applyWalletRefundQuota(tx, receipt)
}

func PostConsumeUserSubscriptionDeltaTx(tx *gorm.DB, id int, delta int64) error {
	return postConsumeUserSubscriptionDeltaTx(tx, id, delta)
}

func RefundSubscriptionPreConsumeWithTx(tx *gorm.DB, requestID string) error {
	return refundSubscriptionPreConsumeTx(tx, WalletRefundReceipt{RequestID: requestID})
}

func applyWalletRefundQuota(tx *gorm.DB, receipt WalletRefundReceipt) error {
	switch receipt.Leg {
	case "wallet":
		res := tx.Model(&User{}).Where("id = ?", receipt.UserID).
			Update("quota", gorm.Expr("quota + ?", receipt.Amount))
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected != 1 {
			return fmt.Errorf("wallet refund user %d not found", receipt.UserID)
		}
	case "token":
		res := tx.Model(&Token{}).Where("id = ?", receipt.TokenID).
			Updates(map[string]any{
				"remain_quota":  gorm.Expr("remain_quota + ?", receipt.Amount),
				"used_quota":    gorm.Expr("used_quota - ?", receipt.Amount),
				"accessed_time": common.GetTimestamp(),
			})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected != 1 {
			return fmt.Errorf("wallet refund token %d not found", receipt.TokenID)
		}
	case "subscription", "subscription_extra":
		return nil
	default:
		return fmt.Errorf("unknown wallet refund leg %q", receipt.Leg)
	}
	return nil
}

func refundSubscriptionPreConsumeTx(tx *gorm.DB, receipt WalletRefundReceipt) error {
	var record SubscriptionPreConsumeRecord
	if err := lockForUpdate(tx).Where("request_id = ?", receipt.RequestID).First(&record).Error; err != nil {
		return err
	}
	if record.Status == "refunded" {
		return nil
	}
	if record.PreConsumed > 0 {
		if err := postConsumeUserSubscriptionDeltaTx(tx, record.UserSubscriptionId, -record.PreConsumed); err != nil {
			return err
		}
	}
	record.Status = "refunded"
	return tx.Save(&record).Error
}

// RecoverWalletRefunds applies every durable pending refund leg. A lease is
// closed only after all of its receipts are applied, so a partial failure keeps
// the migration fence active for the next recovery pass.
func RecoverWalletRefunds(ctx context.Context, limit int) error {
	if err := walletRefundTableMissing(); err != nil {
		return err
	}
	if limit <= 0 {
		limit = 100
	}
	var receipts []WalletRefundReceipt
	if err := DB.WithContext(ctx).Where("state IN ?", []string{"pending", "unknown"}).
		Order("created_at asc").Limit(limit).Find(&receipts).Error; err != nil {
		return err
	}
	var firstErr error
	leases := map[string]bool{}
	for _, receipt := range receipts {
		var apply func(*gorm.DB, WalletRefundReceipt) error
		switch receipt.Leg {
		case "subscription":
			apply = func(tx *gorm.DB, r WalletRefundReceipt) error { return refundSubscriptionPreConsumeTx(tx, r) }
		case "subscription_extra":
			apply = func(tx *gorm.DB, r WalletRefundReceipt) error {
				return postConsumeUserSubscriptionDeltaTx(tx, r.RefID, -int64(r.Amount))
			}
		default:
			apply = applyWalletRefundQuota
		}
		if err := ApplyWalletRefund(ctx, receipt.ID, apply); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			common.SysError("wallet refund receipt pending: " + err.Error())
			continue
		}
		_ = invalidateUserCache(receipt.UserID)
		if receipt.LeaseID != "" {
			leases[receipt.LeaseID] = true
		}
	}
	for leaseID := range leases {
		var pending int64
		if err := DB.WithContext(ctx).Model(&WalletRefundReceipt{}).
			Where("lease_id = ? AND state != ?", leaseID, "applied").Count(&pending).Error; err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if pending == 0 {
			if err := FinishProductLegacyFundingLease(ctx, leaseID, "refunded"); err != nil && firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}
