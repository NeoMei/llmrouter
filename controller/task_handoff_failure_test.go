package controller

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/bytedance/gopkg/util/gopool"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// setupTaskHandoffFixture builds the minimal durable state for a real Suno
// task submission. The task table is omitted when migrateTask is false so the
// final handoff fails after the upstream job has already been accepted.
func setupTaskHandoffFixture(t *testing.T, migrateTask bool) (*httptest.ResponseRecorder, *gin.Context) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	t.Setenv("UNIHUB_PRODUCT_RUNTIME_ENABLED", "false")
	savedDB, savedLogDB := model.DB, model.LOG_DB
	savedRedis, savedBatch := common.RedisEnabled, common.BatchUpdateEnabled
	savedMemory := common.MemoryCacheEnabled
	savedMain, savedLog := common.MainDatabaseType(), common.LogDatabaseType()
	savedRetries, savedDisable, savedErrors := common.RetryTimes, common.AutomaticDisableChannelEnabled, constant.ErrorLogEnabled
	savedRanges := operation_setting.AutomaticDisableStatusCodeRanges
	savedPrices := ratio_setting.ModelPrice2JSONString()
	savedGroups := ratio_setting.GroupRatio2JSONString()
	savedSpecialGroups := ratio_setting.GroupGroupRatio2JSONString()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	model.DB, model.LOG_DB = db, db
	common.SetDatabaseTypes(common.DatabaseTypeSQLite, common.DatabaseTypeSQLite)
	common.RedisEnabled, common.BatchUpdateEnabled = false, false
	common.MemoryCacheEnabled = false
	common.RetryTimes, common.AutomaticDisableChannelEnabled, constant.ErrorLogEnabled = 0, true, false
	operation_setting.AutomaticDisableStatusCodeRanges = []operation_setting.StatusCodeRange{{Start: 400, End: 599}}
	t.Cleanup(func() {
		require.Eventually(t, func() bool { return gopool.WorkerCount() == 0 }, 2*time.Second, time.Millisecond)
		model.DB, model.LOG_DB = savedDB, savedLogDB
		common.RedisEnabled, common.BatchUpdateEnabled = savedRedis, savedBatch
		common.MemoryCacheEnabled = savedMemory
		common.SetDatabaseTypes(savedMain, savedLog)
		common.RetryTimes, common.AutomaticDisableChannelEnabled, constant.ErrorLogEnabled = savedRetries, savedDisable, savedErrors
		operation_setting.AutomaticDisableStatusCodeRanges = savedRanges
		require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(savedPrices))
		require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(savedGroups))
		require.NoError(t, ratio_setting.UpdateGroupGroupRatioByJSONString(savedSpecialGroups))
		require.NoError(t, sqlDB.Close())
	})
	models := []any{&model.User{}, &model.Token{}, &model.Channel{}, &model.Log{}, &model.Ability{}}
	if migrateTask {
		models = append(models, &model.Task{})
	}
	require.NoError(t, db.AutoMigrate(models...))
	require.NoError(t, db.Create(&model.User{Id: 981, Username: "task-handoff", Quota: 100000, Status: common.UserStatusEnabled}).Error)
	require.NoError(t, db.Create(&model.Token{Id: 982, UserId: 981, Key: "task-handoff", RemainQuota: 100000, ExpiredTime: -1, Status: common.TokenStatusEnabled}).Error)
	require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(`{"task-handoff-model":0.04}`))
	require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(`{"default":1}`))
	require.NoError(t, ratio_setting.UpdateGroupGroupRatioByJSONString(`{}`))
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":"success","data":"upstream-job"}`))
	}))
	t.Cleanup(upstream.Close)
	require.NoError(t, db.Create(&model.Channel{Id: 983, Name: "task-handoff", Type: constant.ChannelTypeSunoAPI, Status: common.ChannelStatusEnabled, Models: "task-handoff-model", Group: "default"}).Error)
	require.NoError(t, db.Create(&model.Ability{ChannelId: 983, Group: "default", Model: "task-handoff-model", Enabled: true}).Error)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest("POST", "/suno/submit/LYRICS", strings.NewReader(`{"prompt":"hello"}`))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Params = gin.Params{{Key: "action", Value: constant.SunoActionLyrics}}
	c.Set("platform", string(constant.TaskPlatformSuno))
	common.SetContextKey(c, constant.ContextKeyUserId, 981)
	common.SetContextKey(c, constant.ContextKeyTokenId, 982)
	common.SetContextKey(c, constant.ContextKeyTokenKey, "task-handoff")
	common.SetContextKey(c, constant.ContextKeyUserGroup, "default")
	common.SetContextKey(c, constant.ContextKeyUsingGroup, "default")
	common.SetContextKey(c, constant.ContextKeyOriginalModel, "task-handoff-model")
	common.SetContextKey(c, constant.ContextKeyUserSetting, dto.UserSetting{BillingPreference: "wallet_only"})
	common.SetContextKey(c, constant.ContextKeyChannelId, 983)
	common.SetContextKey(c, constant.ContextKeyChannelType, constant.ChannelTypeSunoAPI)
	common.SetContextKey(c, constant.ContextKeyChannelKey, "upstream-key")
	common.SetContextKey(c, constant.ContextKeyChannelBaseUrl, upstream.URL)
	return recorder, c
}

func TestRelayTaskInsertFailureWithholdsSuccessAndRefund(t *testing.T) {
	recorder, c := setupTaskHandoffFixture(t, false)
	oldSettle := settleTaskBillingFn
	settleTaskBillingFn = func(*gin.Context, *relaycommon.RelayInfo, int) error { return nil }
	t.Cleanup(func() { settleTaskBillingFn = oldSettle })
	RelayTask(c)
	require.Equal(t, http.StatusServiceUnavailable, recorder.Code)
	require.Contains(t, recorder.Body.String(), "task handoff requires reconciliation")
	var user model.User
	require.NoError(t, model.DB.First(&user, 981).Error)
	require.Less(t, user.Quota, 100000, "a failed handoff keeps the reservation held")
}

func TestRelayTaskSettlementFailureWithholdsSuccess(t *testing.T) {
	recorder, c := setupTaskHandoffFixture(t, true)
	oldSettle := settleTaskBillingFn
	settleTaskBillingFn = func(*gin.Context, *relaycommon.RelayInfo, int) error {
		return errors.New("settlement ledger unavailable")
	}
	t.Cleanup(func() { settleTaskBillingFn = oldSettle })
	RelayTask(c)
	require.Equal(t, http.StatusServiceUnavailable, recorder.Code)
	require.Contains(t, recorder.Body.String(), "billing settlement requires reconciliation")
	var count int64
	require.NoError(t, model.DB.Model(&model.Task{}).Count(&count).Error)
	require.Zero(t, count, "an uncertain settlement must not create a pollable task")
}

func TestRelayTaskSuccessCommitsBufferedResponse(t *testing.T) {
	recorder, c := setupTaskHandoffFixture(t, true)
	oldSettle := settleTaskBillingFn
	settleTaskBillingFn = func(*gin.Context, *relaycommon.RelayInfo, int) error { return nil }
	t.Cleanup(func() { settleTaskBillingFn = oldSettle })
	RelayTask(c)
	require.Equal(t, http.StatusOK, recorder.Code)
	require.Contains(t, recorder.Body.String(), "task_")
	var count int64
	require.NoError(t, model.DB.Model(&model.Task{}).Count(&count).Error)
	require.EqualValues(t, 1, count)
}
