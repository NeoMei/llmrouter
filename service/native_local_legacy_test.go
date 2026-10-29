package service

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/internal/platformaccount"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/setting/model_setting"
	"github.com/QuantumNous/new-api/types"
	"github.com/bytedance/gopkg/util/gopool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestNativeLocalLegacyWalletFencesBindingAndCutover(t *testing.T) {
	db, c, info := legacyLeaseFixture(t)
	require.NoError(t, db.AutoMigrate(&model.UniHubIdentityMapping{}, &model.ProductAuthLifecycle{}))
	info.NativeLocalLegacy = true
	session, apiErr := NewBillingSession(c, info, 20)
	require.Nil(t, apiErr)
	require.NotNil(t, session)
	var lease model.ProductLegacyFundingLease
	require.NoError(t, db.First(&lease, "user_id = ?", 42).Error)
	assert.Equal(t, "active", lease.State)
	_, err := model.BindUniHubIdentity(platformaccount.Identity{Issuer: "https://hub.test", User: platformaccount.User{ID: "central-user"}}, 42)
	require.ErrorIs(t, err, model.ErrProductFundingPending)
	store := model.NewProductFundingStore(db, nil)
	require.ErrorIs(t, store.Import(context.Background(), 42, "account", "batch"), model.ErrProductFundingPending)
	require.NoError(t, session.Settle(12))
	require.NoError(t, db.First(&lease, "id = ?", lease.ID).Error)
	assert.Equal(t, "settled", lease.State)
	var user model.User
	var token model.Token
	require.NoError(t, db.First(&user, 42).Error)
	require.NoError(t, db.First(&token, 7).Error)
	assert.Equal(t, 488, user.Quota)
	assert.Equal(t, 188, token.RemainQuota)
	_, err = model.BindUniHubIdentity(platformaccount.Identity{Issuer: "https://hub.test", User: platformaccount.User{ID: "central-user"}}, 42)
	require.NoError(t, err)
	info.RequestId = "bound-rejected"
	session, apiErr = NewBillingSession(c, info, 1)
	assert.Nil(t, session)
	assert.NotNil(t, apiErr)
}

func TestNativeLocalLegacySubscriptionFencesBinding(t *testing.T) {
	db, c, info := legacyLeaseFixture(t)
	require.NoError(t, db.AutoMigrate(&model.UniHubIdentityMapping{}, &model.ProductAuthLifecycle{}, &model.SubscriptionPlan{}, &model.UserSubscription{}, &model.SubscriptionPreConsumeRecord{}))
	require.NoError(t, db.Create(&model.SubscriptionPlan{Id: 1, Title: "local"}).Error)
	require.NoError(t, db.Create(&model.UserSubscription{Id: 1, UserId: 42, PlanId: 1, Status: "active", AmountTotal: 100, StartTime: time.Now().Add(-time.Minute).Unix(), EndTime: time.Now().Add(time.Hour).Unix()}).Error)
	info.NativeLocalLegacy = true
	info.UserSetting.BillingPreference = "subscription_only"
	session, apiErr := NewBillingSession(c, info, 20)
	require.Nil(t, apiErr)
	require.NotNil(t, session)
	var lease model.ProductLegacyFundingLease
	require.NoError(t, db.First(&lease, "user_id = ?", 42).Error)
	assert.Equal(t, "active", lease.State)
	_, err := model.BindUniHubIdentity(platformaccount.Identity{Issuer: "https://hub.test", User: platformaccount.User{ID: "central-user"}}, 42)
	require.ErrorIs(t, err, model.ErrProductFundingPending)
	require.NoError(t, session.Settle(12))
	require.NoError(t, db.First(&lease, "id = ?", lease.ID).Error)
	assert.Equal(t, "settled", lease.State)
	var sub model.UserSubscription
	require.NoError(t, db.First(&sub, 1).Error)
	assert.EqualValues(t, 12, sub.AmountUsed)
	var user model.User
	require.NoError(t, db.First(&user, 42).Error)
	assert.Equal(t, 500, user.Quota)
}

func TestNativeLocalLegacyDoesNotChargeCentralFundingAfterAdmissionChange(t *testing.T) {
	db, c, info := legacyLeaseFixture(t)
	require.NoError(t, db.AutoMigrate(&model.UniHubIdentityMapping{}, &model.ProductAuthLifecycle{}))
	info.NativeLocalLegacy = true
	require.NoError(t, db.Create(&model.ProductFundingAccount{UserID: 42, State: "central"}).Error)
	session, apiErr := NewBillingSession(c, info, 20)
	assert.Nil(t, session)
	assert.NotNil(t, apiErr)
	var user model.User
	require.NoError(t, db.First(&user, 42).Error)
	assert.Equal(t, 500, user.Quota)
}

