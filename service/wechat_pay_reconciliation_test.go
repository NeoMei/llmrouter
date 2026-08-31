package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type reconcileQueryOutcome struct {
	result WeChatPayQueryResult
	err    error
}

type fakeReconcileWeChatPayProvider struct {
	queries    map[string][]reconcileQueryOutcome
	closeErrs  map[string]error
	queryCalls map[string]int
	closeCalls map[string]int
}

func (f *fakeReconcileWeChatPayProvider) CreateNative(context.Context, WeChatPayCreateRequest) (WeChatPayCreateResult, error) {
	return WeChatPayCreateResult{}, errors.New("unexpected create call")
}

func (f *fakeReconcileWeChatPayProvider) QueryOrder(_ context.Context, tradeNo string) (WeChatPayQueryResult, error) {
	if f.queryCalls == nil {
		f.queryCalls = map[string]int{}
	}
	index := f.queryCalls[tradeNo]
	f.queryCalls[tradeNo]++
	outcomes := f.queries[tradeNo]
	if index >= len(outcomes) {
		return WeChatPayQueryResult{}, errors.New("unexpected extra query")
	}
	return outcomes[index].result, outcomes[index].err
}

func (f *fakeReconcileWeChatPayProvider) CloseOrder(_ context.Context, tradeNo string) (WeChatPayCloseResult, error) {
	if f.closeCalls == nil {
		f.closeCalls = map[string]int{}
	}
	f.closeCalls[tradeNo]++
	return WeChatPayCloseResult{RequestID: "close-request-1"}, f.closeErrs[tradeNo]
}

func (f *fakeReconcileWeChatPayProvider) ParseNotify(context.Context, *http.Request) (WeChatPayNotifyResult, error) {
	return WeChatPayNotifyResult{}, errors.New("unexpected notify call")
}

func seedReconcileWeChatOrder(t *testing.T, userID int, suffix int, now int64, ageSeconds int64) *model.TopUp {
	t.Helper()
	seedUser(t, userID, 0)
	topUp, _, err := model.CreateOrReuseWeChatTopUp(model.WeChatTopUpOrderInput{
		UserID:        userID,
		TradeNo:       fmt.Sprintf("WXNR%027d", suffix),
		AmountMinor:   5000,
		CreditedQuota: 3424658,
		CreateTime:    now - ageSeconds,
	})
	require.NoError(t, err)
	topUp.NextReconcileTime = now
	require.NoError(t, model.DB.Save(topUp).Error)
	return topUp
}

func reconcileTransaction(config WeChatPayConfig, topUp *model.TopUp, state string) WeChatPayTransaction {
	transactionID := ""
	if state == "SUCCESS" {
		transactionID = "4200000000202608310000000099"
	}
	return WeChatPayTransaction{
		AppID:         config.AppID,
		MchID:         config.MchID,
		OutTradeNo:    topUp.TradeNo,
		TransactionID: transactionID,
		TradeType:     "NATIVE",
		TradeState:    state,
		Currency:      "CNY",
		AmountMinor:   topUp.PaidAmountMinor,
	}
}

func TestReconcileWeChatTopUpsSettlesSuccessfulQuery(t *testing.T) {
	truncate(t)
	now := int64(1_788_188_000)
	topUp := seedReconcileWeChatOrder(t, 901, 1, now, 60)
	config := WeChatPayConfig{MchID: "1295594101", AppID: "wxa0b1bc308cdea2b2"}
	provider := &fakeReconcileWeChatPayProvider{queries: map[string][]reconcileQueryOutcome{
		topUp.TradeNo: {{result: WeChatPayQueryResult{Transaction: reconcileTransaction(config, topUp, "SUCCESS"), RequestID: "query-success-1"}}},
	}}

	summary := ReconcileWeChatTopUps(context.Background(), provider, config, now, 10)
	assert.Equal(t, 1, summary.Claimed)
	assert.Equal(t, 1, summary.Settled)
	assert.Zero(t, summary.QueryErrors)
	assert.Equal(t, common.TopUpStatusSuccess, model.GetTopUpByTradeNo(topUp.TradeNo).Status)
	var user model.User
	require.NoError(t, model.DB.First(&user, topUp.UserId).Error)
	assert.Equal(t, topUp.CreditedQuota, user.Quota)
}

func TestReconcileWeChatTopUpsKeepsPreDeadlineNotPayPending(t *testing.T) {
	truncate(t)
	now := int64(1_788_188_100)
	topUp := seedReconcileWeChatOrder(t, 902, 2, now, 60)
	config := WeChatPayConfig{MchID: "1295594101", AppID: "wxa0b1bc308cdea2b2"}
	provider := &fakeReconcileWeChatPayProvider{queries: map[string][]reconcileQueryOutcome{
		topUp.TradeNo: {{result: WeChatPayQueryResult{Transaction: reconcileTransaction(config, topUp, "NOTPAY")}}},
	}}

	summary := ReconcileWeChatTopUps(context.Background(), provider, config, now, 10)
	assert.Equal(t, 1, summary.Pending)
	assert.Zero(t, provider.closeCalls[topUp.TradeNo])
	assert.Equal(t, common.TopUpStatusPending, model.GetTopUpByTradeNo(topUp.TradeNo).Status)
}

