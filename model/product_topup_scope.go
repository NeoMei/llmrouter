package model

import (
	"context"
	"errors"

	userkit "github.com/NeoMei/UniHub/packages/userkit-go"
	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

// ProductWalletAccount requires a proven central owner in this deployment.
// A shared native-only owner cannot authorize product payment/history access.
func ProductWalletAccount(ctx context.Context, userID int) (ProductFundingAccount, error) {
	var a ProductFundingAccount
	if DB == nil || userID <= 0 {
		return a, ErrProductFundingPending
	}
	var owner User
	if err := DB.WithContext(ctx).Select("id", "funding_realm").First(&owner, userID).Error; err != nil {
		return a, err
	}
	if owner.FundingRealm != "" {
		return a, ErrNativeFundingLocalOnly
	}
	if err := DB.WithContext(ctx).First(&a, "user_id = ?", userID).Error; err != nil {
		return a, err
	}
	scope, err := ProductRuntimeScopeDigest()
	if err != nil {
		return a, err
	}
	if a.ScopeDigest == "" || a.ScopeDigest != scope || a.AccountID == "" || a.Asset != userkit.RuntimeAsset || a.UnitVersion != userkit.RuntimeUnitVersion {
		return a, ErrProductFundingConflict
	}
	if a.State != "central" {
		return a, ErrProductFundingPending
	}
	return a, nil
}

func productTopUpScopeQuery(ctx context.Context, userID int) (*gorm.DB, error) {
	a, err := ProductWalletAccount(ctx, userID)
	if err != nil {
		return nil, err
	}
	return DB.WithContext(ctx).Model(&TopUp{}).Where("user_id = ? AND funding_scope_digest = ? AND funding_account_id = ?", userID, a.ScopeDigest, a.AccountID).Where("(funding_asset = ? AND funding_unit_version = ?) OR (funding_asset = ? AND funding_unit_version = ?)", userkit.RuntimeAsset, userkit.RuntimeUnitVersion, userkit.RuntimeCashAsset, userkit.RuntimeCashUnitVersion), nil
}

func GetProductUserWeChatTopUp(ctx context.Context, userID int, tradeNo string) (*TopUp, error) {
	q, err := productTopUpScopeQuery(ctx, userID)
	if err != nil {
		return nil, err
	}
	var order TopUp
	err = q.Where("trade_no = ? AND payment_provider = ?", tradeNo, PaymentProviderWeChatPay).First(&order).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrTopUpNotFound
	}
	return &order, err
}

// Pending rows have no award yet, so history scopes the immutable checkout
// snapshot rather than joining success-only central credit receipts.
func GetProductUserTopUps(ctx context.Context, userID int, keyword string, page *common.PageInfo) ([]*TopUp, int64, error) {
	q, err := productTopUpScopeQuery(ctx, userID)
	if err != nil {
		return nil, 0, err
	}
	q = q.Where("create_time >= ?", topUpQueryCutoff())
	if keyword != "" {
		pattern, err := sanitizeLikePattern(keyword)
		if err != nil {
			return nil, 0, err
		}
		q = q.Where("trade_no LIKE ? ESCAPE '!'", pattern)
	}
	var total int64
	if err = q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	rows := make([]*TopUp, 0)
	err = q.Order("id desc").Limit(page.GetPageSize()).Offset(page.GetStartIdx()).Find(&rows).Error
	return rows, total, err
}

func validateProductTopUpFundingTx(tx *gorm.DB, order *TopUp) error {
	if order.FundingScopeDigest == "" || order.FundingAccountID == "" || !((order.FundingAsset == userkit.RuntimeAsset && order.FundingUnitVersion == userkit.RuntimeUnitVersion) || (order.FundingAsset == userkit.RuntimeCashAsset && order.FundingUnitVersion == userkit.RuntimeCashUnitVersion)) {
		return ErrProductFundingConflict
	}
	if _, err := lockCentralFundingOwnerTx(tx, order.UserId); err != nil {
		return err
	}
	var a ProductFundingAccount
	if err := lockForUpdate(tx).First(&a, "user_id = ?", order.UserId).Error; err != nil {
		return err
	}
	scope, err := ProductRuntimeScopeDigest()
	if err != nil {
		return err
	}
	if a.State != "central" || a.AccountID != order.FundingAccountID || a.ScopeDigest != scope || order.FundingScopeDigest != scope || a.Asset != userkit.RuntimeAsset || a.UnitVersion != userkit.RuntimeUnitVersion {
		return ErrProductFundingConflict
	}
	return nil
}
