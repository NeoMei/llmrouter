package model

import (
	"errors"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestSubscriptionRefundIsAtomicAndIdempotent(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(t.TempDir()+"/subscriptions.db"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&UserSubscription{}, &SubscriptionPreConsumeRecord{}))
	old := DB
	DB = db
	t.Cleanup(func() { DB = old })
	require.NoError(t, db.Create(&UserSubscription{Id: 1, UserId: 1, AmountUsed: 70}).Error)
	require.NoError(t, db.Create(&SubscriptionPreConsumeRecord{RequestId: "refund-1", UserId: 1, UserSubscriptionId: 1, PreConsumed: 20, Status: "consumed"}).Error)
	const hook = "test:fail-refund-record"
	require.NoError(t, db.Callback().Update().Before("gorm:update").Register(hook, func(tx *gorm.DB) {
		if tx.Statement.Table == "subscription_pre_consume_records" {
			tx.AddError(errors.New("record write failure"))
		}
	}))
	require.Error(t, RefundSubscriptionPreConsume("refund-1"))
	require.NoError(t, db.Callback().Update().Remove(hook))
	var sub UserSubscription
	require.NoError(t, db.First(&sub, 1).Error)
	require.EqualValues(t, 70, sub.AmountUsed)
	require.NoError(t, RefundSubscriptionPreConsume("refund-1"))
	require.NoError(t, RefundSubscriptionPreConsume("refund-1"))
	require.NoError(t, db.First(&sub, 1).Error)
	require.EqualValues(t, 50, sub.AmountUsed)
}
