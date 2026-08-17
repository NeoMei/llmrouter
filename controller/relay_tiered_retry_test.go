package controller

import (
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/billingexpr"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type relayReserveFailureSpy struct {
	reserved int
	err      error
	calls    []int
}

func (s *relayReserveFailureSpy) Reserve(target int) error {
	s.calls = append(s.calls, target)
	return s.err
}

func (s *relayReserveFailureSpy) GetPreConsumedQuota() int { return s.reserved }
func (s *relayReserveFailureSpy) Settle(int) error         { return nil }
func (s *relayReserveFailureSpy) Refund(*gin.Context)      {}
func (s *relayReserveFailureSpy) NeedsRefund() bool        { return false }

func setupTieredRelayBillingDB(t *testing.T) *gorm.DB {
	t.Helper()
	previousDB := model.DB
	previousType := common.MainDatabaseType()
	previousRedis := common.RedisEnabled
	dsn := "file:relay_tiered_billing_" + strings.NewReplacer("/", "_", " ", "_").Replace(t.Name()) + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.Token{}))
	model.DB = db
	common.SetMainDatabaseType(common.DatabaseTypeSQLite)
	common.RedisEnabled = false
	t.Cleanup(func() {
		model.DB = previousDB
		common.SetMainDatabaseType(previousType)
		common.RedisEnabled = previousRedis
		require.NoError(t, sqlDB.Close())
	})
	return db
}

func createTieredRelayWalletFixtures(t *testing.T, db *gorm.DB, userID, tokenID, walletQuota, tokenQuota int, tokenKey string) {
	t.Helper()
	require.NoError(t, db.Create(&model.User{Id: userID, Username: tokenKey, Quota: walletQuota, Status: common.UserStatusEnabled}).Error)
	require.NoError(t, db.Create(&model.Token{
		Id: tokenID, UserId: userID, Key: tokenKey, Name: tokenKey,
		Status: common.TokenStatusEnabled, RemainQuota: tokenQuota,
	}).Error)
}

func tieredRelayQuotaState(t *testing.T, db *gorm.DB, userID, tokenID int) (int, int, int) {
	t.Helper()
	var user model.User
	var token model.Token
	require.NoError(t, db.Select("quota").First(&user, userID).Error)
	require.NoError(t, db.Select("remain_quota", "used_quota").First(&token, tokenID).Error)
	return user.Quota, token.RemainQuota, token.UsedQuota
}

