package service

import (
	"context"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/metering"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"path/filepath"
	"testing"
)

func TestNativePlatformEvidenceSurvivesDisconnectWithoutLocalDatabase(t *testing.T) {
	old := model.DB
	model.DB = nil
	t.Cleanup(func() { model.DB = old })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	called := false
	info := &relaycommon.RelayInfo{PlatformMode: true, NativeBilling: &relaycommon.NativeBillingContext{Platform: true, Observe: func(ctx context.Context, u metering.Usage) error {
		called = true
		require.NoError(t, ctx.Err())
		assert.Equal(t, "missing", u.State)
		return nil
	}}}
	require.NoError(t, RecordNativeUsageForRelay(ctx, info, metering.Usage{State: "missing"}))
	assert.True(t, called)
	info.NativeBilling.Observe = nil
	require.Error(t, RecordNativeUsageForRelay(ctx, info, metering.Usage{State: "missing"}))
	info.NativeBilling = nil
	require.Error(t, RecordNativeUsageForRelay(ctx, info, metering.Usage{State: "missing"}))
}

func TestNativeLocalTokenFlowUsesFrozenReservationAndReceiptOnly(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "native.db")), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	old, redisEnabled := model.DB, common.RedisEnabled
	model.DB = db
	common.RedisEnabled = false
	t.Cleanup(func() { model.DB = old; common.RedisEnabled = redisEnabled })
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.Token{}, &model.Channel{}, &model.SubscriptionPlan{}, &model.UserSubscription{}, &model.OCRJob{}, &model.OCROperation{}, &model.OCRAuditEvent{}, &model.NativeBilling{}))
	u := model.User{Username: "native-local", Quota: 10000, Status: common.UserStatusEnabled}
	require.NoError(t, db.Create(&u).Error)
	token := model.Token{UserId: u.Id, Key: "native-test", RemainQuota: 10000, Status: common.TokenStatusEnabled, ExpiredTime: -1}
	require.NoError(t, db.Create(&token).Error)
	ch := model.Channel{Name: "native"}
	require.NoError(t, db.Create(&ch).Error)
	price := metering.Price{Version: "metered_v2", Operation: "chat", Policy: "provider_reported_v1", Terms: []metering.Term{{Key: "input", Unit: "token", Price: metering.Rational{Numerator: "1", Denominator: "1"}}, {Key: "output", Unit: "token", Price: metering.Rational{Numerator: "1", Denominator: "1"}}}}
	info := &relaycommon.RelayInfo{UserId: u.Id, TokenId: token.Id, Request: &dto.GeneralOpenAIRequest{MaxTokens: common.GetPointer(uint(20))}}
	info.SetEstimatePromptTokens(10)
	reservation, err := NativeTokenReservation(info, price)
	require.NoError(t, err)
	assert.Equal(t, "incomplete", reservation.State)
	assert.Equal(t, "20", reservation.Measurements[1].Quantity.Value)
	a := model.NativeAdmission{CallID: "synthetic-chat", UserID: u.Id, TokenID: token.Id, ChannelID: ch.Id, Model: "native", Group: "default", Preference: "wallet_only", Price: price, GroupRatio: "1", QuotaPerUnit: "1", Reservation: reservation}
	ctx := context.Background()
	require.NoError(t, BeginNativeBillingForRelay(ctx, info, a))
	require.Nil(t, info.Billing)
	require.NoError(t, MarkNativeBillingDispatched(ctx, info))
	require.NoError(t, db.First(&u, u.Id).Error)
	assert.Equal(t, 9970, u.Quota)
	actual := reservation
	actual.State = "confirmed"
	actual.Source = "openai_raw"
	actual.Measurements = []metering.Measurement{{Key: "input", Unit: "token", Quantity: metering.Quantity{Value: "4"}}, {Key: "output", Unit: "token", Quantity: metering.Quantity{Value: "2"}}}
	require.NoError(t, RecordNativeUsageForRelay(ctx, info, actual))
	require.NoError(t, RecordNativeUsageForRelay(ctx, info, actual))
	require.NoError(t, db.First(&u, u.Id).Error)
	assert.Equal(t, 9994, u.Quota)
	assert.Equal(t, 6, u.UsedQuota)
	assert.Equal(t, 1, u.RequestCount)
	var journal model.NativeBilling
	require.NoError(t, db.First(&journal, "id = ?", info.NativeBilling.CallID).Error)
	assert.Equal(t, "settled", journal.State)
	// A new process must recover confirmed evidence and project its receipt
	// before StartNativeBillingRecovery returns, without waiting for a timer.
	require.NoError(t, db.AutoMigrate(&model.Log{}, &model.OCRLogReceipt{}))
	oldLog, oldConsume := model.LOG_DB, common.LogConsumeEnabled
	model.LOG_DB = db
	common.LogConsumeEnabled = true
	t.Cleanup(func() { model.LOG_DB = oldLog; common.LogConsumeEnabled = oldConsume })
	a.CallID = "startup-recovery"
	pending, err := BeginNativeBilling(ctx, a)
	require.NoError(t, err)
	require.NoError(t, RecordNativeUsage(ctx, pending.ID, actual))
	stop := StartNativeBillingRecovery(ctx)
	defer stop()
	var restored model.NativeBilling
	require.NoError(t, db.First(&restored, "id = ?", pending.ID).Error)
	assert.Equal(t, "settled", restored.State)
	var receipts int64
	require.NoError(t, db.Model(&model.OCRLogReceipt{}).Count(&receipts).Error)
	assert.EqualValues(t, 2, receipts)

}
