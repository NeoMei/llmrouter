package model

import (
	"context"
	"errors"
	"strconv"

	userkit "github.com/NeoMei/UniHub/packages/userkit-go"
	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

// A subscription request spends product entitlement, not cash again. Its exact
// pre-consume record must lead to a centrally paid purchase in this deployment.
func readProductSubscriptionTelemetryLog(ctx context.Context, log *Log, payer ProductFundingAccount, digest string) (ProductRuntimeTelemetryLog, bool, error) {
	var result ProductRuntimeTelemetryLog
	if log == nil || log.RequestId == "" || log.UserId <= 0 || log.UserId != payer.UserID || log.TokenId < 0 || digest == "" || payer.ScopeDigest != digest || payer.State != "central" || payer.AccountID == "" {
		return result, false, nil
	}
	for _, schema := range []any{&SubscriptionPreConsumeRecord{}, &UserSubscription{}, &ProductSubscriptionPurchase{}} {
		if !DB.Migrator().HasTable(schema) {
			return result, false, nil
		}
	}
	var record SubscriptionPreConsumeRecord
	if err := DB.WithContext(ctx).First(&record, "request_id = ?", log.RequestId).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return result, false, nil
		}
		return result, false, err
	}
	if record.UserId != log.UserId || record.UserSubscriptionId <= 0 || record.PreConsumed <= 0 || (record.Status != "consumed" && record.Status != "refunded") || log.Type == LogTypeConsume && record.Status != "consumed" {
		return result, false, nil
	}
	var subscription UserSubscription
	if err := DB.WithContext(ctx).First(&subscription, record.UserSubscriptionId).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return result, false, nil
		}
		return result, false, err
	}
	// Expired or cancelled entitlements retain their legitimate usage history.
	if subscription.UserId != log.UserId || subscription.PlanId <= 0 || subscription.Source != PaymentMethodBalance {
		return result, false, nil
	}
	var purchase ProductSubscriptionPurchase
	if err := DB.WithContext(ctx).First(&purchase, "subscription_id = ? AND user_id = ? AND plan_id = ? AND state = ?", subscription.Id, log.UserId, subscription.PlanId, "applied").Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return result, false, nil
		}
		return result, false, err
	}
	if purchase.AccountID != payer.AccountID || purchase.RequiredQuota < 0 || purchase.RequiredQuota > common.MaxQuota || purchase.PlanSnapshot == "" {
		return result, false, nil
	}
	var plan SubscriptionPlan
	if common.UnmarshalJsonStr(purchase.PlanSnapshot, &plan) != nil || plan.Id != purchase.PlanID {
		return result, false, nil
	}
	var call ProductRuntimeCall
	if err := DB.WithContext(ctx).First(&call, "id = ?", purchase.ID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return result, false, nil
		}
		return result, false, err
	}
	if call.UserID != log.UserId || call.TokenID != 0 || call.AccountID != payer.AccountID || call.ScopeDigest != digest || call.State != "settled" || call.ParentCallID != "" || !call.FinancialCommitment || !call.ExternalCounters || call.FinalQuota != purchase.RequiredQuota {
		return result, false, nil
	}
	reserve, err := ProductRuntimeCallContract(call)
	if err != nil || reserve.Model != "subscription:"+strconv.Itoa(plan.Id) || reserve.Operation != "subscription_purchase" || reserve.PriceVersion != reserve.UnitVersion {
		return result, false, nil
	}
	snapshot := productDigest(purchase.PlanSnapshot)
	if call.SnapshotDigest != "" {
		if call.SnapshotDigest != snapshot {
			return result, false, nil
		}
	} else if reserve.Asset != userkit.RuntimeAsset || call.PricingDigest != snapshot {
		return result, false, nil
	}
	var owner User
	if err := DB.WithContext(ctx).Unscoped().Select("id", "funding_realm").First(&owner, log.UserId).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return result, false, nil
		}
		return result, false, err
	}
	if owner.FundingRealm != "" {
		return result, false, nil
	}
	if log.TokenId > 0 {
		var token Token
		if err := DB.WithContext(ctx).Unscoped().Select("id", "user_id", "auth_realm", "funding_account_id").First(&token, log.TokenId).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return result, false, nil
			}
			return result, false, err
		}
		if token.UserId != log.UserId || token.AuthRealm == NativeFundingLocalOnly || token.FundingAccountID != "" && token.FundingAccountID != payer.AccountID {
			return result, false, nil
		}
	}
	return ProductRuntimeTelemetryLog{Log: log, AccountID: payer.AccountID, State: record.Status, Asset: userkit.RuntimeAsset, UnitVersion: userkit.RuntimeUnitVersion, SubscriptionID: subscription.Id, PurchaseCallID: purchase.ID, BillingSource: "subscription"}, true, nil
}
