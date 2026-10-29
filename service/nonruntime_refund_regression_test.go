package service

import (
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/model"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type refundSubscriptionFixture struct{}

func (refundSubscriptionFixture) Source() string       { return BillingSourceSubscription }
func (refundSubscriptionFixture) PreConsume(int) error { return nil }
func (refundSubscriptionFixture) Settle(int) error     { return nil }
func (refundSubscriptionFixture) Refund() error        { return nil }

func TestNonRuntimeSubscriptionExtraRefundFailureStillRestoresTokenOnce(t *testing.T) {
	db, c, info := legacyLeaseFixture(t)
	t.Setenv("UNIHUB_PRODUCT_RUNTIME_ENABLED", "false")
	require.NoError(t, db.AutoMigrate(&model.UserSubscription{}))
	require.NoError(t, db.Create(&model.UserSubscription{Id: 1, UserId: 42, AmountUsed: 10}).Error)
	require.NoError(t, db.Model(&model.Token{}).Where("id = ?", 7).Update("remain_quota", 180).Error)
	const hook = "test:nonruntime-sub-extra-failure"
	require.NoError(t, db.Callback().Update().Before("gorm:update").Register(hook, func(tx *gorm.DB) {
		if tx.Statement.Table == "user_subscriptions" {
			tx.AddError(errors.New("extra refund unavailable"))
		}
	}))
	t.Cleanup(func() { _ = db.Callback().Update().Remove(hook) })
	info.SubscriptionId = 1
	session := &BillingSession{relayInfo: info, funding: refundSubscriptionFixture{}, tokenConsumed: 20, extraReserved: 10}
	session.Refund(c)
	require.Eventually(t, func() bool {
		var token model.Token
		return db.First(&token, 7).Error == nil && token.RemainQuota == 200
	}, 2*time.Second, time.Millisecond)
	session.Refund(c)
	var token model.Token
	require.NoError(t, db.First(&token, 7).Error)
	require.Equal(t, 200, token.RemainQuota)
	var subscription model.UserSubscription
	require.NoError(t, db.First(&subscription, 1).Error)
	require.EqualValues(t, 10, subscription.AmountUsed)
}

func TestNonRuntimeReserveRollbackFailureRefundsOnlyKnownOriginalOnce(t *testing.T) {
	db, c, info := legacyLeaseFixture(t)
	t.Setenv("UNIHUB_PRODUCT_RUNTIME_ENABLED", "false")
	sessionValue, err := NewBillingSession(c, info, 20)
	require.Nil(t, err)
	session := sessionValue.(*BillingSession)
	var tokenFailed atomic.Bool
	var userFailed atomic.Bool
	const hook = "test:nonruntime-reserve-compensation-failure"
	require.NoError(t, db.Callback().Update().Before("gorm:update").Register(hook, func(tx *gorm.DB) {
		if tx.Statement.Table == "tokens" && !tokenFailed.Swap(true) {
			tx.AddError(errors.New("token reserve unavailable"))
		}
		if tx.Statement.Table == "users" && tokenFailed.Load() && !userFailed.Swap(true) {
			tx.AddError(errors.New("funding rollback unavailable"))
		}
	}))
	t.Cleanup(func() { _ = db.Callback().Update().Remove(hook) })
	require.Error(t, session.Reserve(30))
	session.Refund(c)
	require.Eventually(t, func() bool {
		var token model.Token
		var user model.User
		return db.First(&token, 7).Error == nil && db.First(&user, 42).Error == nil && token.RemainQuota == 200 && user.Quota == 490
	}, 2*time.Second, time.Millisecond)
	session.Refund(c)
	var user model.User
	require.NoError(t, db.First(&user, 42).Error)
	require.Equal(t, 490, user.Quota)
}