func TestNativeLocalViolationFeeUsesReservationOnce(t *testing.T) {
	for _, mode := range []string{"wallet", "subscription", "token_failure"} {
		t.Run(mode, func(t *testing.T) {
			db, c, info := legacyLeaseFixture(t)
			require.NoError(t, db.AutoMigrate(&model.UniHubIdentityMapping{}, &model.ProductAuthLifecycle{}, &model.SubscriptionPlan{}, &model.UserSubscription{}, &model.SubscriptionPreConsumeRecord{}, &model.Log{}, &model.Channel{}))
			oldLogDB, oldUnit, oldThreshold := model.LOG_DB, common.QuotaPerUnit, common.QuotaRemindThreshold
			model.LOG_DB, common.QuotaPerUnit, common.QuotaRemindThreshold = db, 1000, 0
			t.Cleanup(func() {
				require.Eventually(t, func() bool { return gopool.WorkerCount() == 0 }, 2*time.Second, time.Millisecond)
				model.LOG_DB, common.QuotaPerUnit, common.QuotaRemindThreshold = oldLogDB, oldUnit, oldThreshold
			})
			settings := model_setting.GetGrokSettings()
			oldSettings := *settings
			settings.ViolationDeductionEnabled, settings.ViolationDeductionAmount = true, 0.05
			t.Cleanup(func() { *settings = oldSettings })
			info.NativeLocalLegacy = true
			info.PriceData.GroupRatioInfo.GroupRatio = 1
			info.UserQuota = 500
			if mode == "subscription" {
				require.NoError(t, db.Create(&model.SubscriptionPlan{Id: 1, Title: "plan"}).Error)
				require.NoError(t, db.Create(&model.UserSubscription{Id: 1, UserId: 42, PlanId: 1, Status: "active", AmountTotal: 100, StartTime: time.Now().Unix(), EndTime: time.Now().Add(time.Hour).Unix()}).Error)
				info.UserSetting.BillingPreference = "subscription_only"
			}
			require.NoError(t, db.Create(&model.Channel{Id: 1, Name: "fixture"}).Error)
			info.ChannelMeta = &relaycommon.ChannelMeta{ChannelId: 1}
			session, apiErr := NewBillingSession(c, info, 20)
			require.Nil(t, apiErr)
			info.Billing = session
			if mode == "token_failure" {
				const name = "test:violation-token-failure"
				require.NoError(t, db.Callback().Update().Before("gorm:update").Register(name, func(tx *gorm.DB) {
					if tx.Statement.Table == "tokens" {
						tx.AddError(errors.New("token settlement unavailable"))
					}
				}))
				t.Cleanup(func() { _ = db.Callback().Update().Remove(name) })
			}
			violation := NormalizeViolationFeeError(types.NewError(errors.New(ContentViolatesUsageMarker), types.ErrorCodeInvalidRequest))
			require.True(t, NativeLocalViolationFeeApplies(info, violation))
			var wg sync.WaitGroup
			for range 5 {
				wg.Add(1)
				go func() { defer wg.Done(); ChargeViolationFeeIfNeeded(c, info, violation) }()
			}
			wg.Wait()
			var user model.User
			var token model.Token
			var lease model.ProductLegacyFundingLease
			require.NoError(t, db.First(&user, 42).Error)
			require.NoError(t, db.First(&token, 7).Error)
			require.NoError(t, db.First(&lease, "user_id = ?", 42).Error)
			if mode == "token_failure" {
				assert.Equal(t, 450, user.Quota)
				assert.Equal(t, 180, token.RemainQuota)
				assert.Equal(t, "active", lease.State)
				assert.False(t, ChargeViolationFeeIfNeeded(c, info, violation))
				return
			}
			assert.Equal(t, "settled", lease.State)
			assert.Equal(t, 150, token.RemainQuota)
			if mode == "subscription" {
				var sub model.UserSubscription
				require.NoError(t, db.First(&sub, 1).Error)
				assert.EqualValues(t, 50, sub.AmountUsed)
				assert.Equal(t, 500, user.Quota)
			} else {
				assert.Equal(t, 450, user.Quota)
			}
			var logs int64
			require.NoError(t, db.Model(&model.Log{}).Where("user_id = ? AND type = ?", 42, model.LogTypeConsume).Count(&logs).Error)
			assert.EqualValues(t, 1, logs)
		})
	}
}

