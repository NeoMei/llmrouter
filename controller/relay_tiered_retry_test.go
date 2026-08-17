package controller

import (
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

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
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
