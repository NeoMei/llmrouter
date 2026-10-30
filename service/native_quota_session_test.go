package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/alicebob/miniredis/v2"
	"github.com/go-redis/redis/v8"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func enrollNativeSessionKey(t *testing.T, db *gorm.DB) {
	t.Helper()
	require.NoError(t, db.Model(&model.Token{}).Where("id = ?", 7).Update("auth_realm", model.NativeFundingLocalOnly).Error)
	digest := sha256.Sum256([]byte("original-key"))
	manifest := model.NativeTokenQuotaManifest{Version: 1, CutoverEvidence: strings.Repeat("a", 64), Tokens: []model.NativeTokenQuotaImport{{TokenID: 7, UserID: 42, KeySHA256: hex.EncodeToString(digest[:]), RemainQuota: 200, Status: common.TokenStatusEnabled, ExpiredTime: -1}}}
	raw, err := common.Marshal(manifest)
	require.NoError(t, err)
	digest = sha256.Sum256(raw)
	require.NoError(t, model.ImportNativeTokenQuota(context.Background(), db, raw, hex.EncodeToString(digest[:])))
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	oldRDB := common.RDB
	common.RDB, common.RedisEnabled, common.BatchUpdateEnabled = client, true, true
	t.Cleanup(func() { common.RDB = oldRDB; require.NoError(t, client.Close()) })
}

func TestNativeQuotaSessionTokenFailureRetainsSameOperationForRetry(t *testing.T) {
	db, c, info := legacyLeaseFixture(t)
	t.Setenv("UNIHUB_PRODUCT_RUNTIME_ENABLED", "false")
	enrollNativeSessionKey(t, db)
	info.NativeRealm = true
	session := &BillingSession{relayInfo: info, funding: &WalletFunding{userId: 42}}
	require.Nil(t, session.preConsume(c, 20))
	const hook = "native:settlement-receipt-failure"
	require.NoError(t, db.Callback().Create().Before("gorm:create").Register(hook, func(tx *gorm.DB) {
		if tx.Statement.Table == "native_token_quota_operations" {
			tx.AddError(errors.New("synthetic token receipt unavailable"))
		}
	}))
	err := session.Settle(12)
	require.Error(t, err)
	assert.False(t, session.settled, "funding completion must not swallow an unresolved token receipt")
	assert.True(t, session.fundingSettled)
	require.NoError(t, db.Callback().Create().Remove(hook))
	require.NoError(t, session.Settle(12))
	require.NoError(t, session.Settle(12))
	assert.True(t, session.settled)
	remain, used, err := model.NativeTokenQuotaBalance(context.Background(), 7, 0)
	require.NoError(t, err)
	assert.Equal(t, 188, remain)
	assert.Equal(t, 12, used)
	model.FlushBatchUpdates()
	var user model.User
	require.NoError(t, db.First(&user, 42).Error)
	assert.Equal(t, 488, user.Quota, "same-session token retry must not issue the funding refund twice")
}

type uncertainNativeWallet struct {
	WalletFunding
	preCalls, refundCalls int
}

func (w *uncertainNativeWallet) PreConsume(amount int) error {
	w.preCalls++
	if err := w.WalletFunding.PreConsume(amount); err != nil {
		return err
	}
	return errors.New("synthetic funding result unknown")
}
func (w *uncertainNativeWallet) Refund() error { w.refundCalls++; return w.WalletFunding.Refund() }

func TestNativeQuotaUnknownFundingCannotReplayOrRefundAfterReconstruction(t *testing.T) {
	db, c, info := legacyLeaseFixture(t)
	t.Setenv("UNIHUB_PRODUCT_RUNTIME_ENABLED", "false")
	enrollNativeSessionKey(t, db)
	info.NativeRealm = true
	funding := &uncertainNativeWallet{WalletFunding: WalletFunding{userId: 42}}
	session := &BillingSession{relayInfo: info, funding: funding}
	require.NotNil(t, session.preConsume(c, 20))
	assert.True(t, session.reconciliationPending)
	session.Refund(c)
	assert.Zero(t, funding.refundCalls)
	var journal model.NativeTokenQuotaSession
	require.NoError(t, db.First(&journal, "token_id = ?", 7).Error)
	assert.Equal(t, "unknown", journal.FundingState)
	assert.Equal(t, "preconsume", journal.FundingStage)
	// A new process/session object has no trusted consumed amount. It must read
	// the durable UNKNOWN, not repeat the funding call or invent a refund.
	reconstructed := &BillingSession{relayInfo: info, funding: funding}
	require.NotNil(t, reconstructed.preConsume(c, 20))
	assert.Equal(t, 1, funding.preCalls)
	require.ErrorIs(t, reconstructed.Settle(12), model.ErrNativeFundingReconciliation)
	reconstructed.Refund(c)
	assert.Zero(t, funding.refundCalls)
	remain, used, err := model.NativeTokenQuotaBalance(context.Background(), 7, 0)
	require.NoError(t, err)
	assert.Equal(t, 180, remain)
	assert.Equal(t, 20, used)
	model.FlushBatchUpdates()
	var user model.User
	require.NoError(t, db.First(&user, 42).Error)
	assert.Equal(t, 480, user.Quota)
}

