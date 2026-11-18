package router

import (
	"bytes"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/internal/platformbilling"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/billing_setting"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestProductCreditRelayFreezesPublicPriceAndDoesNotReplayUnknownProvider(t *testing.T) {
	origin := os.Getenv("PRODUCT_CREDIT_INTEGRATION_ORIGIN")
	integration := origin != ""
	product, instance := "agentwiki", "default"
	credential, proof, serviceToken := "uh_key_test", "product-proof", "service-service-service-service-token"
	callerKey := "caller-one"
	if integration {
		endpoint, err := url.Parse(origin)
		require.NoError(t, err)
		ip := net.ParseIP(endpoint.Hostname())
		require.True(t, endpoint.Scheme == "http" && (endpoint.Hostname() == "localhost" || ip != nil && ip.IsLoopback()), "integration must use an isolated loopback UniHub")
		product = "superwagie"
		if target := os.Getenv("PRODUCT_CREDIT_INTEGRATION_INSTANCE"); target != "" {
			instance = target
		}
		serviceToken = os.Getenv("PRODUCT_CREDIT_INTEGRATION_SERVICE_TOKEN")
		credential = os.Getenv("PRODUCT_CREDIT_INTEGRATION_KEY")
		proof = os.Getenv("PRODUCT_CREDIT_INTEGRATION_PROOF")
		require.True(t, len(serviceToken) >= 32 && platformbilling.IsModelCredential(credential) && proof != "", "integration authority is required")
		callerKey = "product-integration-" + uuid.NewString()
	}
	enableUniHubBoundary(t)
	require.NoError(t, i18n.Init())
	oldDB, oldLogDB, oldCache, oldRedis, oldRetry := model.DB, model.LOG_DB, common.MemoryCacheEnabled, common.RedisEnabled, common.RetryTimes
	oldGlobal := common.GlobalApiRateLimitEnable
	oldFX, oldQuota := operation_setting.USDExchangeRate, common.QuotaPerUnit
	oldGroup, oldTariff := ratio_setting.GroupRatio2JSONString(), billing_setting.ProductCreditPricingJSONString()
	t.Cleanup(func() {
		model.DB, model.LOG_DB, common.MemoryCacheEnabled, common.RedisEnabled, common.RetryTimes = oldDB, oldLogDB, oldCache, oldRedis, oldRetry
		common.GlobalApiRateLimitEnable = oldGlobal
		operation_setting.USDExchangeRate, common.QuotaPerUnit = oldFX, oldQuota
		require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(oldGroup))
		require.NoError(t, billing_setting.SetProductCreditPricing(oldTariff))
	})
	common.MemoryCacheEnabled, common.RedisEnabled, common.RetryTimes, common.GlobalApiRateLimitEnable = true, false, 2, false
	operation_setting.USDExchangeRate, common.QuotaPerUnit = 1, 1000000
	require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(`{"default":5,"platform":1}`))
	require.NoError(t, billing_setting.SetProductCreditPricing(`{
	   "version":"rev1",
	   "suppliers":{"public-credit-model":{"chat":{
	     "a":{"version":"a-1","currency":"USD","tariff":{"version":"metered_v2","operation":"chat","policy":"provider_reported_v1","terms":[
	       {"key":"input","unit":"token","price":{"numerator":"2","denominator":"1000000"}},
	       {"key":"output","unit":"token","price":{"numerator":"6","denominator":"1000000"}}]}},
	     "b":{"version":"b-1","currency":"USD","tariff":{"version":"metered_v2","operation":"chat","policy":"provider_reported_v1","terms":[
	       {"key":"input","unit":"token","price":{"numerator":"4","denominator":"1000000"}},
	       {"key":"output","unit":"token","price":{"numerator":"2","denominator":"1000000"}}]}}
	   }}}
	}`))
	db, err := gorm.Open(sqlite.Open(t.TempDir()+"/product-relay.db"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Model{}, &model.Vendor{}, &model.Log{}, &model.User{}, &model.Token{}, &model.UserSubscription{}, &model.Channel{}, &model.Ability{}, &model.ClientModelRoute{}, &model.ClientModelTarget{}, &model.UpstreamAccountPool{}, &model.UpstreamAccountMember{}, &model.UpstreamAccountState{}))
	model.DB, model.LOG_DB = db, db
	require.NoError(t, db.Create(&model.User{Id: 1, Username: "routing", Status: common.UserStatusEnabled, Group: "default", Quota: 777}).Error)
	providerCalls, reserves, settlements := 0, 0, 0
	withUsage := true
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		providerCalls++
		assert.Empty(t, r.Header.Get(platformbilling.ProductAuthorizationHeader))
		assert.Empty(t, r.Header.Get("Idempotency-Key"))
		var in struct {
			Model string `json:"model"`
		}
		require.NoError(t, common.DecodeJson(r.Body, &in))
		assert.Contains(t, []string{"upstream-one", "upstream-two"}, in.Model)
		w.Header().Set("Content-Type", "application/json")
		raw := fmt.Sprintf(`{"id":"answer","object":"chat.completion","model":%q,"choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}]`, in.Model)
		if withUsage {
			raw += `,"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}`
		}
		_, _ = w.Write([]byte(raw + "}"))
	}))
	t.Cleanup(provider.Close)
	require.NoError(t, db.Create(&model.ClientModelRoute{Id: 1, PublicId: "public-credit-model", Client: product, DisplayName: "Public", Enabled: true, Metadata: `{"operations":["chat.completions.create"]}`, Aliases: `["product-alias"]`}).Error)
	for i, upstream := range []string{"upstream-one", "upstream-two"} {
		id, base := i+1, provider.URL
		require.NoError(t, db.Create(&model.Channel{Id: id, Type: constant.ChannelTypeOpenAI, Key: "fixture-provider-key", Status: common.ChannelStatusEnabled, BaseURL: &base, Name: upstream, Models: upstream, Group: "default"}).Error)
		require.NoError(t, db.Create(&model.Ability{Group: "default", Model: upstream, ChannelId: id, Enabled: true}).Error)
		require.NoError(t, db.Create(&model.ClientModelTarget{Id: id, RouteId: 1, ChannelId: id, UpstreamModel: upstream, BillingModel: upstream, PreferredWires: `["openai_chat"]`, Enabled: true, Weight: 1 + 2*i}).Error)
	}
	model.InitChannelCache()
	service.InitHttpClient()
	service.InitTokenEncoders()
	conversion := platformbilling.NativeConversion{Version: "product-fixture-1", Numerator: "10", Denominator: "1", Rounding: "lr-cumulative-ceil-v1"}
	grant := platformbilling.Claims{CreditScope: platformbilling.ProductCreditScope, SourceVerified: true, FundingProductID: "agentwiki",
		ID: "stable-key", CredentialKind: "api_key", InstanceID: "default", Audience: "llmrouter", ActorID: "actor", AccountID: "team", OriginProductID: "agentwiki",
		ModelScope: "llmrouter_available", PricingSource: platformbilling.NativeCreditPricingSource, CredentialVersion: 2, Asset: "CREDIT/6", FundingMode: "credits", NativeCreditConversion: &conversion}
	remote := map[string]platformbilling.RemoteCall{}
	hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var output any
		switch {
		case r.URL.Path == "/internal/v1/model-credentials/verify":
			var in map[string]string
			require.NoError(t, common.DecodeJson(r.Body, &in))
			if in["product_authorization"] != "product-proof" {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			output = grant
		case r.URL.Path == "/internal/v1/calls/native-reserve":
			reserves++
			var in platformbilling.NativeReserveRequest
			require.NoError(t, common.DecodeJson(r.Body, &in))
			assert.Equal(t, platformbilling.ProductCreditScope, in.CreditScope)
			assert.Equal(t, "product-proof", in.ProductAuthorization)
			assert.Empty(t, in.MinimumCreditAmount)
			row := platformbilling.RemoteCall{CreditScope: in.CreditScope, CredentialVersion: grant.CredentialVersion, TaskID: in.TaskID, CallID: in.CallID, CredentialID: grant.ID, InstanceID: grant.InstanceID,
				ActorID: grant.ActorID, AccountID: grant.AccountID, OriginProductID: grant.OriginProductID, RequestDigest: in.RequestDigest, WireOperation: in.WireOperation, NativePricing: &in.Quote,
				Rate: platformbilling.Rate{Model: in.Model, Asset: "CREDIT/6", Semantics: "llmrouter_native"}, Held: in.Quote.MaxAmount, ChargeAsset: "CREDIT/6", FundingMode: "credits", FundingProductID: "agentwiki", Status: "RESERVED"}
			remote[in.CallID], output = row, row
		case strings.HasSuffix(r.URL.Path, "/native-settle"):
			settlements++
			id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/internal/v1/calls/"), "/native-settle")
			var in platformbilling.NativeSettleRequest
			require.NoError(t, common.DecodeJson(r.Body, &in))
			assert.Equal(t, "105", in.NativeQuota)
			assert.Equal(t, "1050", in.Amount)
			assert.Empty(t, in.ProviderCost)
			require.NotNil(t, in.Usage)
			assert.Equal(t, "confirmed", in.Usage.Metered.State)
			row := remote[id]
			row.Status, row.Charged = "SETTLED", in.Amount
			remote[id], output = row, row
		default:
			t.Errorf("unexpected central endpoint %s", r.URL.Path)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		raw, err := common.Marshal(output)
		require.NoError(t, err)
		_, _ = w.Write(raw)
	}))
	t.Cleanup(hub.Close)
	if !integration {
		origin = hub.URL
	}
	adapter, err := platformbilling.New(db, platformbilling.Config{Enabled: true, Origin: origin, ServiceToken: serviceToken, InstanceID: instance,
		PrincipalID: 1, Group: "platform", NativeGroup: "default", MaxBodyBytes: 8192, MaxInputTokens: 1000, MaxOutputTokens: 100})
	require.NoError(t, err)
	r := gin.New()
	SetManagedKeyDispatch(r, adapter)
	SetRelayRouter(r)
	send := func(key, proof string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/platform/v1/chat/completions", bytes.NewBufferString(`{"model":"product-alias","messages":[{"role":"user","content":"hello"}]}`))
		req.Header.Set("Authorization", "Bearer "+credential)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", key)
		if proof != "" {
			req.Header.Set(platformbilling.ProductAuthorizationHeader, proof)
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}
	w := send(callerKey, proof)
	require.Equal(t, 200, w.Code, w.Body.String())
	id := w.Header().Get("X-UniHub-Call-ID")
	require.True(t, strings.HasPrefix(id, "pc_"))
	var row platformbilling.Call
	require.NoError(t, db.First(&row, "call_id = ?", id).Error)
	assert.Equal(t, "SETTLED", row.Status)
	assert.Equal(t, "1050", row.Charged)
	assert.Contains(t, row.NativePriceSnapshot, "public-credit-model")
	assert.Contains(t, row.Evidence, `"state":"confirmed"`)
	assert.False(t, strings.Contains(row.NativePriceSnapshot, proof), "pricing evidence must never persist proof")
	w = send(callerKey, proof)
	assert.Equal(t, 409, w.Code, w.Body.String())
	get := httptest.NewRequest(http.MethodGet, "/platform/v1/calls/"+callerKey, nil)
	get.Header.Set("Authorization", "Bearer "+credential)
	get.Header.Set(platformbilling.ProductAuthorizationHeader, proof)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, get)
	require.Equal(t, 200, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), `"idempotency_key":"`+callerKey+`"`)
	assert.Contains(t, w.Body.String(), `"funding_product_id":"`+product+`"`)
	assert.Contains(t, w.Body.String(), `"charged":"1050"`)
	if integration {
		assert.Equal(t, 1, providerCalls)
		return
	}
	withUsage = false
	w = send("caller-two", "product-proof")
	require.Equal(t, 200, w.Code, w.Body.String())
	var unknown platformbilling.Call
	require.NoError(t, db.First(&unknown, "call_id = ?", w.Header().Get("X-UniHub-Call-ID")).Error)
	assert.Equal(t, "UNKNOWN", unknown.Status)
	assert.Equal(t, 409, send("caller-two", "product-proof").Code)
	assert.Equal(t, 2, providerCalls)
	assert.Equal(t, 2, reserves)
	assert.Equal(t, 1, settlements)
	var user model.User
	require.NoError(t, db.First(&user, 1).Error)
	assert.Equal(t, 777, user.Quota)
}
