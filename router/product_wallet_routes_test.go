package router

import (
	"net/http"
	"testing"
	"time"

	userkit "github.com/NeoMei/UniHub/packages/userkit-go"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestProductCashWalletReadsScopedPendingAndRejectsLegacyWrites(t *testing.T) {
	f, user, bundle := newCentralProductRouteFixture(t)
	require.NoError(t, f.db.AutoMigrate(&model.TopUp{}))
	t.Setenv("UNIHUB_PRODUCT_FUNDING_MODE", "unified_cash")
	order, _, err := model.CreateOrReuseWeChatTopUp(model.WeChatTopUpOrderInput{UserID: user.Id, TradeNo: "product-cash-pending", AmountMinor: 100, CreditedQuota: 0, CreateTime: time.Now().Unix()})
	require.NoError(t, err)
	require.NoError(t, f.db.Create(&model.TopUp{UserId: user.Id, TradeNo: "native-history-hidden", Status: common.TopUpStatusPending, CreateTime: time.Now().Unix()}).Error)
	info := productRouteRequest(f, "GET", "/api/user/topup/info", bundle.AccessToken, "", "")
	require.Equal(t, http.StatusOK, info.Code, info.Body.String())
	assert.Contains(t, info.Body.String(), `"funding_mode":"unified_cash"`)
	assert.Contains(t, info.Body.String(), `"enable_redemption":false`)
	history := productRouteRequest(f, "GET", "/api/user/topup/self", bundle.AccessToken, "", "")
	require.Equal(t, http.StatusOK, history.Code, history.Body.String())
	assert.Contains(t, history.Body.String(), order.TradeNo)
	assert.Contains(t, history.Body.String(), `"credit_amount":"1000000"`)
	assert.NotContains(t, history.Body.String(), "native-history-hidden")
	status := productRouteRequest(f, "GET", "/api/user/wechat-pay/orders/"+order.TradeNo, bundle.AccessToken, "", "")
	require.Equal(t, http.StatusOK, status.Code, status.Body.String())
	assert.Contains(t, status.Body.String(), userkit.RuntimeCashAsset)
	native := productRouteRequest(f, "GET", "/api/user/wechat-pay/orders/native-history-hidden", bundle.AccessToken, "", "")
	assert.Equal(t, http.StatusNotFound, native.Code, native.Body.String())
	t.Setenv("NATIVE_PUBLIC_ACCESS", "true")
	for _, path := range []string{"/api/user/pay", "/api/user/stripe/pay", "/api/user/creem/pay", "/api/user/waffo/pay", "/api/user/waffo-pancake/pay", "/api/user/topup", "/api/user/aff_transfer", "/api/user/checkin"} {
		denied := productRouteRequest(f, "POST", path, bundle.AccessToken, "https://dashboard.test", `{"amount":100,"key":"redeem"}`)
		assert.Equal(t, http.StatusForbidden, denied.Code, path+" "+denied.Body.String())
	}
	var count int64
	require.NoError(t, f.db.Model(&model.TopUp{}).Count(&count).Error)
	assert.Equal(t, int64(2), count)
	for _, origin := range []string{"", "https://attacker.test"} {
		denied := productRouteRequest(f, "POST", "/api/user/wechat-pay/native", bundle.AccessToken, origin, `{"amount":"1.00","idempotency_key":"123e4567-e89b-12d3-a456-426614174000"}`)
		assert.Equal(t, http.StatusForbidden, denied.Code, denied.Body.String())
	}
	require.NoError(t, f.db.Model(&model.User{}).Where("id = ?", user.Id).Update("funding_realm", model.NativeFundingLocalOnly).Error)
	denied := productRouteRequest(f, "GET", "/api/user/topup/self", bundle.AccessToken, "", "")
	assert.Equal(t, http.StatusForbidden, denied.Code, denied.Body.String())
}

func TestProductCashTopUpInfoSuppressesConfiguredLegacyProviders(t *testing.T) {
	f, _, bundle := newCentralProductRouteFixture(t)
	t.Setenv("UNIHUB_PRODUCT_FUNDING_MODE", "unified_cash")
	payment := operation_setting.GetPaymentSetting()
	old := *payment
	t.Cleanup(func() { *payment = old })
	payment.ComplianceConfirmed = true
	payment.ComplianceTermsVersion = operation_setting.CurrentComplianceTermsVersion
	oldMethods := operation_setting.PayMethods
	operation_setting.PayMethods = []map[string]string{{"name": "Legacy", "type": "alipay"}}
	t.Cleanup(func() { operation_setting.PayMethods = oldMethods })
	response := productRouteRequest(f, "GET", "/api/user/topup/info", bundle.AccessToken, "", "")
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	var parsed struct {
		Data map[string]any `json:"data"`
	}
	require.NoError(t, common.Unmarshal(response.Body.Bytes(), &parsed))
	assert.Equal(t, false, parsed.Data["enable_online_topup"])
	assert.Equal(t, false, parsed.Data["enable_stripe_topup"])
	assert.Empty(t, parsed.Data["pay_methods"])
	assert.Equal(t, false, parsed.Data["enable_checkin"])
	assert.Equal(t, false, parsed.Data["enable_affiliate_transfer"])
}

func TestProductCashSubscriptionUsesOriginalReadsPreferencesAndBalancePurchase(t *testing.T) {
	f, user, bundle := newCentralProductRouteFixture(t)
	t.Setenv("UNIHUB_PRODUCT_FUNDING_MODE", "unified_cash")
	require.NoError(t, f.db.AutoMigrate(&model.SubscriptionPlan{}, &model.UserSubscription{}, &model.SubscriptionPreConsumeRecord{}))
	for _, path := range []string{"/api/subscription/plans", "/api/subscription/self"} {
		response := productRouteRequest(f, "GET", path, bundle.AccessToken, "", "")
		require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	}
	preference := productRouteRequest(f, "PUT", "/api/subscription/self/preference", bundle.AccessToken, "https://dashboard.test", `{"billing_preference":"subscription_first"}`)
	require.Equal(t, http.StatusOK, preference.Code, preference.Body.String())
	assert.Contains(t, preference.Body.String(), `"billing_preference":"subscription_first"`)
	denied := productRouteRequest(f, "PUT", "/api/subscription/self/preference", bundle.AccessToken, "https://attacker.test", `{"billing_preference":"wallet_first"}`)
	assert.Equal(t, http.StatusForbidden, denied.Code, denied.Body.String())
	purchase := productRouteRequest(f, "POST", "/api/subscription/balance/pay", bundle.AccessToken, "https://dashboard.test", `{"plan_id":0}`)
	require.Equal(t, http.StatusOK, purchase.Code, purchase.Body.String())
	assert.Contains(t, purchase.Body.String(), `"success":false`, "validated product route must reach original purchase input validation")
	t.Setenv("NATIVE_PUBLIC_ACCESS", "true")
	for _, provider := range []string{"epay", "stripe", "creem", "waffo-pancake"} {
		response := productRouteRequest(f, "POST", "/api/subscription/"+provider+"/pay", bundle.AccessToken, "https://dashboard.test", `{"plan_id":1}`)
		assert.Equal(t, http.StatusForbidden, response.Code, response.Body.String())
	}
	require.NoError(t, f.db.Model(&model.User{}).Where("id = ?", user.Id).Update("funding_realm", model.NativeFundingLocalOnly).Error)
	shared := productRouteRequest(f, "GET", "/api/subscription/self", bundle.AccessToken, "", "")
	assert.Equal(t, http.StatusForbidden, shared.Code, shared.Body.String())
}