func TestRelaySelectedChannelRecordsAttemptBeforeTieredReservationFailure(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var upstreamCalls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		upstreamCalls.Add(1)
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer upstream.Close()

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	channel := &model.Channel{
		Id:      73,
		Type:    constant.ChannelTypeOpenAI,
		Name:    "final-paid-group",
		Key:     "test-key",
		BaseURL: &upstream.URL,
	}
	require.Nil(t, middleware.SetupContextForSelectedChannel(c, channel, "gpt-test"))

	reserveErr := errors.New("final group quota unavailable")
	spy := &relayReserveFailureSpy{reserved: 100, err: reserveErr}
	info := &relaycommon.RelayInfo{
		Billing:               spy,
		FinalPreConsumedQuota: 100,
		OriginModelName:       "gpt-test",
		RelayMode:             relayconstant.RelayModeChatCompletions,
		RelayFormat:           types.RelayFormatOpenAI,
		RequestURLPath:        "/v1/chat/completions",
		Request:               &dto.GeneralOpenAIRequest{Model: "gpt-test"},
		PriceData: types.PriceData{
			GroupRatioInfo: types.GroupRatioInfo{GroupRatio: 2},
		},
		TieredBillingSnapshot: &billingexpr.BillingSnapshot{
			BillingMode:               "tiered_expr",
			ExprString:                `tier("default", p * 2 + c * 10)`,
			EstimatedQuotaBeforeGroup: 100,
			GroupRatio:                1,
			EstimatedQuotaAfterGroup:  100,
		},
	}

	apiErr, relayStarted := relaySelectedChannel(c, info, channel, types.RelayFormatOpenAI, nil)

	require.NotNil(t, apiErr)
	assert.False(t, relayStarted)
	assert.ErrorIs(t, apiErr, reserveErr)
	assert.Equal(t, types.ErrorCodeUpdateDataError, apiErr.GetErrorCode())
	assert.True(t, types.IsSkipRetryError(apiErr))
	assert.Equal(t, []string{"73"}, c.GetStringSlice("use_channel"))
	assert.Equal(t, []int{200}, spy.calls)
	assert.Zero(t, upstreamCalls.Load())

	controlRecorder := httptest.NewRecorder()
	controlContext, _ := gin.CreateTestContext(controlRecorder)
	controlContext.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gpt-test","messages":[]}`))
	controlContext.Request.Header.Set("Content-Type", "application/json")
	require.Nil(t, middleware.SetupContextForSelectedChannel(controlContext, channel, "gpt-test"))
	service.InitHttpClient()
	controlInfo := &relaycommon.RelayInfo{
		Billing:               &relayReserveFailureSpy{reserved: 100},
		FinalPreConsumedQuota: 100,
		OriginModelName:       "gpt-test",
		RelayMode:             relayconstant.RelayModeChatCompletions,
		RelayFormat:           types.RelayFormatOpenAI,
		RequestURLPath:        "/v1/chat/completions",
		Request:               &dto.GeneralOpenAIRequest{Model: "gpt-test"},
		PriceData:             types.PriceData{GroupRatioInfo: types.GroupRatioInfo{GroupRatio: 1}},
		TieredBillingSnapshot: &billingexpr.BillingSnapshot{
			BillingMode:               "tiered_expr",
			ExprString:                `tier("default", p * 2 + c * 10)`,
			EstimatedQuotaBeforeGroup: 100,
			GroupRatio:                1,
			EstimatedQuotaAfterGroup:  100,
		},
	}

	controlErr, controlStarted := relaySelectedChannel(controlContext, controlInfo, channel, types.RelayFormatOpenAI, nil)
	require.NotNil(t, controlErr)
	assert.True(t, controlStarted, "control request should reach the upstream relay path")
	assert.Equal(t, int32(1), upstreamCalls.Load(), "control proves the test server observes upstream sends")
}

func TestGetChannelRefreshesRatioAfterSuccessfulSelection(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Set("channel_id", 91)
	c.Set("channel_name", "selected-channel")
	c.Set("auto_group", "edit_this")
	info := &relaycommon.RelayInfo{
		UserGroup:  "vip",
		UsingGroup: "default",
	}
	retryParam := &service.RetryParam{Ctx: c}

	channel, apiErr := getChannel(c, info, retryParam)

	require.Nil(t, apiErr)
	require.NotNil(t, channel)
	assert.Equal(t, 91, channel.Id)
	assert.Equal(t, "edit_this", info.UsingGroup)
	assert.Equal(t, 0.9, info.PriceData.GroupRatioInfo.GroupRatio)
	assert.True(t, info.PriceData.GroupRatioInfo.HasSpecialRatio)
}

func TestRelaySelectedChannelRejectsInvalidTieredRatioBeforeSend(t *testing.T) {
	gin.SetMode(gin.TestMode)
	service.InitHttpClient()
	for _, testCase := range []struct {
		name  string
		ratio float64
	}{
		{name: "negative", ratio: -1},
		{name: "nan", ratio: math.NaN()},
		{name: "positive infinity", ratio: math.Inf(1)},
		{name: "negative infinity", ratio: math.Inf(-1)},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			var upstreamCalls atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				upstreamCalls.Add(1)
				w.WriteHeader(http.StatusBadGateway)
			}))
			defer upstream.Close()

			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gpt-test","messages":[]}`))
			c.Request.Header.Set("Content-Type", "application/json")
			channel := &model.Channel{Id: 81, Type: constant.ChannelTypeOpenAI, Name: "invalid-ratio", Key: "test-key", BaseURL: &upstream.URL}
			require.Nil(t, middleware.SetupContextForSelectedChannel(c, channel, "gpt-test"))
			spy := &relayReserveFailureSpy{reserved: 100}
			info := &relaycommon.RelayInfo{
				Billing:               spy,
				FinalPreConsumedQuota: 100,
				OriginModelName:       "gpt-test",
				RelayMode:             relayconstant.RelayModeChatCompletions,
				RelayFormat:           types.RelayFormatOpenAI,
				RequestURLPath:        "/v1/chat/completions",
				Request:               &dto.GeneralOpenAIRequest{Model: "gpt-test"},
				PriceData: types.PriceData{
					FreeModel:      true,
					GroupRatioInfo: types.GroupRatioInfo{GroupRatio: testCase.ratio},
				},
				TieredBillingSnapshot: &billingexpr.BillingSnapshot{
					BillingMode:               "tiered_expr",
					ExprString:                `tier("default", p * 2 + c * 10)`,
					EstimatedQuotaBeforeGroup: 100,
					GroupRatio:                1,
					EstimatedQuotaAfterGroup:  100,
				},
			}

			apiErr, relayStarted := relaySelectedChannel(c, info, channel, types.RelayFormatOpenAI, nil)

			require.NotNil(t, apiErr)
			assert.False(t, relayStarted)
			assert.Equal(t, types.ErrorCodeModelPriceError, apiErr.GetErrorCode())
			assert.True(t, types.IsSkipRetryError(apiErr))
			assert.Equal(t, []string{"81"}, c.GetStringSlice("use_channel"))
			assert.Empty(t, spy.calls)
			assert.Equal(t, 100, spy.reserved)
			assert.Equal(t, 100, info.FinalPreConsumedQuota)
			assert.Equal(t, 1.0, info.TieredBillingSnapshot.GroupRatio)
			assert.Equal(t, 100, info.TieredBillingSnapshot.EstimatedQuotaAfterGroup)
			assert.True(t, info.PriceData.FreeModel)
			assert.Zero(t, upstreamCalls.Load())
		})
	}
}

