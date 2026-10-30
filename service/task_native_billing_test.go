package service

import (
	"context"
	"fmt"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/metering"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"path/filepath"
	"testing"
)

func TestNativeTaskRecoveryPagesPastMeasurementHolds(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "tasks.db")), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	old, observer, release := model.DB, PlatformTaskObserver, PlatformTaskRelease
	model.DB = db
	t.Cleanup(func() {
		model.DB = old
		PlatformTaskObserver = observer
		PlatformTaskRelease = release
		require.NoError(t, sqlDB.Close())
	})
	require.NoError(t, db.AutoMigrate(&model.Task{}))
	p := metering.Price{Version: "metered_v2", Operation: "video", Policy: "sora_completed_seconds_v1", Terms: []metering.Term{{Key: "video", Unit: "video_second", Price: metering.Rational{Numerator: "1", Denominator: "1"}}}}
	observed := map[string]bool{}
	PlatformTaskObserver = func(_ context.Context, call, _ string, _ metering.Usage) error { observed[call] = true; return nil }
	for i := 0; i < 101; i++ {
		u := metering.Usage{Version: "metered_v2", Operation: "video", State: "missing", Source: p.Policy}
		if i == 100 {
			u.State = "confirmed"
			u.Measurements = []metering.Measurement{{Key: "video", Unit: "video_second", Quantity: metering.Quantity{Value: "5"}}}
		}
		raw, err := common.Marshal(u)
		require.NoError(t, err)
		task := model.Task{TaskID: fmt.Sprint(i), FundingOwner: "platform", NativePending: true, NativeEvidence: string(raw), Status: model.TaskStatusSuccess, PrivateData: model.TaskPrivateData{BillingContext: &model.TaskBillingContext{NativePrice: &p, PlatformCallID: fmt.Sprint(i)}}}
		require.NoError(t, db.Create(&task).Error)
	}
	recoverNativeTasks(context.Background())
	assert.True(t, observed["100"])
	var last model.Task
	require.NoError(t, db.Where("task_id = ?", "100").First(&last).Error)
	assert.False(t, last.NativePending)
	// A verified no-output failure releases and clears the task outbox once.
	releases := 0
	PlatformTaskRelease = func(_ context.Context, _, _ string) error { releases++; return nil }
	failed := model.Task{TaskID: "failed", FundingOwner: "platform", NativePending: true, Status: model.TaskStatusFailure, PrivateData: last.PrivateData}
	require.NoError(t, prepareNativeTaskIntent(&failed, []byte(`{"status":"failed","videos":[]}`)))
	require.NoError(t, db.Create(&failed).Error)
	require.NoError(t, settleNativeTask(context.Background(), &failed))
	require.NoError(t, settleNativeTask(context.Background(), &failed))
	assert.Equal(t, 1, releases)
	assert.False(t, failed.NativePending)

	// A losing terminal observation is retained, keeps recovery active, and blocks
	// another observer settlement even when the caller has a stale task snapshot.
	stale := last
	require.NoError(t, model.RecordNativeTaskConflict(last.ID, `{"state":"invalid"}`))
	require.Error(t, settleNativeTask(context.Background(), &stale))
	require.NoError(t, db.First(&last, last.ID).Error)
	assert.NotEmpty(t, last.NativeConflictEvidence)
	assert.True(t, last.NativePending)
}

