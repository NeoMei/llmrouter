package model

import (
	"context"
	"errors"
	"strconv"
	"time"

	userkit "github.com/NeoMei/UniHub/packages/userkit-go"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/internal/platformaccount"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ProductCreditAward is committed in the original payment/redemption transaction.
// It stores a command and its receipt, never a second spendable balance.
type ProductCreditAward struct {
	ScopeDigest          string `gorm:"size:64"`
	ID                   string `gorm:"primaryKey;size:64"`
	UserID               int    `gorm:"index"`
	Request              string `gorm:"type:text"`
	RequestDigest        string `gorm:"size:64"`
	State                string `gorm:"size:24;index"`
	ReceiptID            string `gorm:"size:128"`
	CreatedAt, UpdatedAt int64
}

// QueueProductCreditAward returns false for an account still on the original
// local backend. For a central account the business receipt and outbox are one
// SQL transaction; external HTTP must happen only after that transaction commits.
func QueueProductCreditAward(tx *gorm.DB, userID int, kind, receiptID string, quota int, source any) (bool, error) {
	if !tx.Migrator().HasTable(&ProductFundingAccount{}) {
		return false, nil
	}
	var a ProductFundingAccount
	err := lockForUpdate(tx).First(&a, "user_id = ?", userID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	if err != nil {
		return true, err
	}
	if a.State != "central" {
		return true, ErrProductFundingPending
	}
	if quota < 0 || quota > common.MaxQuota || receiptID == "" || len(receiptID) > 128 {
		return true, ErrProductFundingConflict
	}
	if kind != "payment" && kind != "redemption" && kind != "checkin" && kind != "referral" && kind != "signup" {
		return true, ErrProductFundingConflict
	}
	payload, err := common.Marshal(source)
	if err != nil {
		return true, err
	}
	req := userkit.RuntimeAwardRequest{ReceiptID: receiptID, SourceKind: kind, ReceiptDigest: productDigest(string(payload)), Reason: "llmrouter verified " + kind + " receipt", LocalUserID: strconv.Itoa(userID), AccountID: a.AccountID, Amount: strconv.Itoa(quota), Asset: a.Asset, UnitVersion: a.UnitVersion, SourcePayload: payload}
	raw, err := common.Marshal(req)
	if err != nil {
		return true, err
	}
	id := productDigest(kind + ":" + receiptID)
	award := ProductCreditAward{ScopeDigest: a.ScopeDigest, ID: id, UserID: userID, Request: string(raw), RequestDigest: productDigest(string(raw)), State: "pending", CreatedAt: time.Now().Unix()}
	if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&award).Error; err != nil {
		return true, err
	}
	var existing ProductCreditAward
	if err := lockForUpdate(tx).First(&existing, "id = ?", id).Error; err != nil {
		return true, err
	}
	if existing.RequestDigest != award.RequestDigest || existing.UserID != userID {
		return true, ErrProductFundingConflict
	}
	return true, nil
}

func FlushProductCreditAwards(ctx context.Context, userID int) error {
	if DB == nil || !DB.Migrator().HasTable(&ProductCreditAward{}) {
		return nil
	}
	var client *userkit.RuntimeClient
	started := time.Now().UnixNano()
	afterTime := int64(-1)
	afterID := ""
	var first error
	for ctx.Err() == nil {
		var awards []ProductCreditAward
		q := DB.WithContext(ctx).Where("state = ? AND updated_at < ?", "pending", started).Where("updated_at > ? OR (updated_at = ? AND id > ?)", afterTime, afterTime, afterID).Order("updated_at,id").Limit(100)
		if userID > 0 {
			q = q.Where("user_id = ?", userID)
		}
		if err := q.Find(&awards).Error; err != nil {
			return err
		}
		if len(awards) == 0 {
			return first
		}
		if client == nil {
			var err error
			client, err = platformaccount.RuntimeClient()
			if err != nil {
				return err
			}
		}
		for _, award := range awards {
			afterTime, afterID = award.UpdatedAt, award.ID
			var req userkit.RuntimeAwardRequest
			err := common.UnmarshalJsonStr(award.Request, &req)
			if err == nil && (award.ScopeDigest == "" || award.ScopeDigest != NewProductFundingStore(DB, client).ScopeDigest()) {
				err = ErrProductFundingConflict
			}
			var result userkit.RuntimeAdjustment
			if err == nil {
				result, err = client.Award(ctx, req)
			}
			if err == nil && (result.Status != "APPLIED" || result.AccountID != req.AccountID || result.Amount != req.Amount) {
				err = ErrProductFundingPending
			}
			receiptCtx, stop := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
			fields := map[string]any{"updated_at": time.Now().UnixNano()}
			if err == nil {
				fields["state"] = "applied"
				fields["receipt_id"] = result.ID
			}
			saveErr := DB.WithContext(receiptCtx).Model(&ProductCreditAward{}).Where("id = ? AND request_digest = ? AND state = ?", award.ID, award.RequestDigest, "pending").Updates(fields).Error
			stop()
			if err == nil {
				err = saveErr
			}
			if err != nil && first == nil {
				first = err
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
		}
	}
	return ctx.Err()
}

// ActivateRegisteredProductFunding is called only after a newly registered
// product identity is centrally bound. Original signup rewards become one
// scoped central award; legacy users use the separate reviewed import instead.
func ActivateRegisteredProductFunding(ctx context.Context, user *User) error {
	if !platformaccount.RuntimeEnabled() {
		return nil
	}
	client, err := platformaccount.RuntimeClient()
	if err != nil {
		return err
	}
	access, err := client.Access(ctx, strconv.Itoa(user.Id))
	if err != nil {
		return err
	}
	err = DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var current User
		if err := lockForUpdate(tx).First(&current, user.Id).Error; err != nil {
			return err
		}
		var old ProductFundingAccount
		err := tx.First(&old, "user_id = ?", user.Id).Error
		if err == nil {
			if old.ScopeDigest != NewProductFundingStore(DB, client).ScopeDigest() || old.State != "central" || old.AccountID != access.Binding.PersonalAccountID {
				return ErrProductFundingConflict
			}
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if current.Quota < 0 {
			return ErrProductFundingConflict
		}
		a := ProductFundingAccount{ScopeDigest: NewProductFundingStore(DB, client).ScopeDigest(), UserID: user.Id, AccountID: access.Binding.PersonalAccountID, Asset: userkit.RuntimeAsset, UnitVersion: userkit.RuntimeUnitVersion, State: "central", SnapshotQuota: current.Quota, SnapshotDigest: productDigest("signup:" + strconv.Itoa(user.Id) + ":" + strconv.Itoa(current.Quota)), BatchID: "signup", CreatedAt: time.Now().Unix()}
		if err := tx.Create(&a).Error; err != nil {
			return err
		}
		if current.Quota > 0 {
			if _, err := QueueProductCreditAward(tx, user.Id, "signup", "signup-"+strconv.Itoa(user.Id), current.Quota, map[string]any{"local_user_id": user.Id, "initial_quota": current.Quota, "source": "original_product_registration"}); err != nil {
				return err
			}
		}
		return tx.Model(&current).Update("quota", 0).Error
	})
	if err != nil {
		return err
	}
	return FlushProductCreditAwards(ctx, user.Id)
}

// Registration-only rewards are frozen into the single signup award at central
// activation. This helper cannot credit an already provisioned local wallet.
func CreditProductRegistrationSeed(userID, amount int) error {
	if amount <= 0 || amount > common.MaxQuota {
		return ErrProductFundingConflict
	}
	r := DB.Model(&User{}).Where("id = ? AND product_registration_pending = ? AND quota <= ?", userID, true, common.MaxQuota-amount).Update("quota", gorm.Expr("quota + ?", amount))
	if r.Error != nil {
		return r.Error
	}
	if r.RowsAffected != 1 {
		return ErrProductFundingConflict
	}
	return nil
}