func TestNativeLeaseRetainsFenceWhenReserveRollbackFails(t *testing.T) {
	db, c, info := legacyLeaseFixture(t)
	require.NoError(t, db.AutoMigrate(&model.UniHubIdentityMapping{}, &model.ProductAuthLifecycle{}))
	info.NativeLocalLegacy = true
	session, apiErr := NewBillingSession(c, info, 20)
	require.Nil(t, apiErr)
	var tokenFailure atomic.Bool
	var tokenWrites atomic.Int32
	var rollbackWrites atomic.Int32
	const callback = "test:reserve-rollback-failure"
	require.NoError(t, db.Callback().Update().Before("gorm:update").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table == "tokens" && tokenWrites.Add(1) == 1 {
			tokenFailure.Store(true)
			tx.AddError(errors.New("token reserve unavailable"))
		} else if tx.Statement.Table == "users" && tokenFailure.Load() && rollbackWrites.Add(1) == 1 {
			tx.AddError(errors.New("wallet rollback unavailable"))
		}
	}))
	t.Cleanup(func() { _ = db.Callback().Update().Remove(callback) })
	require.Error(t, session.Reserve(30))
	localSession, ok := session.(*BillingSession)
	require.True(t, ok)
	assert.Error(t, session.Reserve(40), "an uncertain reserve must block further reservation")
	assert.Error(t, session.Settle(20), "an uncertain reserve must not close the lease as settled")
	assert.Error(t, localSession.settleViolationFee(20), "an uncertain reserve must block violation settlement")
	session.Refund(c)
	require.Eventually(t, func() bool { return gopool.WorkerCount() == 0 }, 2*time.Second, time.Millisecond)
	var lease model.ProductLegacyFundingLease
	require.NoError(t, db.First(&lease, "user_id = ?", 42).Error)
	assert.Equal(t, "active", lease.State)
}

func TestNativeLeaseRetainsFenceWhenSubscriptionExtraRefundFails(t *testing.T) {
	db, c, info := legacyLeaseFixture(t)
	require.NoError(t, db.AutoMigrate(&model.UniHubIdentityMapping{}, &model.ProductAuthLifecycle{}, &model.UserSubscription{}))
	require.NoError(t, db.Create(&model.UserSubscription{Id: 1, UserId: 42, AmountUsed: 10}).Error)
	info.NativeLocalLegacy = true
	require.NoError(t, model.BeginUnboundNativeFundingLease(context.Background(), "fixture-sub", 42))
	session := &BillingSession{relayInfo: info, funding: &SubscriptionFunding{subscriptionId: 1}, extraReserved: 10, legacyLeaseID: "fixture-sub"}
	info.SubscriptionId = 1
	// The original subscription hold was already returned; fail the separate extra hold.
	session.tokenConsumed = 1
	const callback = "test:subscription-extra-refund-failure"
	require.NoError(t, db.Callback().Update().Before("gorm:update").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table == "user_subscriptions" {
			tx.AddError(errors.New("subscription extra refund unavailable"))
		}
	}))
	t.Cleanup(func() { _ = db.Callback().Update().Remove(callback) })
	session.Refund(c)
	require.Eventually(t, func() bool { return gopool.WorkerCount() == 0 }, 2*time.Second, time.Millisecond)
	var lease model.ProductLegacyFundingLease
	require.NoError(t, db.First(&lease, "id = ?", "fixture-sub").Error)
	assert.Equal(t, "active", lease.State)
}

func TestBillingSessionRejectsSettlementAfterRefund(t *testing.T) {
	for _, runtime := range []string{"true", "false"} {
		t.Run(runtime, func(t *testing.T) {
			db, c, info := legacyLeaseFixture(t)
			t.Setenv("UNIHUB_PRODUCT_RUNTIME_ENABLED", runtime)
			if runtime == "true" {
				require.NoError(t, db.AutoMigrate(&model.UniHubIdentityMapping{}, &model.ProductAuthLifecycle{}))
				info.NativeLocalLegacy = true
			}
			session, apiErr := NewBillingSession(c, info, 20)
			require.Nil(t, apiErr)
			session.Refund(c)
			require.Eventually(t, func() bool {
				var user model.User
				var token model.Token
				return db.First(&user, 42).Error == nil && db.First(&token, 7).Error == nil && user.Quota == 500 && token.RemainQuota == 200
			}, 2*time.Second, time.Millisecond)
			if runtime == "true" {
				require.Eventually(t, func() bool {
					var lease model.ProductLegacyFundingLease
					return db.First(&lease, "user_id = ?", 42).Error == nil && lease.State == "refunded"
				}, 2*time.Second, time.Millisecond)
			}
			require.Error(t, session.Settle(20))
			var user model.User
			var token model.Token
			require.NoError(t, db.First(&user, 42).Error)
			require.NoError(t, db.First(&token, 7).Error)
			assert.Equal(t, 500, user.Quota)
			assert.Equal(t, 200, token.RemainQuota)
			if runtime == "true" {
				var lease model.ProductLegacyFundingLease
				require.NoError(t, db.First(&lease, "user_id = ?", 42).Error)
				assert.Equal(t, "refunded", lease.State)
			}
		})
	}
}

func TestBillingSessionSettlementRemainsIdempotent(t *testing.T) {
	db, c, info := legacyLeaseFixture(t)
	info.UserSetting.BillingPreference = "wallet_only"
	session, apiErr := NewBillingSession(c, info, 20)
	require.Nil(t, apiErr)
	require.NoError(t, session.Settle(12))
	require.NoError(t, session.Settle(12))
	var user model.User
	var token model.Token
	require.NoError(t, db.First(&user, 42).Error)
	require.NoError(t, db.First(&token, 7).Error)
	assert.Equal(t, 488, user.Quota)
	assert.Equal(t, 188, token.RemainQuota)
}
