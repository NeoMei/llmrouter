package model

import (
	"context"
	"gorm.io/gorm"
	"math"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	userkit "github.com/NeoMei/UniHub/packages/userkit-go"
	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A cash payment must use the verified CNY receipt, never the LR quota display
// conversion. Repeated notifications retain one immutable outbox command.
func TestCashWeChatPaymentFreezesMoneyAndQueuesExactReceiptOnce(t *testing.T) {
	paymentParityFixture(t)
	t.Setenv("UNIHUB_PRODUCT_FUNDING_MODE", "unified_cash")
	order, _, err := CreateOrReuseWeChatTopUp(WeChatTopUpOrderInput{UserID: 42, TradeNo: "cash-verified", AmountMinor: 5001, CreditedQuota: 7, CreateTime: time.Now().Unix()})
	require.NoError(t, err)
	raw, err := common.Marshal(order)
	require.NoError(t, err)
	var snapshot map[string]any
	require.NoError(t, common.Unmarshal(raw, &snapshot))
	assert.Equal(t, "CNY/6", snapshot["funding_asset"])
	assert.Equal(t, "lr-cny-v1", snapshot["funding_unit_version"])
	t.Setenv("UNIHUB_PRODUCT_FUNDING_MODE", "")
	in := WeChatTopUpSettlement{TradeNo: order.TradeNo, ProviderTradeNo: "verified-wx-txn", AmountMinor: 5001, Currency: "CNY"}
	_, _, _, err = SettleWeChatTopUp(in)
	require.NoError(t, err)
	_, _, _, err = SettleWeChatTopUp(in)
	require.NoError(t, err)
	var awards []ProductCreditAward
	require.NoError(t, DB.Find(&awards).Error)
	require.Len(t, awards, 1)
	var req userkit.RuntimeAwardRequest
	require.NoError(t, common.UnmarshalJsonStr(awards[0].Request, &req))
	assert.Equal(t, "CNY/6", req.Asset)
	assert.Equal(t, "lr-cny-v1", req.UnitVersion)
	assert.Equal(t, "50010000", req.Amount)
	assert.Equal(t, "payer", req.AccountID)
	assert.Equal(t, productDigest(string(req.SourcePayload)), req.ReceiptDigest)
	var receipt map[string]any
	require.NoError(t, common.Unmarshal(req.SourcePayload, &receipt))
	assert.Equal(t, "verified-wx-txn", receipt["provider_trade_no"])
	assert.Equal(t, float64(5001), receipt["paid_amount_minor"])
	var user User
	require.NoError(t, DB.First(&user, 42).Error)
	assert.Zero(t, user.Quota)
}

func TestLegacyWeChatPendingOrderKeepsLRWhenCashModeEnabled(t *testing.T) {
	paymentParityFixture(t)
	order := &TopUp{UserId: 42, TradeNo: "old-lr-pending", PaymentProvider: PaymentProviderWeChatPay, PaymentMethod: PaymentMethodWeChatNative, Currency: "CNY", PaidAmountMinor: 5001, CreditedQuota: 9, Status: common.TopUpStatusPending}
	require.NoError(t, order.Insert())
	t.Setenv("UNIHUB_PRODUCT_FUNDING_MODE", "unified_cash")
	_, _, _, err := SettleWeChatTopUp(WeChatTopUpSettlement{TradeNo: order.TradeNo, ProviderTradeNo: "legacy-txn", AmountMinor: 5001, Currency: "CNY"})
	require.NoError(t, err)
	var award ProductCreditAward
	require.NoError(t, DB.First(&award).Error)
	var req userkit.RuntimeAwardRequest
	require.NoError(t, common.UnmarshalJsonStr(award.Request, &req))
	assert.Equal(t, "LR_QUOTA/0", req.Asset)
	assert.Equal(t, "9", req.Amount)
}

func TestCashPaymentLostLedgerReceiptRetriesSameCashCommand(t *testing.T) {
	paymentParityFixture(t)
	t.Setenv("UNIHUB_PRODUCT_FUNDING_MODE", "unified_cash")
	requests := map[string]userkit.RuntimeAwardRequest{}
	var total int64
	failReply := true
	hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/internal/v1/product-runtime/awards" {
			raw, _ := common.Marshal(userkit.RuntimeBalance{AccountID: "payer", Asset: "LR_QUOTA/0", UnitVersion: "lr-quota-v1", Available: "0", Held: "0", Consumed: "0"})
			_, _ = w.Write(raw)
			return
		}
		var req userkit.RuntimeAwardRequest
		require.NoError(t, common.DecodeJson(r.Body, &req))
		if old, ok := requests[req.ReceiptID]; ok {
			assert.Equal(t, old, req)
		} else {
			requests[req.ReceiptID] = req
			n, e := strconv.ParseInt(req.Amount, 10, 64)
			require.NoError(t, e)
			total += n
		}
		if failReply {
			w.WriteHeader(503)
			return
		}
		raw, _ := common.Marshal(userkit.RuntimeAdjustment{ID: "receipt", ProductID: "llmrouter", InstanceID: "default", AccountID: req.AccountID, Amount: req.Amount, Status: "APPLIED"})
		_, _ = w.Write(raw)
	}))
	t.Cleanup(hub.Close)
	t.Setenv("UNIHUB_ORIGIN", hub.URL)
	scope, err := ProductRuntimeScopeDigest()
	require.NoError(t, err)
	require.NoError(t, DB.Model(&ProductFundingAccount{}).Where("user_id = ?", 42).Update("scope_digest", scope).Error)
	order, _, err := CreateOrReuseWeChatTopUp(WeChatTopUpOrderInput{UserID: 42, TradeNo: "lost-cash-receipt", AmountMinor: 101, CreditedQuota: 3, CreateTime: time.Now().Unix()})
	require.NoError(t, err)
	_, _, _, err = SettleWeChatTopUp(WeChatTopUpSettlement{TradeNo: order.TradeNo, ProviderTradeNo: "cash-provider-lost", AmountMinor: 101, Currency: "CNY"})
	require.NoError(t, err)
	var pending ProductCreditAward
	require.NoError(t, DB.First(&pending).Error)
	assert.Equal(t, "pending", pending.State)
	failReply = false
	require.NoError(t, FlushProductCreditAwards(context.Background(), 42))
	require.NoError(t, FlushProductCreditAwards(context.Background(), 42))
	assert.Equal(t, int64(1010000), total)
	assert.Len(t, requests, 1)
	require.NoError(t, DB.First(&pending).Error)
	assert.Equal(t, "applied", pending.State)
}

