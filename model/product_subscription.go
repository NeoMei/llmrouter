package model

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

// ProductSubscriptionPurchase pins a purchase before central payment. Product
// entitlements are issued in the same SQL transaction as the applied receipt,
// so retrying a paid intent cannot grant the subscription a second time.
type productSubscriptionPurchaseContext struct{}

var ErrProductSubscriptionDeclined = errors.New("subscription payment was not applied")

type ProductSubscriptionPurchase struct {
	ID                   string `gorm:"primaryKey;size:64"`
	UserID               int    `gorm:"index:idx_product_subscription_pending,priority:1"`
	PlanID               int    `gorm:"index:idx_product_subscription_pending,priority:2"`
	AccountID            string `gorm:"size:128"`
	RequestKey           string `gorm:"size:128"`
	State                string `gorm:"size:24;index:idx_product_subscription_pending,priority:3"`
	PlanSnapshot         string `gorm:"type:text"`
	RequiredQuota        int
	SubscriptionID       int
	LastAttemptAt        int64 `gorm:"index"` // Unix nanoseconds, used only for fair recovery scheduling.
	CreatedAt, UpdatedAt int64
}

func purchaseProductSubscription(ctx context.Context, userID, planID int, requestKey string, store *ProductFundingStore) error {
	if len(requestKey) > 128 {
		return ErrProductFundingConflict
	}
	var purchase ProductSubscriptionPurchase
	err := DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var user User
		if err := lockForUpdate(tx).First(&user, userID).Error; err != nil {
			return err
		}
		if requestKey != "" {
			id := productDigest(fmt.Sprintf("subscription:%d:%s", userID, requestKey))
			err := lockForUpdate(tx).First(&purchase, "id = ?", id).Error
			if err == nil {
				if purchase.UserID != userID || purchase.PlanID != planID {
					return ErrProductFundingConflict
				}
				return nil
			}
			if !errors.Is(err, gorm.ErrRecordNotFound) {
				return err
			}
		} else {
			err := lockForUpdate(tx).Where("user_id = ? AND plan_id = ? AND state = ?", userID, planID, "pending").First(&purchase).Error
			if err == nil {
				return nil
			}
			if !errors.Is(err, gorm.ErrRecordNotFound) {
				return err
			}
			requestKey = common.GetUUID()
		}
		plan, err := getSubscriptionPlanByIdTx(tx, planID)
		if err != nil {
			return err
		}
		if !plan.Enabled {
			return errors.New("套餐未启用")
		}
		if plan.PriceAmount < 0 {
			return errors.New("套餐价格不能为负数")
		}
		if plan.AllowBalancePay != nil && !*plan.AllowBalancePay {
			return errors.New("该套餐不允许使用余额兑换")
		}
		if plan.MaxPurchasePerUser > 0 {
			var fulfilled, pending int64
			if err := tx.Model(&UserSubscription{}).Where("user_id = ? AND plan_id = ?", userID, planID).Count(&fulfilled).Error; err != nil {
				return err
			}
			if err := tx.Model(&ProductSubscriptionPurchase{}).Where("user_id = ? AND plan_id = ? AND state = ?", userID, planID, "pending").Count(&pending).Error; err != nil {
				return err
			}
			if fulfilled+pending >= int64(plan.MaxPurchasePerUser) {
				return errors.New("已达到该套餐购买上限")
			}
		}
		required, err := calcSubscriptionBalanceQuota(plan.PriceAmount)
		if err != nil {
			return err
		}
		var account ProductFundingAccount
		if err := lockForUpdate(tx).First(&account, "user_id = ?", userID).Error; err != nil {
			return err
		}
		if account.State != "central" {
			return ErrProductFundingPending
		}
		raw, err := common.Marshal(plan)
		if err != nil {
			return err
		}
		purchase = ProductSubscriptionPurchase{ID: productDigest(fmt.Sprintf("subscription:%d:%s", userID, requestKey)), UserID: userID, PlanID: planID, AccountID: account.AccountID, RequestKey: requestKey, State: "pending", PlanSnapshot: string(raw), RequiredQuota: required, CreatedAt: time.Now().Unix()}
		return tx.Create(&purchase).Error
	})
	if err != nil || purchase.State == "applied" {
		return err
	}
	if purchase.State == "rejected" {
		return ErrProductSubscriptionDeclined
	}
	return completeProductSubscriptionPurchase(ctx, purchase, store)
}