func TestRelaySelectedChannelTieredWalletDeltaInsufficientStopsBeforeSendAndIsRetryable(t *testing.T) {
	gin.SetMode(gin.TestMode)
	service.InitHttpClient()
	db := setupTieredRelayBillingDB(t)
	createTieredRelayWalletFixtures(t, db, 401, 402, 150, 1_000, "tiered-wallet-insufficient")
	var upstreamCalls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		upstreamCalls.Add(1)
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer upstream.Close()

	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gpt-test","messages":[]}`))
	c.Request.Header.Set("Content-Type", "application/json")
	channel := &model.Channel{Id: 403, Type: constant.ChannelTypeOpenAI, Name: "wallet-insufficient", Key: "test-key", BaseURL: &upstream.URL}
	require.Nil(t, middleware.SetupContextForSelectedChannel(c, channel, "gpt-test"))
	info := &relaycommon.RelayInfo{
		UserId:          401,
		TokenId:         402,
		TokenKey:        "tiered-wallet-insufficient",
		OriginModelName: "gpt-test",
		ForcePreConsume: true,
		UserSetting:     dto.UserSetting{BillingPreference: "wallet_only"},
		RelayMode:       relayconstant.RelayModeChatCompletions,
		RelayFormat:     types.RelayFormatOpenAI,
		RequestURLPath:  "/v1/chat/completions",
		Request:         &dto.GeneralOpenAIRequest{Model: "gpt-test"},
		PriceData:       types.PriceData{GroupRatioInfo: types.GroupRatioInfo{GroupRatio: 2}},
		TieredBillingSnapshot: &billingexpr.BillingSnapshot{
			BillingMode:               "tiered_expr",
			ExprString:                `tier("default", p * 2 + c * 10)`,
			EstimatedQuotaBeforeGroup: 100,
			GroupRatio:                1,
			EstimatedQuotaAfterGroup:  100,
		},
	}
	require.Nil(t, service.PreConsumeBilling(c, 100, info))
	info.PriceData.FreeModel = true

	firstErr, firstStarted := relaySelectedChannel(c, info, channel, types.RelayFormatOpenAI, nil)
	secondErr, secondStarted := relaySelectedChannel(c, info, channel, types.RelayFormatOpenAI, nil)

	for _, apiErr := range []*types.NewAPIError{firstErr, secondErr} {
		require.NotNil(t, apiErr)
		assert.Equal(t, types.ErrorCodeInsufficientUserQuota, apiErr.GetErrorCode())
		assert.Equal(t, http.StatusForbidden, apiErr.StatusCode)
		assert.True(t, types.IsSkipRetryError(apiErr))
		assert.Contains(t, apiErr.Error(), "用户额度不足, 剩余额度")
	}
	assert.Equal(t, firstErr.Error(), secondErr.Error())
	assert.False(t, firstStarted)
	assert.False(t, secondStarted)
	assert.Equal(t, []string{"403", "403"}, c.GetStringSlice("use_channel"))
	assert.Zero(t, upstreamCalls.Load())
	assert.Equal(t, 100, info.Billing.GetPreConsumedQuota())
	assert.Equal(t, 100, info.FinalPreConsumedQuota)
	assert.Equal(t, 1.0, info.TieredBillingSnapshot.GroupRatio)
	assert.Equal(t, 100, info.TieredBillingSnapshot.EstimatedQuotaAfterGroup)
	assert.True(t, info.PriceData.FreeModel)
	userQuota, tokenRemain, tokenUsed := tieredRelayQuotaState(t, db, 401, 402)
	assert.Equal(t, 50, userQuota)
	assert.Equal(t, 900, tokenRemain)
	assert.Equal(t, 100, tokenUsed)
	require.NoError(t, info.Billing.Settle(100))
}