func TestProductTopUpHistoryShowsOnlyScopedTaggedReceiptsIncludingPending(t *testing.T) {
	paymentParityFixture(t)
	t.Setenv("UNIHUB_PRODUCT_FUNDING_MODE", "unified_cash")
	pending, _, err := CreateOrReuseWeChatTopUp(WeChatTopUpOrderInput{UserID: 42, TradeNo: "mine-pending", AmountMinor: 100, CreditedQuota: 1, CreateTime: time.Now().Unix()})
	require.NoError(t, err)
	paid, _, err := CreateOrReuseWeChatTopUp(WeChatTopUpOrderInput{UserID: 42, TradeNo: "mine-paid", AmountMinor: 100, CreditedQuota: 1, CreateTime: time.Now().Unix()})
	require.NoError(t, err)
	require.NoError(t, DB.Model(paid).Update("status", common.TopUpStatusSuccess).Error)
	native := *pending
	native.Id = 0
	native.TradeNo = "native-untagged"
	native.FundingScopeDigest = ""
	native.FundingAccountID = ""
	require.NoError(t, DB.Create(&native).Error)
	foreign := *pending
	foreign.Id = 0
	foreign.TradeNo = "other-account"
	foreign.FundingAccountID = "other-payer"
	require.NoError(t, DB.Create(&foreign).Error)
	foreign.Id = 0
	foreign.TradeNo = "other-deployment"
	foreign.FundingAccountID = pending.FundingAccountID
	foreign.FundingScopeDigest = "other-scope"
	require.NoError(t, DB.Create(&foreign).Error)
	t.Setenv("UNIHUB_PRODUCT_FUNDING_MODE", "")
	rows, total, err := GetProductUserTopUps(context.Background(), 42, "mine%", &common.PageInfo{Page: 1, PageSize: 10})
	require.NoError(t, err)
	assert.Equal(t, int64(2), total)
	require.Len(t, rows, 2)
	assert.Equal(t, "mine-paid", rows[0].TradeNo)
	assert.Equal(t, "mine-pending", rows[1].TradeNo)
	_, err = GetProductUserWeChatTopUp(context.Background(), 42, native.TradeNo)
	require.ErrorIs(t, err, ErrTopUpNotFound)
	require.NoError(t, DB.Model(&User{}).Where("id = ?", 42).Update("funding_realm", NativeFundingLocalOnly).Error)
	_, _, err = GetProductUserTopUps(context.Background(), 42, "", &common.PageInfo{Page: 1, PageSize: 10})
	require.Error(t, err)
}