func TestReconcileWeChatTopUpsClosesExpiredNotPayBeforeExpiring(t *testing.T) {
	truncate(t)
	now := int64(1_788_188_200)
	topUp := seedReconcileWeChatOrder(t, 903, 3, now, 901)
	config := WeChatPayConfig{MchID: "1295594101", AppID: "wxa0b1bc308cdea2b2"}
	provider := &fakeReconcileWeChatPayProvider{queries: map[string][]reconcileQueryOutcome{
		topUp.TradeNo: {
			{result: WeChatPayQueryResult{Transaction: reconcileTransaction(config, topUp, "NOTPAY")}},
			{result: WeChatPayQueryResult{Transaction: reconcileTransaction(config, topUp, "CLOSED")}},
		},
	}}

	summary := ReconcileWeChatTopUps(context.Background(), provider, config, now, 10)
	assert.Equal(t, 1, provider.closeCalls[topUp.TradeNo])
	assert.Equal(t, 1, summary.Expired)
	assert.Equal(t, common.TopUpStatusExpired, model.GetTopUpByTradeNo(topUp.TradeNo).Status)
}

func TestReconcileWeChatTopUpsExpiresAuthoritativeClosedState(t *testing.T) {
	truncate(t)
	now := int64(1_788_188_300)
	topUp := seedReconcileWeChatOrder(t, 904, 4, now, 60)
	config := WeChatPayConfig{MchID: "1295594101", AppID: "wxa0b1bc308cdea2b2"}
	provider := &fakeReconcileWeChatPayProvider{queries: map[string][]reconcileQueryOutcome{
		topUp.TradeNo: {{result: WeChatPayQueryResult{Transaction: reconcileTransaction(config, topUp, "CLOSED")}}},
	}}

	summary := ReconcileWeChatTopUps(context.Background(), provider, config, now, 10)
	assert.Equal(t, 1, summary.Expired)
	assert.Equal(t, common.TopUpStatusExpired, model.GetTopUpByTradeNo(topUp.TradeNo).Status)
}

func TestReconcileWeChatTopUpsKeepsAmbiguousQueryAndClosePending(t *testing.T) {
	tests := []struct {
		name      string
		query     []reconcileQueryOutcome
		closeErr  error
		wantQuery int
		wantClose int
	}{
		{name: "query error", query: []reconcileQueryOutcome{{err: errors.New("query timeout")}}, wantQuery: 1},
		{name: "close error", query: []reconcileQueryOutcome{{}}, closeErr: errors.New("close timeout"), wantClose: 1},
	}

	for index, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			truncate(t)
			now := int64(1_788_188_400 + index)
			topUp := seedReconcileWeChatOrder(t, 910+index, 10+index, now, 901)
			config := WeChatPayConfig{MchID: "1295594101", AppID: "wxa0b1bc308cdea2b2"}
			if tt.name == "close error" {
				tt.query[0].result = WeChatPayQueryResult{Transaction: reconcileTransaction(config, topUp, "NOTPAY")}
			}
			provider := &fakeReconcileWeChatPayProvider{
				queries:   map[string][]reconcileQueryOutcome{topUp.TradeNo: tt.query},
				closeErrs: map[string]error{topUp.TradeNo: tt.closeErr},
			}

			summary := ReconcileWeChatTopUps(context.Background(), provider, config, now, 10)
			assert.Equal(t, common.TopUpStatusPending, model.GetTopUpByTradeNo(topUp.TradeNo).Status)
			assert.Equal(t, tt.wantQuery, summary.QueryErrors)
			assert.Equal(t, tt.wantClose, summary.CloseErrors)
		})
	}
}

func TestReconcileWeChatTopUpsResolvesPayCloseRaceWithSecondQuery(t *testing.T) {
	truncate(t)
	now := int64(1_788_188_500)
	topUp := seedReconcileWeChatOrder(t, 920, 20, now, 901)
	config := WeChatPayConfig{MchID: "1295594101", AppID: "wxa0b1bc308cdea2b2"}
	provider := &fakeReconcileWeChatPayProvider{queries: map[string][]reconcileQueryOutcome{
		topUp.TradeNo: {
			{result: WeChatPayQueryResult{Transaction: reconcileTransaction(config, topUp, "NOTPAY")}},
			{result: WeChatPayQueryResult{Transaction: reconcileTransaction(config, topUp, "SUCCESS")}},
		},
	}}

	summary := ReconcileWeChatTopUps(context.Background(), provider, config, now, 10)
	assert.Equal(t, 1, provider.closeCalls[topUp.TradeNo])
	assert.Equal(t, 2, provider.queryCalls[topUp.TradeNo])
	assert.Equal(t, 1, summary.Settled)
	assert.Equal(t, common.TopUpStatusSuccess, model.GetTopUpByTradeNo(topUp.TradeNo).Status)
}

func TestReconcileWeChatTopUpsRecordsVerifiedIdentityMismatch(t *testing.T) {
	truncate(t)
	now := int64(1_788_188_600)
	topUp := seedReconcileWeChatOrder(t, 930, 30, now, 60)
	config := WeChatPayConfig{MchID: "1295594101", AppID: "wxa0b1bc308cdea2b2"}
	transaction := reconcileTransaction(config, topUp, "SUCCESS")
	transaction.AmountMinor++
	provider := &fakeReconcileWeChatPayProvider{queries: map[string][]reconcileQueryOutcome{
		topUp.TradeNo: {{result: WeChatPayQueryResult{Transaction: transaction}}},
	}}

	summary := ReconcileWeChatTopUps(context.Background(), provider, config, now, 10)
	assert.Equal(t, 1, summary.Incidents)
	assert.Equal(t, common.TopUpStatusPending, model.GetTopUpByTradeNo(topUp.TradeNo).Status)
	var count int64
	require.NoError(t, model.DB.Model(&model.Log{}).Where("content = ?", "WeChat Pay reconciliation requires review").Count(&count).Error)
	assert.Equal(t, int64(1), count)
}
