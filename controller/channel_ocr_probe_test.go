package controller

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func setupPaddleOCRProbeTest(t *testing.T) (*gorm.DB, *model.User, *model.Channel, *atomic.Int32) {
	t.Helper()
	oldPrices := ratio_setting.ModelPrice2JSONString()
	oldDB, oldLogDB := model.DB, model.LOG_DB
	oldRedis, oldMemory := common.RedisEnabled, common.MemoryCacheEnabled
	oldMain, oldLog := common.MainDatabaseType(), common.LogDatabaseType()
	t.Cleanup(func() {
		require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(oldPrices))
		model.DB, model.LOG_DB = oldDB, oldLogDB
		common.RedisEnabled, common.MemoryCacheEnabled = oldRedis, oldMemory
		common.SetDatabaseTypes(oldMain, oldLog)
	})
	db := setupModelListControllerTestDB(t)
	common.MemoryCacheEnabled = false
	require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(`{"ocr-probe-model":0.001}`))
	require.NoError(t, db.AutoMigrate(&model.Log{}))
	user := &model.User{Username: "ocr-probe-user", Group: "default", Status: common.UserStatusEnabled, Quota: 1000000, UsedQuota: 123, RequestCount: 4}
	require.NoError(t, db.Create(user).Error)
	requests := &atomic.Int32{}
	supplier := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"fixture rejected generic probe","type":"invalid_api_key","code":"invalid_api_key"}}`))
	}))
	t.Cleanup(supplier.Close)
	service.InitHttpClient()
	channel := &model.Channel{
		Type: constant.ChannelTypePaddleOCR, Name: "native OCR jobs", Key: "fixture-key", Models: "ocr-probe-model", Group: "default",
		BaseURL: &supplier.URL, Status: common.ChannelStatusEnabled, AutoBan: common.GetPointer(1), ResponseTime: 987, TestTime: 1234, UsedQuota: 456,
	}
	require.NoError(t, db.Create(channel).Error)
	return db, user, channel, requests
}

func TestPaddleOCRProbeRejectsBeforeUserOrCredentialSelection(t *testing.T) {
	_, user, channel, requests := setupPaddleOCRProbeTest(t)
	for _, test := range []struct {
		name   string
		userID int
		pooled bool
	}{
		{name: "configured supplier", userID: user.Id},
		{name: "missing user", userID: 999999},
		{name: "empty account pool", userID: user.Id, pooled: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			candidate := *channel
			candidate.ChannelInfo.IsMultiKey = test.pooled
			if test.pooled {
				candidate.Key = ""
			}
			result := testChannel(context.Background(), &candidate, test.userID, "", "", false)
			require.ErrorContains(t, result.localErr, "PaddleOCR channel test is not supported")
			assert.Nil(t, result.context, "unsupported native jobs must stop before relay context and account selection")
			assert.Nil(t, result.newAPIError, "unsupported is a local result, not invalid supplier credentials")
		})
	}
	assert.Zero(t, requests.Load(), "generic probes must never reach the native OCR supplier")
}

func TestPaddleOCRManualProbeReturnsUnsupportedWithoutHealthOrBillingWrites(t *testing.T) {
	db, user, channel, requests := setupPaddleOCRProbeTest(t)
	for _, userID := range []int{0, user.Id} {
		recorder := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(recorder)
		ctx.Params = gin.Params{{Key: "id", Value: fmt.Sprint(channel.Id)}}
		ctx.Request = httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/channel/test/%d", channel.Id), nil)
		if userID != 0 {
			ctx.Set("id", userID)
		}
		TestChannel(ctx)
		var response struct {
			Success   bool    `json:"success"`
			Message   string  `json:"message"`
			Time      float64 `json:"time"`
			ErrorCode string  `json:"error_code"`
		}
		require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
		assert.Equal(t, http.StatusOK, recorder.Code)
		assert.False(t, response.Success)
		assert.Contains(t, response.Message, "PaddleOCR channel test is not supported")
		assert.Zero(t, response.Time)
		assert.Empty(t, response.ErrorCode)
	}
	assert.Zero(t, requests.Load())
	assertPaddleOCRProbeStateUnchanged(t, db, user, channel)
}

func TestPaddleOCRBulkProbeSkipsHealthCountsAndWrites(t *testing.T) {
	for _, mode := range []string{operation_setting.ChannelTestModeScheduledAll, operation_setting.ChannelTestModePassiveRecovery} {
		t.Run(mode, func(t *testing.T) {
			db, user, channel, requests := setupPaddleOCRProbeTest(t)
			oldDisable, oldEnable := common.AutomaticDisableChannelEnabled, common.AutomaticEnableChannelEnabled
			common.AutomaticDisableChannelEnabled, common.AutomaticEnableChannelEnabled = true, true
			t.Cleanup(func() {
				common.AutomaticDisableChannelEnabled, common.AutomaticEnableChannelEnabled = oldDisable, oldEnable
			})
			channels := []*model.Channel{}
			expectedChannels := []model.Channel{}
			for _, status := range []int{common.ChannelStatusEnabled, common.ChannelStatusAutoDisabled, common.ChannelStatusManuallyDisabled} {
				candidate := *channel
				candidate.Id = 0
				candidate.Status = status
				require.NoError(t, db.Create(&candidate).Error)
				channels = append(channels, &candidate)
				expectedChannels = append(expectedChannels, candidate)
			}
			selected := selectChannelsForAutomaticTest(channels, mode)
			summary := performChannelTests(context.Background(), selected, user.Id, true, nil)
			assert.Equal(t, channelTestSummary{}, summary, "unsupported OCR jobs must not become successful or failed health tests")
			assert.Zero(t, requests.Load())
			for index := range expectedChannels {
				assertPaddleOCRProbeStateUnchanged(t, db, user, &expectedChannels[index])
			}
		})
	}
}

func assertPaddleOCRProbeStateUnchanged(t *testing.T, db *gorm.DB, user *model.User, channel *model.Channel) {
	t.Helper()
	var stored model.Channel
	require.NoError(t, db.First(&stored, channel.Id).Error)
	assert.Equal(t, channel.Status, stored.Status)
	assert.Equal(t, channel.ResponseTime, stored.ResponseTime)
	assert.Equal(t, channel.TestTime, stored.TestTime)
	assert.Equal(t, channel.UsedQuota, stored.UsedQuota)
	var storedUser model.User
	require.NoError(t, db.First(&storedUser, user.Id).Error)
	assert.Equal(t, user.Quota, storedUser.Quota)
	assert.Equal(t, user.UsedQuota, storedUser.UsedQuota)
	assert.Equal(t, user.RequestCount, storedUser.RequestCount)
	var logs int64
	require.NoError(t, db.Model(&model.Log{}).Count(&logs).Error)
	assert.Zero(t, logs)
}