func TestNativeTaskConfirmedNoOutputReleasesLocalHoldOnce(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "native-failure.db")), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	old, redisEnabled := model.DB, common.RedisEnabled
	model.DB, common.RedisEnabled = db, false
	t.Cleanup(func() { model.DB, common.RedisEnabled = old, redisEnabled; require.NoError(t, sqlDB.Close()) })
	require.NoError(t, db.AutoMigrate(&model.Task{}, &model.User{}, &model.Token{}, &model.Channel{}, &model.SubscriptionPlan{}, &model.UserSubscription{}, &model.OCRJob{}, &model.OCROperation{}, &model.OCRAuditEvent{}, &model.NativeBilling{}))
	user := model.User{Username: "native-video", Quota: 100, Status: common.UserStatusEnabled}
	require.NoError(t, db.Create(&user).Error)
	token := model.Token{UserId: user.Id, Key: "native-video", RemainQuota: 100, Status: common.TokenStatusEnabled, ExpiredTime: -1}
	require.NoError(t, db.Create(&token).Error)
	channel := model.Channel{Name: "native-video"}
	require.NoError(t, db.Create(&channel).Error)
	price := metering.Price{Version: "metered_v2", Operation: "video", Policy: "sora_completed_seconds_v1", Terms: []metering.Term{{Key: "video", Unit: "video_second", Price: metering.Rational{Numerator: "1", Denominator: "1"}}}}
	reservation := metering.Usage{Version: "metered_v2", Operation: "video", State: "incomplete", Source: "reservation", Measurements: []metering.Measurement{{Key: "video", Unit: "video_second", Quantity: metering.Quantity{Value: "5"}}}}
	ctx := context.Background()
	journal, err := BeginNativeBilling(ctx, model.NativeAdmission{CallID: "failed-video", UserID: user.Id, TokenID: token.Id, ChannelID: channel.Id, Model: "video", Group: "default", Preference: "wallet_only", Price: price, GroupRatio: "1", QuotaPerUnit: "1", Reservation: reservation})
	require.NoError(t, err)
	task := model.Task{TaskID: "failed-video", FundingOwner: "native", Status: model.TaskStatusFailure, PrivateData: model.TaskPrivateData{BillingContext: &model.TaskBillingContext{NativePrice: &price, NativeCallID: journal.ID}}}
	require.NoError(t, prepareNativeTaskIntent(&task, []byte(`{"status":"failed","videos":[]}`)))
	require.NoError(t, db.Create(&task).Error)
	require.NoError(t, settleNativeTask(ctx, &task))
	require.NoError(t, settleNativeTask(ctx, &task))
	require.NoError(t, db.First(&user, user.Id).Error)
	require.NoError(t, db.First(&token, token.Id).Error)
	require.NoError(t, db.First(&journal, "id = ?", journal.ID).Error)
	assert.Equal(t, 100, user.Quota)
	assert.Equal(t, 100, token.RemainQuota)
	assert.Zero(t, user.UsedQuota)
	assert.Equal(t, "released", journal.State)
	assert.False(t, task.NativePending)
}

func TestWanUnknownEvidenceRecoveryKeepsPlatformHold(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "wan-unknown.db")), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	old, observer, release := model.DB, PlatformTaskObserver, PlatformTaskRelease
	model.DB = db
	t.Cleanup(func() {
		model.DB, PlatformTaskObserver, PlatformTaskRelease = old, observer, release
		require.NoError(t, sqlDB.Close())
	})
	require.NoError(t, db.AutoMigrate(&model.Task{}))
	p := metering.Price{Version: "metered_v2", Operation: "video", Policy: "ali_wan26_t2v_billable_duration_v1", Terms: []metering.Term{
		{Key: "video_720p", Unit: "video_second", Price: metering.Rational{Numerator: "3", Denominator: "5"}},
		{Key: "video_1080p", Unit: "video_second", Price: metering.Rational{Numerator: "1", Denominator: "1"}},
	}}
	for name, body := range map[string]string{
		"expired":       `{"output":{"task_status":"UNKNOWN"}}`,
		"contradictory": `{"output":{"task_status":"CANCELED"},"usage":{"video_count":1}}`,
	} {
		task := model.Task{TaskID: name, FundingOwner: "platform", Status: model.TaskStatusFailure, PrivateData: model.TaskPrivateData{BillingContext: &model.TaskBillingContext{NativePrice: &p, PlatformCallID: name, VideoResolution: "video_720p"}}}
		require.NoError(t, prepareNativeTaskIntent(&task, []byte(body)))
		require.NoError(t, db.Create(&task).Error)
	}
	var releases int
	var observations int
	PlatformTaskRelease = func(context.Context, string, string) error { releases++; return nil }
	PlatformTaskObserver = func(_ context.Context, _, _ string, usage metering.Usage) error {
		observations++
		assert.NotEqual(t, "provider_confirmed_no_output", usage.Source)
		return nil
	}
	recoverNativeTasks(context.Background())
	assert.Zero(t, releases)
	assert.Equal(t, 2, observations)
	var pending int64
	require.NoError(t, db.Model(&model.Task{}).Where("native_pending = ?", true).Count(&pending).Error)
	assert.EqualValues(t, 2, pending)
}