func TestNativeQuotaRefundTokenFailureRetriesOnlyTokenReceipt(t *testing.T) {
	db, c, info := legacyLeaseFixture(t)
	t.Setenv("UNIHUB_PRODUCT_RUNTIME_ENABLED", "false")
	enrollNativeSessionKey(t, db)
	info.NativeRealm = true
	session := &BillingSession{relayInfo: info, funding: &WalletFunding{userId: 42}}
	require.Nil(t, session.preConsume(c, 20))
	const hook = "native:refund-token-receipt-failure"
	require.NoError(t, db.Callback().Create().Before("gorm:create").Register(hook, func(tx *gorm.DB) {
		if tx.Statement.Table == "native_token_quota_operations" {
			tx.AddError(errors.New("synthetic token refund unavailable"))
		}
	}))
	session.Refund(c)
	assert.False(t, session.refunded)
	assert.True(t, session.nativeFundingRefunded)
	require.NoError(t, db.Callback().Create().Remove(hook))
	session.Refund(c)
	session.Refund(c)
	assert.True(t, session.refunded)
	remain, used, err := model.NativeTokenQuotaBalance(context.Background(), 7, 0)
	require.NoError(t, err)
	assert.Equal(t, 200, remain)
	assert.Zero(t, used)
	model.FlushBatchUpdates()
	var user model.User
	require.NoError(t, db.First(&user, 42).Error)
	assert.Equal(t, 500, user.Quota)
}

func TestNativeQuotaFundingAcknowledgedButReceiptFailureStaysUnknown(t *testing.T) {
	db, c, info := legacyLeaseFixture(t)
	t.Setenv("UNIHUB_PRODUCT_RUNTIME_ENABLED", "false")
	enrollNativeSessionKey(t, db)
	info.NativeRealm = true
	session := &BillingSession{relayInfo: info, funding: &WalletFunding{userId: 42}}
	require.Nil(t, session.preConsume(c, 20))
	const hook = "native:funding-ack-receipt-failure"
	require.NoError(t, db.Callback().Update().Before("gorm:update").Register(hook, func(tx *gorm.DB) {
		values, ok := tx.Statement.Dest.(map[string]any)
		if ok && tx.Statement.Table == "native_token_quota_sessions" && values["funding_state"] == "confirmed" {
			tx.AddError(errors.New("synthetic funding acknowledgement unavailable"))
		}
	}))
	require.Error(t, session.Settle(12))
	require.NoError(t, db.Callback().Update().Remove(hook))
	assert.False(t, session.settled)
	assert.True(t, session.reconciliationPending)
	require.ErrorIs(t, session.Settle(12), model.ErrNativeFundingReconciliation)
	session.Refund(c)
	var journal model.NativeTokenQuotaSession
	require.NoError(t, db.First(&journal, "token_id = ?", 7).Error)
	assert.Equal(t, "unknown", journal.FundingState)
	assert.Equal(t, "settle:12", journal.FundingStage)
	remain, used, err := model.NativeTokenQuotaBalance(context.Background(), 7, 0)
	require.NoError(t, err)
	assert.Equal(t, 180, remain)
	assert.Equal(t, 20, used)
	model.FlushBatchUpdates()
	var user model.User
	require.NoError(t, db.First(&user, 42).Error)
	assert.Equal(t, 488, user.Quota, "an unknown acknowledgement must block both repeat funding adjustment and refund")
}

func TestNativeQuotaDefiniteExtraWalletDeclineKeepsOriginalHoldRefundable(t *testing.T) {
	db, c, info := legacyLeaseFixture(t)
	t.Setenv("UNIHUB_PRODUCT_RUNTIME_ENABLED", "false")
	enrollNativeSessionKey(t, db)
	require.NoError(t, db.Model(&model.User{}).Where("id = ?", 42).Updates(map[string]any{"quota": 30, "funding_realm": model.NativeFundingLocalOnly, "funding_realm_evidence": "synthetic-native-owner", "native_password": true, "auth_version": 1}).Error)
	info.NativeRealm = true
	value, apiErr := NewBillingSession(c, info, 20)
	require.Nil(t, apiErr)
	require.NotNil(t, value)
	session := value.(*BillingSession)
	err := session.Reserve(40)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInsufficientWalletQuota, "definite no-debit must keep the typed decline through the API wrapper")
	assert.False(t, session.reconciliationPending)
	var journal model.NativeTokenQuotaSession
	require.NoError(t, db.First(&journal, "token_id = ?", 7).Error)
	assert.Equal(t, "declined", journal.FundingState)
	session.Refund(c)
	session.Refund(c)
	assert.True(t, session.refunded)
	remain, used, err := model.NativeTokenQuotaBalance(context.Background(), 7, 0)
	require.NoError(t, err)
	assert.Equal(t, 200, remain)
	assert.Zero(t, used)
	model.FlushBatchUpdates()
	var owner model.User
	require.NoError(t, db.First(&owner, 42).Error)
	assert.Equal(t, 30, owner.Quota)
}
