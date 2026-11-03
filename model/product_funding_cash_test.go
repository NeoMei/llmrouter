package model

import (
	"context"
	"strconv"
	"testing"

	userkit "github.com/NeoMei/UniHub/packages/userkit-go"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func cashFundingFixture(t *testing.T, rate float64) (*ProductFundingStore, *runtimeLedgerFixture) {
	t.Helper()
	s, _, ledger := productFundingFixture(t)
	require.NoError(t, s.Import(context.Background(), 42, "payer", "old-reviewed"))
	ledger.cashBalance = 1000000
	oldRate, oldUnit := operation_setting.USDExchangeRate, common.QuotaPerUnit
	operation_setting.USDExchangeRate, common.QuotaPerUnit = rate, 500000
	t.Cleanup(func() { operation_setting.USDExchangeRate, common.QuotaPerUnit = oldRate, oldUnit })
	t.Setenv("UNIHUB_PRODUCT_FUNDING_MODE", "unified_cash")
	return s, ledger
}

func TestProductCashChargeFreezesRateAndPreservesQuotaCounters(t *testing.T) {
	s, ledger := cashFundingFixture(t, 7.3)
	ctx := context.Background()
	call, err := s.Reserve(ctx, ProductFundingAdmission{CallID: "cash", UserID: 42, TokenID: 7, Model: "m", Operation: "chat", PriceSnapshot: `{"p":1}`, Quota: 50})
	require.NoError(t, err)
	var reserve userkit.RuntimeReserveRequest
	require.NoError(t, common.UnmarshalJsonStr(call.ReserveJSON, &reserve))
	assert.Equal(t, "CNY/6", reserve.Asset)
	assert.Equal(t, "lr-cny-v1", reserve.UnitVersion)
	assert.Equal(t, "730", reserve.Amount)
	operation_setting.USDExchangeRate = 100
	ledger.loseSettleReply = true
	require.Error(t, s.Settle(ctx, call.ID, 30, `{}`))
	require.NoError(t, s.Recover(ctx))
	require.NoError(t, s.Settle(ctx, call.ID, 30, `{}`))
	assert.Equal(t, 438, ledger.cashConsumed)
	assert.Equal(t, 1, ledger.settlements)
	assert.Equal(t, 500, ledger.balance)
	var token Token
	require.NoError(t, s.db.First(&token, 7).Error)
	assert.Equal(t, 170, token.RemainQuota)
	assert.Equal(t, 30, token.UsedQuota)
	var account ProductFundingAccount
	require.NoError(t, s.db.First(&account, 42).Error)
	assert.Equal(t, userkit.RuntimeAsset, account.Asset)
}

func TestProductCashTopupRoundsCumulativeAmountAndReplaysOnce(t *testing.T) {
	s, ledger := cashFundingFixture(t, 0.625) // 5/4 micro-CNY per LR quota
	ctx := context.Background()
	_, err := s.Reserve(ctx, ProductFundingAdmission{CallID: "round", UserID: 42, TokenID: 7, Model: "m", Operation: "chat", PriceSnapshot: `{"p":1}`, Quota: 1})
	require.NoError(t, err)
	ledger.loseTopUpReply = true
	require.Error(t, s.TopUp(ctx, "round", 2))
	require.NoError(t, s.Recover(ctx))
	require.NoError(t, s.TopUp(ctx, "round", 3))
	assert.Equal(t, 4, ledger.cashHeld)
	assert.Len(t, ledger.topups, 2)
	require.NoError(t, s.Settle(ctx, "round", 3, `{}`))
	assert.Equal(t, 4, ledger.cashConsumed)
	require.NoError(t, RegisterProductRefundModels(s.db))
	ledger.loseRefundReply = true
	require.Error(t, s.Refund(ctx, "round", "a", 1, "partial"))
	require.NoError(t, s.RecoverRefunds(ctx))
	require.NoError(t, s.Refund(ctx, "round", "a", 1, "partial"))
	require.NoError(t, s.Refund(ctx, "round", "b", 1, "partial"))
	require.NoError(t, s.Refund(ctx, "round", "c", 1, "full"))
	require.Error(t, s.Refund(ctx, "round", "d", 1, "excess"))
	assert.Zero(t, ledger.cashConsumed)
	assert.Equal(t, 1000000, ledger.cashBalance)
	var token Token
	require.NoError(t, s.db.First(&token, 7).Error)
	assert.Equal(t, 200, token.RemainQuota)
	assert.Zero(t, token.UsedQuota)
}

func TestProductCashModeFinishesOutstandingQuotaCall(t *testing.T) {
	s, db, ledger := productFundingFixture(t)
	ctx := context.Background()
	require.NoError(t, s.Import(ctx, 42, "payer", "reviewed"))
	_, err := s.Reserve(ctx, ProductFundingAdmission{CallID: "legacy", UserID: 42, TokenID: 7, Model: "m", Operation: "chat", PriceSnapshot: `{"p":1}`, Quota: 40})
	require.NoError(t, err)
	require.NoError(t, db.Model(&ProductRuntimeCall{}).Where("id = ?", "legacy").Updates(map[string]any{"asset": "", "unit_version": "", "snapshot_digest": ""}).Error)
	t.Setenv("UNIHUB_PRODUCT_FUNDING_MODE", "unified_cash")
	require.NoError(t, s.Settle(ctx, "legacy", 20, `{}`))
	assert.Equal(t, 480, ledger.balance)
	assert.Zero(t, ledger.cashConsumed)
	var token Token
	require.NoError(t, db.First(&token, 7).Error)
	assert.Equal(t, 180, token.RemainQuota)
}

func TestProductCashFailureRestoresKeyWithoutLocalBalanceFallback(t *testing.T) {
	s, ledger := cashFundingFixture(t, 7.3)
	ledger.rejectReserve = true
	_, err := s.Reserve(context.Background(), ProductFundingAdmission{CallID: "denied-cash", UserID: 42, TokenID: 7, Model: "m", Operation: "chat", PriceSnapshot: `{"p":1}`, Quota: 50})
	require.Error(t, err)
	var token Token
	require.NoError(t, s.db.First(&token, 7).Error)
	assert.Equal(t, 200, token.RemainQuota)
	assert.Equal(t, 500, ledger.balance)
	assert.Equal(t, 1000000, ledger.cashBalance)
}

func TestProductCashBalanceKeepsExactMoneyAndFloorsProjection(t *testing.T) {
	s, ledger := cashFundingFixture(t, 7.3)
	ledger.cashBalance = 1001
	b, e := s.CashBalance(context.Background(), 42)
	require.NoError(t, e)
	assert.Equal(t, "1001", b.Available)
	assert.Equal(t, "CNY/6", b.Asset)
	quota, e := s.Balance(context.Background(), 42)
	require.NoError(t, e)
	assert.Equal(t, 68, quota)
	operation_setting.USDExchangeRate = 0.0000001
	quota, e = s.Balance(context.Background(), 42)
	require.NoError(t, e)
	assert.Equal(t, common.MaxQuota, quota)
}

func TestProductCashSupplementalUsesOriginalAssetRateAndPricing(t *testing.T) {
	s, ledger := cashFundingFixture(t, 7.3)
	ctx := context.Background()
	call, e := s.Reserve(ctx, ProductFundingAdmission{CallID: "parent-cash", UserID: 42, TokenID: 7, Model: "m", Operation: "task", PriceSnapshot: `{"p":1}`, Quota: 20})
	require.NoError(t, e)
	require.NoError(t, s.Settle(ctx, call.ID, 20, `{}`))
	operation_setting.USDExchangeRate = 100
	t.Setenv("UNIHUB_PRODUCT_FUNDING_MODE", "")
	child, e := s.Reserve(ctx, ProductFundingAdmission{CallID: "child-cash", ParentCallID: call.ID, FinancialCommitment: true, ExternalCounters: true, UserID: 42, TokenID: 7, Model: "m", Operation: "task", PriceSnapshot: call.ReserveJSON, Quota: 10})
	require.NoError(t, e)
	require.NoError(t, s.Settle(ctx, child.ID, 10, `{}`))
	assert.Equal(t, 438, ledger.cashConsumed)
	assert.Equal(t, call.PricingDigest, child.PricingDigest)
	assert.Equal(t, "CNY/6", child.Asset)
	child.ConversionNumerator = "999"
	_, e = ProductRuntimeCallContract(child)
	require.ErrorIs(t, e, ErrProductFundingConflict, "supplemental calls bind their frozen rate to the original price digest")
}

func TestProductCashSubMicroTopupsAndRefundsPreserveFullCharge(t *testing.T) {
	s, ledger := cashFundingFixture(t, 0.125) // 1/4 micro-CNY per quota
	ctx := context.Background()
	_, e := s.Reserve(ctx, ProductFundingAdmission{CallID: "micro", UserID: 42, TokenID: 7, Model: "m", Operation: "task", PriceSnapshot: `{"p":1}`, Quota: 1})
	require.NoError(t, e)
	require.NoError(t, s.TopUp(ctx, "micro", 4))
	assert.Empty(t, ledger.topups, "cash held does not change for these key-only topups")
	require.NoError(t, s.Settle(ctx, "micro", 4, `{}`))
	assert.Equal(t, 1, ledger.cashConsumed)
	require.NoError(t, RegisterProductRefundModels(s.db))
	for _, key := range []string{"a", "b", "c"} {
		require.NoError(t, s.Refund(ctx, "micro", key, 1, "partial"))
		assert.Equal(t, 1, ledger.cashConsumed)
	}
	assert.Empty(t, ledger.refunds, "zero monetary refunds only restore original key counters")
	require.NoError(t, s.Refund(ctx, "micro", "d", 1, "full"))
	assert.Zero(t, ledger.cashConsumed)
	assert.Len(t, ledger.refunds, 1)
}

func TestProductCashRefreshSharesExactReadAndFailsClosed(t *testing.T) {
	s, ledger := cashFundingFixture(t, 7.3)
	oldDB := DB
	DB = s.db
	t.Cleanup(func() { DB = oldDB })
	t.Setenv("UNIHUB_PRODUCT_RUNTIME_ENABLED", "true")
	t.Setenv("UNIHUB_PRODUCT_RUNTIME_TOKEN", "tttttttttttttttttttttttttttttttt")
	t.Setenv("UNIHUB_ORIGIN", ledger.origin)
	t.Setenv("UNIHUB_PRODUCT_ID", "llmrouter")
	t.Setenv("UNIHUB_PRODUCT_INSTANCE_ID", "default")
	ledger.cashBalance = 1001
	user := User{Id: 42}
	require.NoError(t, RefreshProductBalance(context.Background(), &user))
	assert.Equal(t, 68, user.Quota)
	require.NotNil(t, user.Funding)
	assert.Equal(t, "1001", user.Funding.Available)
	assert.Equal(t, "CNY/6", user.Funding.Asset)
	assert.Equal(t, "unified_cash", user.Funding.Mode)
	assert.Equal(t, 1, ledger.balanceCalls)
	ledger.rejectBalance = true
	require.Error(t, RefreshProductBalance(context.Background(), &user))
	assert.Nil(t, user.Funding)
	assert.Equal(t, 500, ledger.balance)
}

func TestProductCashInvalidOrOverflowingRatesRejectBeforeDebit(t *testing.T) {
	for _, rate := range []float64{-1, 0, 1e18} {
		t.Run(strconv.FormatFloat(rate, 'g', -1, 64), func(t *testing.T) {
			s, ledger := cashFundingFixture(t, rate)
			_, e := s.Reserve(context.Background(), ProductFundingAdmission{CallID: "bad-rate", UserID: 42, TokenID: 7, Model: "m", Operation: "chat", PriceSnapshot: `{"p":1}`, Quota: 10})
			require.ErrorIs(t, e, ErrProductFundingConflict)
			assert.Empty(t, ledger.calls)
			var key Token
			require.NoError(t, s.db.First(&key, 7).Error)
			assert.Equal(t, 200, key.RemainQuota)
			assert.Zero(t, key.UsedQuota)
		})
	}
}

func TestProductCashRetryUsesOriginalContractAfterModeAndRateChange(t *testing.T) {
	s, ledger := cashFundingFixture(t, 7.3)
	in := ProductFundingAdmission{CallID: "cash-retry", UserID: 42, TokenID: 7, Model: "m", Operation: "chat", PriceSnapshot: `{"p":1}`, Quota: 10}
	original, e := s.Reserve(context.Background(), in)
	require.NoError(t, e)
	t.Setenv("UNIHUB_PRODUCT_FUNDING_MODE", "")
	operation_setting.USDExchangeRate = 0
	retry, e := s.Reserve(context.Background(), in)
	require.NoError(t, e)
	assert.Equal(t, original.ReserveJSON, retry.ReserveJSON)
	assert.Equal(t, 146, ledger.cashHeld)
	in.PriceSnapshot = `{"p":2}`
	_, e = s.Reserve(context.Background(), in)
	require.ErrorIs(t, e, ErrProductFundingConflict)
}

func TestProductCashRepriceRoundsFamilyCumulatively(t *testing.T) {
	for _, tc := range []struct {
		name     string
		rate     float64
		expected []int
	}{
		{"default", 7.3, []int{15, 44, 15, 44, 30, 0, 44}},
		{"sub_micro", 0.125, []int{1, 1, 1, 1, 1, 0, 1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, ledger := cashFundingFixture(t, tc.rate)
			ctx := context.Background()
			require.NoError(t, RegisterProductRefundModels(s.db))
			_, e := s.Reserve(ctx, ProductFundingAdmission{CallID: "family", UserID: 42, TokenID: 7, Model: "m", Operation: "task", PriceSnapshot: `{"p":1}`, Quota: 1})
			require.NoError(t, e)
			require.NoError(t, s.Settle(ctx, "family", 1, `{}`))
			assert.Equal(t, tc.expected[0], ledger.cashConsumed)
			for i, target := range []int{3, 1, 3, 2, 0, 3} {
				key := strconv.Itoa(i)
				require.NoError(t, s.Reprice(ctx, "family", key, target, "measured"))
				require.NoError(t, s.Reprice(ctx, "family", key, target, "measured"))
				assert.Equal(t, tc.expected[i+1], ledger.cashConsumed, "target %d", target)
				var token Token
				require.NoError(t, s.db.First(&token, 7).Error)
				assert.Equal(t, 200-target, token.RemainQuota)
				assert.Equal(t, target, token.UsedQuota)
			}
		})
	}
}

func TestProductCashRefundRejectsOlderSegmentUntilChildrenReversed(t *testing.T) {
	s, ledger := cashFundingFixture(t, 0.125)
	ctx := context.Background()
	require.NoError(t, RegisterProductRefundModels(s.db))
	_, e := s.Reserve(ctx, ProductFundingAdmission{CallID: "ordered", UserID: 42, TokenID: 7, Model: "m", Operation: "task", PriceSnapshot: `{"p":1}`, Quota: 4})
	require.NoError(t, e)
	require.NoError(t, s.Settle(ctx, "ordered", 4, `{}`))
	require.NoError(t, s.Reprice(ctx, "ordered", "up", 5, "measured"))
	require.ErrorIs(t, s.Refund(ctx, "ordered", "older", 1, "partial"), ErrProductFundingConflict)
	assert.Equal(t, 2, ledger.cashConsumed)
	require.NoError(t, s.Reprice(ctx, "ordered", "down", 4, "measured"))
	assert.Equal(t, 1, ledger.cashConsumed)
	require.NoError(t, s.Refund(ctx, "ordered", "older", 1, "partial"))
	assert.Equal(t, 1, ledger.cashConsumed)
}

func TestProductCashCumulativeChildRecoversFrozenBaseOnce(t *testing.T) {
	s, ledger := cashFundingFixture(t, 7.3)
	ctx := context.Background()
	require.NoError(t, RegisterProductRefundModels(s.db))
	_, e := s.Reserve(ctx, ProductFundingAdmission{CallID: "recover-family", UserID: 42, TokenID: 7, Model: "m", Operation: "task", PriceSnapshot: `{"p":1}`, Quota: 1})
	require.NoError(t, e)
	require.NoError(t, s.Settle(ctx, "recover-family", 1, `{}`))
	ledger.loseReserveReply = true
	require.Error(t, s.Reprice(ctx, "recover-family", "up", 3, "measured"))
	operation_setting.USDExchangeRate = 100
	t.Setenv("UNIHUB_PRODUCT_FUNDING_MODE", "")
	require.NoError(t, s.RecoverRefunds(ctx))
	require.NoError(t, s.Reprice(ctx, "recover-family", "up", 3, "measured"))
	assert.Equal(t, 44, ledger.cashConsumed)
	assert.Equal(t, 2, ledger.settlements)
	var token Token
	require.NoError(t, s.db.First(&token, 7).Error)
	assert.Equal(t, 197, token.RemainQuota)
	assert.Equal(t, 3, token.UsedQuota)
}

func TestProductCashChildTopupAndRefundKeepCumulativeBase(t *testing.T) {
	s, ledger := cashFundingFixture(t, 0.125)
	ctx := context.Background()
	require.NoError(t, RegisterProductRefundModels(s.db))
	parent, e := s.Reserve(ctx, ProductFundingAdmission{CallID: "micro-parent", UserID: 42, TokenID: 7, Model: "m", Operation: "task", PriceSnapshot: `{"p":1}`, Quota: 1})
	require.NoError(t, e)
	require.NoError(t, s.Settle(ctx, parent.ID, 1, `{}`))
	child, e := s.Reserve(ctx, ProductFundingAdmission{CallID: "micro-child", ParentCallID: parent.ID, FinancialCommitment: true, ExternalCounters: true, UserID: 42, TokenID: 7, Model: "m", Operation: "task", PriceSnapshot: parent.ReserveJSON, Quota: 1})
	require.NoError(t, e)
	assert.Equal(t, "0", ledger.calls[child.ID].Amount)
	assert.Equal(t, 1, child.CashBaseQuota)
	tampered := child
	tampered.CashBaseQuota = 2
	_, e = ProductRuntimeCallContract(tampered)
	require.ErrorIs(t, e, ErrProductFundingConflict)
	ledger.loseTopUpReply = true
	require.Error(t, s.TopUp(ctx, child.ID, 4))
	require.NoError(t, s.Recover(ctx))
	require.NoError(t, s.Settle(ctx, child.ID, 4, `{}`))
	assert.Equal(t, 2, ledger.cashConsumed)
	ledger.loseRefundReply = true
	require.Error(t, s.Refund(ctx, child.ID, "partial", 1, "measured"))
	require.NoError(t, s.RecoverRefunds(ctx))
	require.NoError(t, s.Refund(ctx, child.ID, "partial", 1, "measured"))
	assert.Equal(t, 1, ledger.cashConsumed)
	require.NoError(t, s.Refund(ctx, child.ID, "rest", 3, "measured"))
	require.NoError(t, s.Refund(ctx, parent.ID, "rest", 1, "measured"))
	assert.Zero(t, ledger.cashConsumed)
	assert.Len(t, ledger.topups, 1)
	assert.Len(t, ledger.refunds, 2, "zero-cash child refunds do not call the ledger")
}

func TestProductCashTamperedBaseCannotPostOrConsumeKeyCapacity(t *testing.T) {
	for _, action := range []string{"topup", "settle", "dispatch"} {
		t.Run(action, func(t *testing.T) {
			s, ledger := cashFundingFixture(t, 7.3)
			ctx := context.Background()
			parent, e := s.Reserve(ctx, ProductFundingAdmission{CallID: "intact", UserID: 42, TokenID: 7, Model: "m", Operation: "task", PriceSnapshot: `{"p":1}`, Quota: 1})
			require.NoError(t, e)
			require.NoError(t, s.Settle(ctx, parent.ID, 1, `{}`))
			child, e := s.Reserve(ctx, ProductFundingAdmission{CallID: "tampered", ParentCallID: parent.ID, FinancialCommitment: true, ExternalCounters: true, UserID: 42, TokenID: 7, Model: "m", Operation: "task", PriceSnapshot: parent.ReserveJSON, Quota: 1})
			require.NoError(t, e)
			require.NoError(t, s.db.Model(&ProductRuntimeCall{}).Where("id = ?", child.ID).Update("cash_base_quota", 2).Error)
			if action == "topup" {
				e = s.TopUp(ctx, child.ID, 2)
			} else if action == "settle" {
				e = s.Settle(ctx, child.ID, 1, `{}`)
			} else {
				e = s.MarkDispatched(ctx, child.ID)
			}
			require.ErrorIs(t, e, ErrProductFundingConflict)
			assert.Equal(t, 1, ledger.settlements)
			assert.Empty(t, ledger.topups)
			var token Token
			require.NoError(t, s.db.First(&token, 7).Error)
			assert.Equal(t, 198, token.RemainQuota)
			assert.Equal(t, 2, token.UsedQuota)
		})
	}
}
