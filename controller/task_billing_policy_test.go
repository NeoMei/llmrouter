package controller

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/QuantumNous/new-api/types"
	"github.com/bytedance/gopkg/util/gopool"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestTaskBillingFailurePreservesLocalErrorPolicy(t *testing.T) {
	gin.SetMode(gin.TestMode)
	previousDisable := common.AutomaticDisableChannelEnabled
	previousRanges := operation_setting.AutomaticDisableStatusCodeRanges
	common.AutomaticDisableChannelEnabled = true
	operation_setting.AutomaticDisableStatusCodeRanges = []operation_setting.StatusCodeRange{{Start: 403, End: 599}}
	t.Cleanup(func() {
		common.AutomaticDisableChannelEnabled = previousDisable
		operation_setting.AutomaticDisableStatusCodeRanges = previousRanges
	})
	for _, status := range []int{http.StatusForbidden, http.StatusInternalServerError, http.StatusTooManyRequests} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			apiErr := types.NewErrorWithStatusCode(errors.New("local billing failure"), types.ErrorCodeInsufficientUserQuota, status, types.ErrOptionWithSkipRetry(), types.ErrOptionWithNoRecordErrorLog())
			taskErr := service.TaskErrorFromAPIError(apiErr)
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			assert.True(t, taskErr.LocalError)
			assert.False(t, shouldRetryTaskRelay(c, 1, taskErr, 2), "local billing errors must never cause another attempt")
			var preserved *types.NewAPIError
			require.ErrorAs(t, taskErr.Error, &preserved)
			assert.Same(t, apiErr, preserved)
			assert.True(t, types.IsSkipRetryError(preserved))
			assert.False(t, types.IsRecordErrorLog(preserved))
			channelErr := types.NewOpenAIError(taskErr.Error, types.ErrorCodeBadResponseStatusCode, taskErr.StatusCode)
			assert.False(t, service.ShouldDisableChannel(channelErr), "a wallet failure cannot disable a healthy channel")
		})
	}
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	infrastructureErr := service.TaskErrorWrapperLocal(errors.New("temporary local setup failure"), "setup_failed", http.StatusInternalServerError)
	assert.True(t, shouldRetryTaskRelay(c, 1, infrastructureErr, 2), "preserve existing infrastructure retry behavior without a typed skip policy")
}

func TestRelayTaskInsufficientWalletDoesNotBlameChannel(t *testing.T) {
	gin.SetMode(gin.TestMode)
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
	common.RetryTimes, common.AutomaticDisableChannelEnabled, constant.ErrorLogEnabled = 2, true, true
	operation_setting.AutomaticDisableStatusCodeRanges = []operation_setting.StatusCodeRange{{Start: 403, End: 403}}
	t.Cleanup(func() {
		require.Eventually(t, func() bool { return gopool.WorkerCount() == 0 }, 2*time.Second, time.Millisecond, "drain asynchronous channel/accounting work before restoring globals")
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
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.Token{}, &model.Channel{}, &model.Log{}))
	require.NoError(t, db.Create(&model.User{Id: 971, Username: "task-policy", Quota: 1}).Error)
	require.NoError(t, db.Create(&model.Token{Id: 972, UserId: 971, Key: "task-policy", RemainQuota: 100000}).Error)
	autoBan := 1
	require.NoError(t, db.Create(&model.Channel{Id: 973, Name: "healthy-task", Type: constant.ChannelTypeSunoAPI, Status: common.ChannelStatusEnabled, AutoBan: &autoBan}).Error)
	require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(`{"task-policy-model":0.04}`))
	require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(`{"default":1}`))
	require.NoError(t, ratio_setting.UpdateGroupGroupRatioByJSONString(`{}`))
	var submitted atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		submitted.Add(1)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"code":"success","data":"job"}`))
	}))
	defer upstream.Close()
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest("POST", "/suno/submit/LYRICS", strings.NewReader(`{"prompt":"hello"}`))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Params = gin.Params{{Key: "action", Value: constant.SunoActionLyrics}}
	c.Set("platform", string(constant.TaskPlatformSuno))
	c.Set("auto_ban", true)
	common.SetContextKey(c, constant.ContextKeyUserId, 971)
	common.SetContextKey(c, constant.ContextKeyTokenId, 972)
	common.SetContextKey(c, constant.ContextKeyTokenKey, "task-policy")
	common.SetContextKey(c, constant.ContextKeyUserGroup, "default")
	common.SetContextKey(c, constant.ContextKeyUsingGroup, "default")
	common.SetContextKey(c, constant.ContextKeyOriginalModel, "task-policy-model")
	common.SetContextKey(c, constant.ContextKeyUserSetting, dto.UserSetting{BillingPreference: "wallet_only"})
	common.SetContextKey(c, constant.ContextKeyChannelId, 973)
	common.SetContextKey(c, constant.ContextKeyChannelType, constant.ChannelTypeSunoAPI)
	common.SetContextKey(c, constant.ContextKeyChannelBaseUrl, upstream.URL)
	RelayTask(c)
	require.Eventually(t, func() bool { return gopool.WorkerCount() == 0 }, 2*time.Second, time.Millisecond)
	assert.Equal(t, http.StatusForbidden, recorder.Code)
	assert.Zero(t, submitted.Load())
	assert.Contains(t, recorder.Body.String(), "insufficient_user_quota")
	assert.Len(t, c.GetStringSlice("use_channel"), 1, "billing failure must stop the controller retry loop")
	var channel model.Channel
	require.NoError(t, db.First(&channel, 973).Error)
	assert.Equal(t, common.ChannelStatusEnabled, channel.Status)
	var errorsCount int64
	require.NoError(t, db.Model(&model.Log{}).Where("type = ?", model.LogTypeError).Count(&errorsCount).Error)
	assert.Zero(t, errorsCount)
}