func completeProductSubscriptionPurchase(ctx context.Context, purchase ProductSubscriptionPurchase, store *ProductFundingStore) error {
	var plan SubscriptionPlan
	if err := common.UnmarshalJsonStr(purchase.PlanSnapshot, &plan); err != nil {
		return err
	}
	if plan.Id != purchase.PlanID {
		return ErrProductFundingConflict
	}
	var call ProductRuntimeCall
	err := DB.WithContext(ctx).First(&call, "id = ?", purchase.ID).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return errors.Join(ErrProductFundingPending, err)
	}
	if err == nil && (call.UserID != purchase.UserID || call.TokenID != 0 || call.AccountID != purchase.AccountID) {
		return ErrProductFundingConflict
	}
	if errors.Is(err, gorm.ErrRecordNotFound) || call.State == "reserve_pending" || call.State == "reserved" {
		call, err = store.Reserve(ctx, ProductFundingAdmission{FinancialCommitment: true, CallID: purchase.ID, UserID: purchase.UserID, TokenID: 0, Model: "subscription:" + strconv.Itoa(plan.Id), Operation: "subscription_purchase", PriceSnapshot: purchase.PlanSnapshot, Quota: purchase.RequiredQuota, ExternalCounters: true})
		if err != nil {
			return productSubscriptionPaymentError(ctx, purchase.ID, err)
		}
	}
	if call.State == "rejected" || call.State == "released" {
		return productSubscriptionPaymentError(ctx, purchase.ID, ErrProductSubscriptionDeclined)
	}
	if call.State == "reserved" {
		// This fence records an accepted financial purchase intent. No model
		// supplier is dispatched; recovery must finish this same purchase.
		if err := store.MarkDispatched(ctx, purchase.ID); err != nil {
			return errors.Join(ErrProductFundingPending, err)
		}
	}
	usage, err := common.Marshal(map[string]any{"plan_id": plan.Id, "purchase_id": purchase.ID, "quota": purchase.RequiredQuota})
	if err != nil {
		return err
	}
	if err := store.Settle(ctx, purchase.ID, purchase.RequiredQuota, string(usage)); err != nil {
		return productSubscriptionPaymentError(ctx, purchase.ID, err)
	}
	var granted bool
	var upgradeGroup string
	err = DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var user User
		if err := lockForUpdate(tx).First(&user, purchase.UserID).Error; err != nil {
			return err
		}
		var current ProductSubscriptionPurchase
		if err := lockForUpdate(tx).First(&current, "id = ?", purchase.ID).Error; err != nil {
			return err
		}
		if current.State == "applied" {
			return nil
		}
		if current.PlanSnapshot != purchase.PlanSnapshot || current.RequiredQuota != purchase.RequiredQuota || current.AccountID != purchase.AccountID {
			return ErrProductFundingConflict
		}
		subscription, err := CreateUserSubscriptionFromPlanTx(tx.WithContext(context.WithValue(ctx, productSubscriptionPurchaseContext{}, purchase.ID)), purchase.UserID, &plan, PaymentMethodBalance)
		if err != nil {
			return err
		}
		order := &SubscriptionOrder{UserId: purchase.UserID, PlanId: plan.Id, Money: plan.PriceAmount, TradeNo: "SUBCENTRAL" + purchase.ID, PaymentMethod: PaymentMethodBalance, PaymentProvider: PaymentProviderBalance, Status: common.TopUpStatusSuccess, CreateTime: current.CreatedAt, CompleteTime: time.Now().Unix(), ProviderPayload: fmt.Sprintf("central_call_id=%s;charged_quota=%d", purchase.ID, purchase.RequiredQuota)}
		if err := tx.Create(order).Error; err != nil {
			return err
		}
		if err := tx.Model(&current).Updates(map[string]any{"state": "applied", "subscription_id": subscription.Id, "updated_at": time.Now().Unix()}).Error; err != nil {
			return err
		}
		granted = true
		if subscription.PrevUserGroup != "" {
			upgradeGroup = subscription.UpgradeGroup
		}
		return nil
	})
	if err != nil {
		return errors.Join(ErrProductFundingPending, err)
	}
	if granted {
		if upgradeGroup != "" {
			refreshSubscriptionUserGroupCache(purchase.UserID, "central subscription balance purchase")
		}
		RecordLog(purchase.UserID, LogTypeTopup, fmt.Sprintf("使用余额购买订阅成功，套餐: %s，支付金额: %.2f，扣除额度: %d", plan.Title, plan.PriceAmount, purchase.RequiredQuota))
	}
	return nil
}

// Recovery only completes financial intents and product entitlement issuance;
// it never dispatches a provider or creates a new purchase request.
func RecoverProductSubscriptionPurchases(ctx context.Context) error {
	if DB == nil || !DB.Migrator().HasTable(&ProductSubscriptionPurchase{}) {
		return nil
	}
	var purchases []ProductSubscriptionPurchase
	if err := DB.WithContext(ctx).Where("state = ?", "pending").Order("last_attempt_at,id").Limit(100).Find(&purchases).Error; err != nil {
		return err
	}
	var first error
	for _, purchase := range purchases {
		if ctx.Err() != nil {
			if first != nil {
				return first
			}
			return ctx.Err()
		}
		// Record the attempt before network work with an independent bounded
		// receipt context, so an exhausted run does not restart at the same
		// permanently failing purchase and starve later paid work.
		receiptCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		err := DB.WithContext(receiptCtx).Model(&ProductSubscriptionPurchase{}).Where("id = ? AND state = ?", purchase.ID, "pending").Updates(map[string]any{"last_attempt_at": time.Now().UnixNano(), "updated_at": time.Now().Unix()}).Error
		cancel()
		if err == nil {
			var store *ProductFundingStore
			var managed bool
			store, managed, err = ProductFundingStoreForUser(ctx, purchase.UserID)
			if err == nil && !managed {
				err = ErrProductFundingConflict
			}
			if err == nil {
				err = completeProductSubscriptionPurchase(ctx, purchase, store)
			}
		}
		if err != nil && first == nil {
			first = err
		}
	}
	return first
}

func productSubscriptionPaymentError(ctx context.Context, id string, cause error) error {
	var call ProductRuntimeCall
	if err := DB.WithContext(ctx).First(&call, "id = ?", id).Error; err == nil && (call.State == "rejected" || call.State == "released") {
		if err := DB.WithContext(ctx).Model(&ProductSubscriptionPurchase{}).Where("id = ? AND state = ?", id, "pending").Updates(map[string]any{"state": "rejected", "updated_at": time.Now().Unix()}).Error; err != nil {
			return errors.Join(ErrProductFundingPending, err)
		}
		return errors.Join(ErrProductSubscriptionDeclined, cause)
	}
	return errors.Join(ErrProductFundingPending, cause)
}