func TestRelaySelectedChannelTieredTrustedHigherGroupInsufficientStopsBeforeSend(t *testing.T) {
	gin.SetMode(gin.TestMode)
	service.InitHttpClient()
	db := setupTieredRelayBillingDB(t)
	const initialQuota = 6_000_000
	const initialTarget = 3_500_000
	createTieredRelayWalletFixtures(t, db, 411, 412, initialQuota, initialQuota, "tiered-trusted-insufficient")
	var upstreamCalls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		upstreamCalls.Add(1)
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer upstream.Close()

	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gpt-test","messages":[]}`))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set("token_quota", initialQuota)
	channel := &model.Channel{Id: 413, Type: constant.ChannelTypeOpenAI, Name: "trusted-insufficient", Key: "test-key", BaseURL: &upstream.URL}
	require.Nil(t, middleware.SetupContextForSelectedChannel(c, channel, "gpt-test"))
	info := &relaycommon.RelayInfo{
		UserId:          411,
		TokenId:         412,
		TokenKey:        "tiered-trusted-insufficient",
		OriginModelName: "gpt-test",
		UserSetting:     dto.UserSetting{BillingPreference: "wallet_only"},
		RelayMode:       relayconstant.RelayModeChatCompletions,
		RelayFormat:     types.RelayFormatOpenAI,
		RequestURLPath:  "/v1/chat/completions",
		Request:         &dto.GeneralOpenAIRequest{Model: "gpt-test"},
		PriceData:       types.PriceData{FreeModel: true, GroupRatioInfo: types.GroupRatioInfo{GroupRatio: 2}},
		TieredBillingSnapshot: &billingexpr.BillingSnapshot{
			BillingMode:               "tiered_expr",
			ExprString:                `tier("default", p * 2 + c * 10)`,
			EstimatedQuotaBeforeGroup: initialTarget,
			GroupRatio:                1,
			EstimatedQuotaAfterGroup:  initialTarget,
		},
	}
	require.Nil(t, service.PreConsumeBilling(c, initialTarget, info))
	assert.Zero(t, info.Billing.GetPreConsumedQuota())

	apiErr, relayStarted := relaySelectedChannel(c, info, channel, types.RelayFormatOpenAI, nil)

	require.NotNil(t, apiErr)
	assert.Equal(t, types.ErrorCodeInsufficientUserQuota, apiErr.GetErrorCode())
	assert.Equal(t, http.StatusForbidden, apiErr.StatusCode)
	assert.True(t, types.IsSkipRetryError(apiErr))
	assert.Contains(t, apiErr.Error(), "用户额度不足, 剩余额度")
	assert.False(t, relayStarted)
	assert.Equal(t, []string{"413"}, c.GetStringSlice("use_channel"))
	assert.Zero(t, upstreamCalls.Load())
	assert.Zero(t, info.Billing.GetPreConsumedQuota())
	assert.Zero(t, info.FinalPreConsumedQuota)
	assert.Equal(t, 1.0, info.TieredBillingSnapshot.GroupRatio)
	assert.Equal(t, initialTarget, info.TieredBillingSnapshot.EstimatedQuotaAfterGroup)
	assert.True(t, info.PriceData.FreeModel)
	userQuota, tokenRemain, tokenUsed := tieredRelayQuotaState(t, db, 411, 412)
	assert.Equal(t, initialQuota, userQuota)
	assert.Equal(t, initialQuota, tokenRemain)
	assert.Zero(t, tokenUsed)
	require.NoError(t, info.Billing.Settle(0))
}
