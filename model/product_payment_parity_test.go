package model

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/stretchr/testify/require"
)

func paymentParityFixture(t *testing.T) {
	t.Helper()
	_, db, _ := productFundingFixture(t)
	require.NoError(t, db.AutoMigrate(&TopUp{}, &Redemption{}, &Checkin{}, &Log{}, &ProductCreditAward{}, &SubscriptionPlan{}, &UserSubscription{}, &SubscriptionOrder{}))
	oldDB, oldLog := DB, LOG_DB
	DB, LOG_DB = db, db
	oldRedis := common.RedisEnabled
	common.RedisEnabled = false
	oldKind := common.MainDatabaseType()
	common.SetMainDatabaseType(common.DatabaseTypeSQLite)
	t.Cleanup(func() {
		DB, LOG_DB = oldDB, oldLog
		common.RedisEnabled = oldRedis
		common.SetMainDatabaseType(oldKind)
	})
	require.NoError(t, db.Model(&User{}).Where("id = ?", 42).Update("quota", 0).Error)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) }))
	t.Cleanup(server.Close)
	t.Setenv("UNIHUB_PRODUCT_RUNTIME_ENABLED", "true")
	t.Setenv("UNIHUB_ORIGIN", server.URL)
	t.Setenv("UNIHUB_PRODUCT_RUNTIME_TOKEN", strings.Repeat("t", 32))
	t.Setenv("UNIHUB_PRODUCT_ID", "llmrouter")
	t.Setenv("UNIHUB_PRODUCT_INSTANCE_ID", "default")
	scope, err := ProductRuntimeScopeDigest()
	require.NoError(t, err)
	require.NoError(t, db.Create(&ProductFundingAccount{ScopeDigest: scope, UserID: 42, AccountID: "payer", Asset: "LR_QUOTA/0", UnitVersion: "lr-quota-v1", State: "central"}).Error)
}

func assertPaymentAward(t *testing.T, kind string, quota int) {
	t.Helper()
	var user User
	require.NoError(t, DB.First(&user, 42).Error)
	require.Zero(t, user.Quota)
	var awards []ProductCreditAward
	require.NoError(t, DB.Find(&awards).Error)
	require.Len(t, awards, 1)
	require.Equal(t, "pending", awards[0].State)
	require.Contains(t, awards[0].Request, `"source_kind":"`+kind+`"`)
	require.Contains(t, awards[0].Request, fmt.Sprintf(`"amount":"%d"`, quota))
}

func TestProductPaymentCallbacksRetainPaidReceiptAndQueueOneCentralAward(t *testing.T) {
	for _, provider := range []string{PaymentProviderEpay, PaymentProviderStripe, PaymentProviderCreem, PaymentProviderWaffo, PaymentProviderWaffoPancake, PaymentProviderWeChatPay, "manual"} {
		t.Run(provider, func(t *testing.T) {
			paymentParityFixture(t)
			quota := common.QuotaFromFloat(2 * common.QuotaPerUnit)
			order := &TopUp{UserId: 42, TradeNo: "verified-order-" + provider, Amount: 2, Money: 2, PaymentMethod: provider, PaymentProvider: provider, Status: common.TopUpStatusPending, CreateTime: time.Now().Unix()}
			if provider == PaymentProviderCreem {
				quota = 2
			}
			if provider == PaymentProviderWeChatPay {
				order.PaymentMethod = PaymentMethodWeChatNative
				order.Currency = "CNY"
				order.PaidAmountMinor = 200
				order.CreditedQuota = quota
			}
			require.NoError(t, order.Insert())
			switch provider {
			case PaymentProviderEpay:
				_, err := RechargeEpay(order.TradeNo, provider, "")
				require.NoError(t, err)
				_, err = RechargeEpay(order.TradeNo, provider, "")
				require.NoError(t, err)
			case PaymentProviderStripe:
				require.NoError(t, Recharge(order.TradeNo, "stripe-customer", ""))
			case PaymentProviderCreem:
				require.NoError(t, RechargeCreem(order.TradeNo, "", "", ""))
			case PaymentProviderWaffo:
				require.NoError(t, RechargeWaffo(order.TradeNo, ""))
				require.NoError(t, RechargeWaffo(order.TradeNo, ""))
			case PaymentProviderWaffoPancake:
				require.NoError(t, RechargeWaffoPancake(order.TradeNo))
				require.NoError(t, RechargeWaffoPancake(order.TradeNo))
			case PaymentProviderWeChatPay:
				in := WeChatTopUpSettlement{TradeNo: order.TradeNo, ProviderTradeNo: "verified-provider-trade", AmountMinor: 200, Currency: "CNY"}
				_, _, _, err := SettleWeChatTopUp(in)
				require.NoError(t, err)
				_, _, _, err = SettleWeChatTopUp(in)
				require.NoError(t, err)
			case "manual":
				require.NoError(t, ManualCompleteTopUp(order.TradeNo, ""))
				require.NoError(t, ManualCompleteTopUp(order.TradeNo, ""))
			}
			require.Equal(t, common.TopUpStatusSuccess, GetTopUpByTradeNo(order.TradeNo).Status)
			assertPaymentAward(t, "payment", quota)
		})
	}
}

func TestProductRedemptionKeepsOriginalOneTimeCodeAndQueuesCentralAward(t *testing.T) {
	paymentParityFixture(t)
	code := &Redemption{Key: "trusted-redemption-key", Quota: 25, Status: common.RedemptionCodeStatusEnabled}
	require.NoError(t, code.Insert())
	quota, err := Redeem(code.Key, 42)
	require.NoError(t, err)
	require.Equal(t, 25, quota)
	_, err = Redeem(code.Key, 42)
	require.Error(t, err)
	assertPaymentAward(t, "redemption", 25)
}

func TestProductCheckinSQLiteCreatesOriginalReceiptAndCentralAwardAtomically(t *testing.T) {
	paymentParityFixture(t)
	setting := operation_setting.GetCheckinSetting()
	old := *setting
	setting.Enabled = true
	setting.MinQuota = 12
	setting.MaxQuota = 12
	t.Cleanup(func() { *setting = old })
	receipt, err := UserCheckin(42)
	require.NoError(t, err)
	require.Equal(t, 12, receipt.QuotaAwarded)
	_, err = UserCheckin(42)
	require.Error(t, err)
	assertPaymentAward(t, "checkin", 12)
	require.Error(t, FlushProductCreditAwards(context.Background(), 42))
}
