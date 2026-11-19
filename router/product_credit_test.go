package router

import (
	"bytes"
	"crypto/x509"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/internal/platformbilling"
	"github.com/QuantumNous/new-api/model"
	perfmetrics "github.com/QuantumNous/new-api/pkg/perf_metrics"
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
	testProductCreditRelayFreezesPublicPrice(t, false)
}

func TestBandedProductCreditRelayFreezesHighestReachableHoldAndSettlesActualBand(t *testing.T) {
	testProductCreditRelayFreezesPublicPrice(t, true)
}

func testProductCreditRelayFreezesPublicPrice(t *testing.T, banded bool) {
	origin := os.Getenv("PRODUCT_CREDIT_INTEGRATION_ORIGIN")
	integration := origin != "" && !banded
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
	oldTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() { constant.StreamingTimeout = oldTimeout })
	oldDB, oldLogDB, oldCache, oldRedis, oldRetry := model.DB, model.LOG_DB, common.MemoryCacheEnabled, common.RedisEnabled, common.RetryTimes
	oldGlobal := common.GlobalApiRateLimitEnable
	oldFX, oldQuota := operation_setting.USDExchangeRate, common.QuotaPerUnit
	oldGroup, oldTariff := ratio_setting.GroupRatio2JSONString(), billing_setting.ProductCreditPricingJSONString()
	t.Cleanup(func() {
		perfmetrics.DrainRelaySamples()
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
	if banded {
		require.NoError(t, billing_setting.SetProductCreditPricing(`{
		 "version":"banded-rev1","suppliers":{"public-credit-model":{"chat":{"a":{"version":"benchmark-1","currency":"CNY","tariff":{
		   "version":"metered_v2","operation":"chat","policy":"provider_reported_v1","terms":[
		     {"key":"input","unit":"token","price":{"numerator":"1","denominator":"1000000"}},
		     {"key":"output","unit":"token","price":{"numerator":"2","denominator":"1000000"}}],"input_token_bands":[
		     {"max_input_tokens":9,"prices":{"input":{"numerator":"1","denominator":"1000000"},"output":{"numerator":"2","denominator":"1000000"}}},
		     {"max_input_tokens":1000,"prices":{"input":{"numerator":"4","denominator":"1000000"},"output":{"numerator":"6","denominator":"1000000"}}}]
		 }}}}}}`))
	}
	db, err := gorm.Open(sqlite.Open(t.TempDir()+"/product-relay.db"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Model{}, &model.Vendor{}, &model.Log{}, &model.User{}, &model.Token{}, &model.UserSubscription{}, &model.Channel{}, &model.Ability{}, &model.ClientModelRoute{}, &model.ClientModelTarget{}, &model.UpstreamAccountPool{}, &model.UpstreamAccountMember{}, &model.UpstreamAccountState{}))
	model.DB, model.LOG_DB = db, db
	require.NoError(t, db.Create(&model.User{Id: 1, Username: "routing", Status: common.UserStatusEnabled, Group: "default", Quota: 777}).Error)
	var providerCalls atomic.Int32
	var providerBodyBytes atomic.Int64
	reserves, settlements := 0, 0
	withUsage := true
	expireBeforeHold := false
	membershipEndedAfterHold := false
	providerHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		providerCalls.Add(1)
		providerBodyBytes.Store(r.ContentLength)
		assert.Empty(t, r.Header.Get(platformbilling.ProductAuthorizationHeader))
		assert.Empty(t, r.Header.Get("Idempotency-Key"))
		var in struct {
			Model  string `json:"model"`
			Stream bool   `json:"stream"`
		}
		require.NoError(t, common.DecodeJson(r.Body, &in))
		assert.Contains(t, []string{"upstream-one", "upstream-two"}, in.Model)
		if in.Stream {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = fmt.Fprint(w,
				"data: "+`{"id":"answer","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","content":"fixture product answer"},"finish_reason":null}]}`+"\n\n"+
					"data: "+`{"id":"answer","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`+"\n\n"+
					"data: "+`{"id":"answer","object":"chat.completion.chunk","choices":[],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`+"\n\ndata: [DONE]\n\n")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		raw := fmt.Sprintf(`{"id":"answer","object":"chat.completion","model":%q,"choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}]`, in.Model)
		if withUsage {
			raw += `,"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}`
		}
		_, _ = w.Write([]byte(raw + "}"))
	})
	startProvider := httptest.NewServer
	if banded {
		startProvider = httptest.NewTLSServer
	}
	provider := startProvider(providerHandler)
	t.Cleanup(provider.Close)
	require.NoError(t, db.Create(&model.ClientModelRoute{Id: 1, PublicId: "public-credit-model", Client: product, DisplayName: "Public", Enabled: true, Metadata: `{"operations":["chat.completions.create"]}`, Aliases: `["product-alias"]`}).Error)
	for i, upstream := range []string{"upstream-one", "upstream-two"} {
		if banded && i > 0 {
			break
		}
		id, base := i+1, provider.URL
		require.NoError(t, db.Create(&model.Channel{Id: id, Type: constant.ChannelTypeOpenAI, Key: "fixture-provider-key", Status: common.ChannelStatusEnabled, BaseURL: &base, Name: upstream, Models: upstream, Group: "default"}).Error)
		require.NoError(t, db.Create(&model.Ability{Group: "default", Model: upstream, ChannelId: id, Enabled: true}).Error)
		require.NoError(t, db.Create(&model.ClientModelTarget{Id: id, RouteId: 1, ChannelId: id, UpstreamModel: upstream, BillingModel: upstream, PreferredWires: `["openai_chat"]`, Enabled: true, Weight: 1 + 2*i}).Error)
	}
	model.InitChannelCache()
	service.InitHttpClient()
	if banded {
		pool := x509.NewCertPool()
		pool.AddCert(provider.Certificate())
		service.GetHttpClient().Transport.(*http.Transport).TLSClientConfig.RootCAs = pool
	}
	service.InitTokenEncoders()
	conversion := platformbilling.NativeConversion{Version: "product-fixture-1", Numerator: "1", Denominator: "1", Rounding: "lr-cumulative-ceil-v1"}
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
			if membershipEndedAfterHold && in["purpose"] != "receipt" {
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte(`{"error":"product_membership_expired"}`))
				return
			}
			checked := grant
			if in["purpose"] == "receipt" {
				checked.NativeCreditConversion = nil
			}
			output = checked
		case r.URL.Path == "/internal/v1/calls/native-reserve":
			reserves++
			if expireBeforeHold {
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte(`{"error":"product_membership_expired_no_hold"}`))
				return
			}
			var in platformbilling.NativeReserveRequest
			require.NoError(t, common.DecodeJson(r.Body, &in))
			assert.Equal(t, platformbilling.ProductCreditScope, in.CreditScope)
			assert.Equal(t, "product-proof", in.ProductAuthorization)
			assert.Empty(t, in.MinimumCreditAmount)
			row := platformbilling.RemoteCall{CreditScope: in.CreditScope, CredentialVersion: grant.CredentialVersion, TaskID: in.TaskID, CallID: in.CallID, CredentialID: grant.ID, InstanceID: grant.InstanceID,
				ActorID: grant.ActorID, AccountID: grant.AccountID, OriginProductID: grant.OriginProductID, RequestDigest: in.RequestDigest, WireOperation: in.WireOperation, NativePricing: &in.Quote,
				Rate: platformbilling.Rate{Model: in.Model, Asset: "CREDIT/6", Semantics: "llmrouter_native"}, Held: in.Quote.MaxAmount, ChargeAsset: "CREDIT/6", FundingMode: "credits", FundingProductID: "agentwiki", Status: "RESERVED"}
			remote[in.CallID], output = row, row
			if reserves == 1 {
				membershipEndedAfterHold = true
				require.NoError(t, billing_setting.SetProductCreditPricing(`{
				   "version":"rev2",
				   "suppliers":{"public-credit-model":{"chat":{
				     "a":{"version":"a-2","currency":"CNY","tariff":{"version":"metered_v2","operation":"chat","policy":"provider_reported_v1","terms":[
				       {"key":"input","unit":"token","price":{"numerator":"1","denominator":"1000000"}},
				       {"key":"output","unit":"token","price":{"numerator":"2","denominator":"1000000"}}]}}
				   }}}
				}`))
			}
		case strings.HasSuffix(r.URL.Path, "/native-settle"):
			settlements++
			id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/internal/v1/calls/"), "/native-settle")
			var in platformbilling.NativeSettleRequest
			require.NoError(t, common.DecodeJson(r.Body, &in))
			want := "10500"
			if settlements == 2 {
				want = "3000"
			}
			assert.Equal(t, want, in.NativeQuota)
			assert.Equal(t, want, in.Amount)
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
	config := platformbilling.Config{Enabled: true, Origin: origin, ServiceToken: serviceToken, InstanceID: instance,
		PrincipalID: 1, Group: "platform", NativeGroup: "default", MaxBodyBytes: 8192, MaxInputTokens: 1000, MaxOutputTokens: 100}
	if banded {
		config.ProductCreditTextInputPolicies = map[string]map[string]platformbilling.TextInputPolicy{"chat.completions.create": {"public-credit-model": {
			Version: "full_body_bytes_v1", ChannelID: 1, ChannelType: constant.ChannelTypeOpenAI, UpstreamModel: "upstream-one",
			Endpoint: provider.URL + "/v1/chat/completions", ContextTokens: 1100, MaxInputTokens: 1000, MaxOutputTokens: 100,
			ProviderReference: "https://example.invalid/text-token-contract",
		}}}
	}
	adapter, err := platformbilling.New(db, config)
	require.NoError(t, err)
	r := gin.New()
	SetManagedKeyDispatch(r, adapter)
	SetRelayRouter(r)
	send := func(key, proof string, stream bool) *httptest.ResponseRecorder {
		body := `{"model":"product-alias","messages":[{"role":"user","content":"hello"}]}`
		if stream {
			body = `{"model":"product-alias","messages":[{"role":"user","content":"hello"}],"stream":true}`
		}
		req := httptest.NewRequest(http.MethodPost, "/platform/v1/chat/completions", bytes.NewBufferString(body))
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
	w := send(callerKey, proof, false)
	require.Equal(t, 200, w.Code, w.Body.String())
	membershipEndedAfterHold = false
	id := w.Header().Get("X-UniHub-Call-ID")
	require.True(t, strings.HasPrefix(id, "pc_"))
	var row platformbilling.Call
	require.NoError(t, db.First(&row, "call_id = ?", id).Error)
	assert.Equal(t, "SETTLED", row.Status)
	assert.Equal(t, "10500", row.Charged)
	if banded {
		inputCap := providerBodyBytes.Load() * 2
		assert.Positive(t, inputCap)
		assert.Equal(t, inputCap, row.ReservationQuantity)
		assert.EqualValues(t, 100, row.ReservationOutput)
		assert.Equal(t, strconv.FormatInt(inputCap*600+100*900, 10), row.Amount)
		assert.Contains(t, row.NativePriceSnapshot, "banded-rev1")
		assert.Contains(t, row.TextInputSnapshot, "full_body_bytes_v1")
	}
	assert.Contains(t, row.NativePriceSnapshot, "public-credit-model")
	assert.Contains(t, row.Evidence, `"state":"confirmed"`)
	assert.False(t, strings.Contains(row.NativePriceSnapshot, proof), "pricing evidence must never persist proof")
	w = send(callerKey, proof, false)
	assert.Equal(t, 409, w.Code, w.Body.String())
	get := httptest.NewRequest(http.MethodGet, "/platform/v1/calls/"+callerKey, nil)
	get.Header.Set("Authorization", "Bearer "+credential)
	get.Header.Set(platformbilling.ProductAuthorizationHeader, proof)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, get)
	require.Equal(t, 200, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), `"idempotency_key":"`+callerKey+`"`)
	assert.Contains(t, w.Body.String(), `"funding_product_id":"`+product+`"`)
	assert.Contains(t, w.Body.String(), `"charged":"10500"`)
	if integration {
		require.EqualValues(t, 1, providerCalls.Load())
		if app := os.Getenv("PRODUCT_CREDIT_INTEGRATION_APP_PATH"); app != "" {
			require.True(t, filepath.IsAbs(app), "the SuperWagie fixture must use an absolute checkout")
			httpGateway := httptest.NewServer(r)
			t.Cleanup(httpGateway.Close)
			run := exec.CommandContext(t.Context(), "bun", "run", "--cwd", "packages/web", "test", "server/lib/platform-account/product-model-cross-repository.test.js")
			run.Dir = app
			run.Env = append(os.Environ(), "PRODUCT_CREDIT_INTEGRATION_GATEWAY="+httpGateway.URL)
			output, err := run.CombinedOutput()
			require.NoError(t, err, "SuperWagie product proxy fixture:\n%s", output)
			t.Logf("SuperWagie product proxy fixture:\n%s", output)
			require.EqualValues(t, 2, providerCalls.Load(), "a replay must not dispatch a second provider request")
		}
		return
	}
	w = send("caller-stream", "product-proof", true)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), "fixture product answer")
	var streamed platformbilling.Call
	require.NoError(t, db.First(&streamed, "call_id = ?", w.Header().Get("X-UniHub-Call-ID")).Error)
	assert.Equal(t, "SETTLED", streamed.Status)
	assert.Equal(t, "3000", streamed.Charged)
	assert.Contains(t, streamed.NativePriceSnapshot, "rev2")
	assert.NotEqual(t, row.NativePriceSnapshot, streamed.NativePriceSnapshot)

	withUsage = false
	w = send("caller-two", "product-proof", false)
	require.Equal(t, 200, w.Code, w.Body.String())
	var unknown platformbilling.Call
	require.NoError(t, db.First(&unknown, "call_id = ?", w.Header().Get("X-UniHub-Call-ID")).Error)
	assert.Equal(t, "UNKNOWN", unknown.Status)
	assert.Equal(t, 409, send("caller-two", "product-proof", false).Code)
	assert.EqualValues(t, 3, providerCalls.Load())
	assert.Equal(t, 3, reserves)
	assert.Equal(t, 2, settlements)
	expireBeforeHold = true
	w = send("caller-expired", "product-proof", false)
	require.Equal(t, http.StatusForbidden, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), "product_membership_expired")
	ctx, err := platformbilling.WithProductAuthorization(t.Context(), "product-proof", "caller-expired")
	require.NoError(t, err)
	callID, err := grant.ProductCreditCallID(ctx)
	require.NoError(t, err)
	var expired platformbilling.Call
	require.NoError(t, db.First(&expired, "call_id = ?", callID).Error)
	assert.Equal(t, "REJECTED", expired.Status)
	assert.EqualValues(t, 3, providerCalls.Load())
	var user model.User
	require.NoError(t, db.First(&user, 1).Error)
	assert.Equal(t, 777, user.Quota)
}
