package relay

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type failedUsageBillingSpy struct {
	settledCalls int
	pending      []string
	refundCalls  int
}

func (s *failedUsageBillingSpy) Settle(int) error         { s.settledCalls++; return nil }
func (s *failedUsageBillingSpy) Refund(*gin.Context)      { s.refundCalls++ }
func (s *failedUsageBillingSpy) NeedsRefund() bool        { return s.settledCalls == 0 }
func (s *failedUsageBillingSpy) GetPreConsumedQuota() int { return 100 }
func (s *failedUsageBillingSpy) Reserve(int) error        { return nil }
func (s *failedUsageBillingSpy) MarkReconciliationPending(reason string) {
	s.pending = append(s.pending, reason)
}
func (s *failedUsageBillingSpy) IsSettled() bool { return s.settledCalls > 0 }

func failedUsageInfo(t *testing.T, billing relaycommon.BillingSettler) *relaycommon.RelayInfo {
	t.Helper()
	return &relaycommon.RelayInfo{
		UserId: 42, TokenId: 7, ChannelMeta: &relaycommon.ChannelMeta{ChannelId: 3}, OriginModelName: "failed-usage-model",
		UsingGroup: "default", StartTime: time.Now(), Billing: billing,
		PriceData: types.PriceData{ModelRatio: 1, CompletionRatio: 1, GroupRatioInfo: types.GroupRatioInfo{GroupRatio: 1}},
	}
}

func setupFailedUsageSettlement(t *testing.T, info *relaycommon.RelayInfo) func() *dto.Usage {
	t.Helper()
	var observed *dto.Usage
	oldText, oldAudio := postTextConsumeQuotaFn, postAudioConsumeQuotaFn
	postTextConsumeQuotaFn = func(_ *gin.Context, _ *relaycommon.RelayInfo, usage *dto.Usage, _ []string) {
		observed = usage
		require.NoError(t, info.Billing.Settle(48))
	}
	postAudioConsumeQuotaFn = func(_ *gin.Context, _ *relaycommon.RelayInfo, usage *dto.Usage, _ string) {
		observed = usage
		require.NoError(t, info.Billing.Settle(48))
	}
	t.Cleanup(func() {
		postTextConsumeQuotaFn, postAudioConsumeQuotaFn = oldText, oldAudio
	})
	return func() *dto.Usage { return observed }
}

func setupFailedUsageHold(t *testing.T) {
	t.Helper()
	oldText := postTextConsumeQuotaFn
	postTextConsumeQuotaFn = func(_ *gin.Context, relayInfo *relaycommon.RelayInfo, _ *dto.Usage, _ []string) {
		relayInfo.BillingSettlementFailed = true
	}
	t.Cleanup(func() { postTextConsumeQuotaFn = oldText })
}

func TestFailedUsageSettlementHoldKeepsReservationPending(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	spy := &failedUsageBillingSpy{}
	info := failedUsageInfo(t, spy)
	setupFailedUsageHold(t)
	usage := &dto.Usage{PromptTokens: 37, CompletionTokens: 11, TotalTokens: 48}

	outcome := consumeFailedUsage(c, info, usage)
	apiErr := finalizeFailedUsageError(types.NewError(assert.AnError, "upstream_failed"), outcome)

	assert.Equal(t, failedUsagePending, outcome)
	assert.Zero(t, spy.settledCalls)
	assert.True(t, info.BillingSettlementFailed)
	assert.True(t, types.IsSkipRetryError(apiErr))
}

func TestFailedUsageSettlesOnceAndSkipsRetry(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	spy := &failedUsageBillingSpy{}
	info := failedUsageInfo(t, spy)
	getObserved := setupFailedUsageSettlement(t, info)
	usage := &dto.Usage{PromptTokens: 37, CompletionTokens: 11, TotalTokens: 48}

	outcome := consumeFailedUsage(c, info, usage)
	apiErr := finalizeFailedUsageError(types.NewError(assert.AnError, types.ErrorCodeBadResponse), outcome)

	assert.Equal(t, failedUsageSettled, outcome)
	assert.True(t, info.FailedUsageConsumed)
	assert.Equal(t, 1, spy.settledCalls)
	assert.Empty(t, spy.pending)
	assert.True(t, types.IsSkipRetryError(apiErr))
	assert.Equal(t, http.StatusInternalServerError, apiErr.StatusCode)
	require.NotNil(t, getObserved())
	assert.Equal(t, 37, getObserved().PromptTokens)
	assert.Equal(t, 11, getObserved().CompletionTokens)

	second := consumeFailedUsage(c, info, usage)
	assert.Equal(t, failedUsageNone, second, "the same attempt must not bill terminal usage twice")
	assert.Equal(t, 1, spy.settledCalls)
}

func TestFailedUsageEstimatedCountersKeepReservationPending(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	spy := &failedUsageBillingSpy{}
	info := failedUsageInfo(t, spy)
	estimated := dto.NewEstimatedGeminiChatBillingUsage(&dto.Usage{PromptTokens: 100})
	usage := &dto.Usage{PromptTokens: 100, BillingUsage: estimated}

	outcome := consumeFailedUsage(c, info, usage)
	apiErr := finalizeFailedUsageError(types.NewError(assert.AnError, "upstream_failed"), outcome)

	assert.Equal(t, failedUsagePending, outcome)
	assert.Equal(t, []string{"estimated upstream usage"}, spy.pending)
	assert.Zero(t, spy.settledCalls)
	assert.False(t, info.FailedUsageConsumed)
	assert.True(t, types.IsSkipRetryError(apiErr))
}

func TestFailedUsageWithoutVendorCountersLeavesOrdinaryError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	spy := &failedUsageBillingSpy{}
	info := failedUsageInfo(t, spy)

	outcome := consumeFailedUsage(c, info, &dto.Usage{})
	apiErr := finalizeFailedUsageError(types.NewError(assert.AnError, "upstream_failed"), outcome)

	assert.Equal(t, failedUsageNone, outcome)
	assert.Zero(t, spy.settledCalls)
	assert.Empty(t, spy.pending)
	assert.False(t, types.IsSkipRetryError(apiErr))
}
