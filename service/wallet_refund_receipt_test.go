package service

import (
	"context"
	"errors"
	"testing"

	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// assertLegacyLease in product_legacy_lease_test.go reads the "unadmitted"
// lease, so these fixtures use that ID while keeping distinct request IDs.

func TestWalletRefundReceiptFailureKeepsHoldAndRecoversOnce(t *testing.T) {
	db, c, info := legacyLeaseFixture(t)
	info.RequestId = "wallet-refund-receipt"
	require.NoError(t, model.BeginProductLegacyFundingLease(context.Background(), "unadmitted", 42))
	require.NoError(t, db.Model(&model.Token{}).Where("id = ?", 7).Update("remain_quota", 180).Error)
	require.NoError(t, db.Model(&model.User{}).Where("id = ?", 42).Update("quota", 480).Error)
	session := &BillingSession{
		relayInfo: info, funding: &WalletFunding{userId: 42, consumed: 20},
		legacyLeaseID: "unadmitted", preConsumedQuota: 20, tokenConsumed: 20,
	}
	const hook = "test:wallet-refund-apply-failure"
	require.NoError(t, db.Callback().Update().Before("gorm:update").Register(hook, func(tx *gorm.DB) {
		if tx.Statement.Table == "users" {
			tx.AddError(errors.New("wallet write unavailable"))
		}
	}))
	refund := session.prepareRefund(c)
	require.NotNil(t, refund)
	refund()
	var pending int64
	require.NoError(t, db.Model(&model.WalletRefundReceipt{}).Where("state = ?", "pending").Count(&pending).Error)
	require.EqualValues(t, 1, pending)
	var user model.User
	require.NoError(t, db.First(&user, 42).Error)
	require.Equal(t, 480, user.Quota)
	assertLegacyLease(t, db, "active")
	require.NoError(t, db.Callback().Update().Remove(hook))
	require.NoError(t, model.RecoverWalletRefunds(context.Background(), 10))
	require.NoError(t, db.First(&user, 42).Error)
	require.Equal(t, 500, user.Quota)
	var token model.Token
	require.NoError(t, db.First(&token, 7).Error)
	require.Equal(t, 200, token.RemainQuota)
	var count int64
	require.NoError(t, db.Model(&model.WalletRefundReceipt{}).Where("state = ?", "applied").Count(&count).Error)
	require.EqualValues(t, 2, count)
	assertLegacyLease(t, db, "refunded")
	require.NoError(t, model.RecoverWalletRefunds(context.Background(), 10))
	require.NoError(t, db.First(&user, 42).Error)
	require.Equal(t, 500, user.Quota)
	require.NoError(t, db.First(&token, 7).Error)
	require.Equal(t, 200, token.RemainQuota)
}

func TestWalletRefundReceiptCreationFailureKeepsLeaseActive(t *testing.T) {
	db, c, info := legacyLeaseFixture(t)
	info.RequestId = "wallet-refund-begin-failure"
	require.NoError(t, model.BeginProductLegacyFundingLease(context.Background(), "unadmitted", 42))
	session := &BillingSession{
		relayInfo: info, funding: &WalletFunding{userId: 42, consumed: 20},
		legacyLeaseID: "unadmitted", preConsumedQuota: 20, tokenConsumed: 20,
	}
	const hook = "test:wallet-refund-begin-failure"
	require.NoError(t, db.Callback().Create().Before("gorm:create").Register(hook, func(tx *gorm.DB) {
		if tx.Statement.Table == "wallet_refund_receipts" {
			tx.AddError(errors.New("receipt create unavailable"))
		}
	}))
	t.Cleanup(func() { _ = db.Callback().Create().Remove(hook) })
	session.Refund(c)
	assertLegacyLease(t, db, "active")
	require.True(t, session.reconciliationPending)
	var count int64
	require.NoError(t, db.Model(&model.WalletRefundReceipt{}).Count(&count).Error)
	require.Zero(t, count)
}

func TestZeroPreconsumeRefundCreatesNoWalletReceipt(t *testing.T) {
	db, c, info := legacyLeaseFixture(t)
	info.RequestId = "zero-preconsume-refund"
	require.NoError(t, model.BeginProductLegacyFundingLease(context.Background(), "unadmitted", 42))
	session := &BillingSession{
		relayInfo: info, funding: &WalletFunding{userId: 42}, legacyLeaseID: "unadmitted",
	}
	session.Refund(c)
	assertLegacyLease(t, db, "refunded")
	var count int64
	require.NoError(t, db.Model(&model.WalletRefundReceipt{}).Count(&count).Error)
	require.Zero(t, count)
}

func TestMarkBillingReconciliationPendingSetsRelayFlag(t *testing.T) {
	info := &relaycommon.RelayInfo{}
	markBillingReconciliationPending(info, errors.New("settlement failed"))
	require.True(t, info.BillingSettlementFailed)
}

func TestSettleBillingTokenFailureHoldsReservation(t *testing.T) {
	db, c, info := legacyLeaseFixture(t)
	info.RequestId = "settle-token-failure"
	require.NoError(t, model.BeginProductLegacyFundingLease(context.Background(), "unadmitted", 42))
	sessionValue, apiErr := NewBillingSession(c, info, 20)
	require.Nil(t, apiErr)
	session := sessionValue.(*BillingSession)
	const hook = "test:settle-token-failure"
	require.NoError(t, db.Callback().Query().Before("gorm:query").Register(hook, func(tx *gorm.DB) {
		if tx.Statement.Table == "tokens" {
			tx.AddError(errors.New("token quota read unavailable"))
		}
	}))
	t.Cleanup(func() { _ = db.Callback().Query().Remove(hook) })
	settleErr := session.Settle(12)
	require.Error(t, settleErr)
	markBillingReconciliationPending(info, settleErr)
	require.False(t, session.IsSettled())
	require.True(t, session.settlementFailed)
	require.True(t, session.reconciliationPending)
	require.True(t, info.BillingSettlementFailed)
	assertLegacyLease(t, db, "active")
	session.Refund(c)
	assertLegacyLease(t, db, "active")
}

func TestSettleBillingPlatformModeSetsRelayFlag(t *testing.T) {
	_, c, info := legacyLeaseFixture(t)
	info.Billing = nil
	info.PlatformMode = true
	err := SettleBilling(c, info, 10)
	require.Error(t, err)
	require.True(t, info.BillingSettlementFailed)
}