func TestCashPaymentCannotBorrowAnotherFundingAccountOrCreditBothAssets(t *testing.T) {
	for _, mismatch := range []string{"funding_account_id", "funding_scope_digest", "funding_unit_version", "double_asset"} {
		t.Run(mismatch, func(t *testing.T) {
			paymentParityFixture(t)
			t.Setenv("UNIHUB_PRODUCT_FUNDING_MODE", "unified_cash")
			order, _, err := CreateOrReuseWeChatTopUp(WeChatTopUpOrderInput{UserID: 42, TradeNo: "immutable-payment", AmountMinor: 100, CreditedQuota: 7, CreateTime: time.Now().Unix()})
			require.NoError(t, err)
			if mismatch == "double_asset" {
				require.NoError(t, DB.Transaction(func(tx *gorm.DB) error {
					_, err := QueueProductCreditAward(tx, 42, "payment", productDigest(PaymentProviderWeChatPay+":"+order.TradeNo), 7, map[string]any{"old_receipt": true})
					return err
				}))
			} else {
				require.NoError(t, DB.Model(order).Update(mismatch, "other-contract").Error)
			}
			_, _, _, err = SettleWeChatTopUp(WeChatTopUpSettlement{TradeNo: order.TradeNo, ProviderTradeNo: "verified-payment-txn", AmountMinor: 100, Currency: "CNY"})
			require.Error(t, err)
			require.Equal(t, common.TopUpStatusPending, GetTopUpByTradeNo(order.TradeNo).Status)
			var user User
			require.NoError(t, DB.First(&user, 42).Error)
			assert.Zero(t, user.Quota)
		})
	}
}

func TestLRPaymentSnapshotCannotFollowReboundAccount(t *testing.T) {
	paymentParityFixture(t)
	t.Setenv("UNIHUB_PRODUCT_FUNDING_MODE", "")
	order, _, err := CreateOrReuseWeChatTopUp(WeChatTopUpOrderInput{UserID: 42, TradeNo: "frozen-lr-owner", AmountMinor: 100, CreditedQuota: 7, CreateTime: time.Now().Unix()})
	require.NoError(t, err)
	require.Equal(t, "LR_QUOTA/0", order.FundingAsset)
	require.NoError(t, DB.Model(&ProductFundingAccount{}).Where("user_id = ?", 42).Update("account_id", "rebound").Error)
	_, _, _, err = SettleWeChatTopUp(WeChatTopUpSettlement{TradeNo: order.TradeNo, ProviderTradeNo: "lr-provider-txn", AmountMinor: 100, Currency: "CNY"})
	require.Error(t, err)
	assert.Equal(t, common.TopUpStatusPending, GetTopUpByTradeNo(order.TradeNo).Status)
}

func TestCashCheckoutRejectsMicroAmountOverflowBeforeCreatingOrder(t *testing.T) {
	paymentParityFixture(t)
	t.Setenv("UNIHUB_PRODUCT_FUNDING_MODE", "unified_cash")
	_, _, err := CreateOrReuseWeChatTopUp(WeChatTopUpOrderInput{UserID: 42, TradeNo: "overflow-cash", AmountMinor: math.MaxInt64/10000 + 1, CreateTime: time.Now().Unix()})
	require.Error(t, err)
	var count int64
	require.NoError(t, DB.Model(&TopUp{}).Count(&count).Error)
	assert.Zero(t, count)
}
