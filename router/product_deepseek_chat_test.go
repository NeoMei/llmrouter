package router

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/internal/platformbilling"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/metering"
	perfmetrics "github.com/QuantumNous/new-api/pkg/perf_metrics"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/billing_setting"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestProductDeepSeekChatCertifiedSettlement(t *testing.T) {
	enableUniHubBoundary(t)
	require.NoError(t, i18n.Init())
	oldDB, oldLog, oldCache, oldRedis := model.DB, model.LOG_DB, common.MemoryCacheEnabled, common.RedisEnabled
	oldFX, oldTimeout, oldTLS := operation_setting.USDExchangeRate, constant.StreamingTimeout, common.TLSInsecureSkipVerify
	oldTariff := billing_setting.ProductCreditPricingJSONString()
	t.Cleanup(func() {
		perfmetrics.DrainRelaySamples()
		model.DB, model.LOG_DB, common.MemoryCacheEnabled, common.RedisEnabled = oldDB, oldLog, oldCache, oldRedis
		operation_setting.USDExchangeRate, constant.StreamingTimeout, common.TLSInsecureSkipVerify = oldFX, oldTimeout, oldTLS
		require.NoError(t, billing_setting.SetProductCreditPricing(oldTariff))
	})
	common.MemoryCacheEnabled, common.RedisEnabled = true, false
	operation_setting.USDExchangeRate, constant.StreamingTimeout, common.TLSInsecureSkipVerify = 7.3, 30, false

	const public = "deepseek-chat-product"
	const upstream = "deepseek-flash"
	const product = "agentwiki"
	const proof = "product-proof"
	const credential = "uh_key_test"
	const providerPath = "/v1/chat/completions"
	terms := []metering.Term{
		{Key: "input", Unit: "token", Price: metering.Rational{Numerator: "2", Denominator: "1000000"}},
		{Key: "cache_read", Unit: "token", Price: metering.Rational{Numerator: "4", Denominator: "100000000"}},
		{Key: "output", Unit: "token", Price: metering.Rational{Numerator: "8", Denominator: "1000000"}},
		{Key: "reasoning", Unit: "token", Price: metering.Rational{Numerator: "8", Denominator: "1000000"}},
	}
	bandRates := map[string]metering.Rational{}
	for _, term := range terms {
		bandRates[term.Key] = term.Price
	}
	raw, err := common.Marshal(map[string]any{
		"version": "deepseek-official-peak",
		"suppliers": map[string]any{public: map[string]any{"chat": map[string]any{"deepseek-cn": map[string]any{
			"version": "deepseek-flash-cn", "currency": "CNY",
			"tariff": map[string]any{"version": "metered_v2", "operation": "chat", "policy": "provider_reported_v1", "terms": terms,
				"input_token_bands": []map[string]any{{"max_input_tokens": 4096, "prices": bandRates}}},
		}}}},
	})
	require.NoError(t, err)
	require.NoError(t, billing_setting.SetProductCreditPricing(string(raw)))

	db, err := gorm.Open(sqlite.Open(t.TempDir()+"/product-deepseek.db"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Model{}, &model.Vendor{}, &model.Log{}, &model.User{}, &model.Token{}, &model.UserSubscription{}, &model.Channel{}, &model.Ability{}, &model.ClientModelRoute{}, &model.ClientModelTarget{}, &model.UpstreamAccountPool{}, &model.UpstreamAccountMember{}, &model.UpstreamAccountState{}))
	model.DB, model.LOG_DB = db, db
	require.NoError(t, db.Create(&model.User{Id: 1, Username: "routing", Status: common.UserStatusEnabled, Group: "default", Quota: 777}).Error)
	base := ""
	require.NoError(t, db.Create(&model.Channel{Id: 3, Type: constant.ChannelTypeDeepSeek, Key: "fixture-provider-key", Status: common.ChannelStatusEnabled, BaseURL: &base, Name: upstream, Models: upstream, Group: "default"}).Error)
	require.NoError(t, db.Create(&model.Ability{Group: "default", Model: upstream, ChannelId: 3, Enabled: true}).Error)
	require.NoError(t, db.Create(&model.ClientModelRoute{Id: 1, PublicId: public, Client: product, DisplayName: "DeepSeek", Enabled: true, Metadata: `{"operations":["chat.completions.create"]}`}).Error)
	require.NoError(t, db.Create(&model.ClientModelTarget{Id: 1, RouteId: 1, ChannelId: 3, UpstreamModel: upstream, BillingModel: upstream, PreferredWires: `["openai_chat"]`, Enabled: true, Weight: 1}).Error)
	model.InitChannelCache()
	service.InitHttpClient()
	service.InitTokenEncoders()

	var supplierRequests, reserves, settlements atomic.Int32
	var badUsage atomic.Bool
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		supplierRequests.Add(1)
		assert.Equal(t, providerPath, r.URL.Path)
		assert.Equal(t, "Bearer fixture-provider-key", r.Header.Get("Authorization"))
		assert.Empty(t, r.Header.Get(platformbilling.ProductAuthorizationHeader))
		var request struct {
			Model     string `json:"model"`
			Stream    bool   `json:"stream"`
			MaxTokens int    `json:"max_tokens"`
			Tools     []struct {
				Type string `json:"type"`
			} `json:"tools"`
			StreamOptions struct {
				IncludeUsage bool `json:"include_usage"`
			} `json:"stream_options"`
		}
		require.NoError(t, common.DecodeJson(r.Body, &request))
		assert.Equal(t, upstream, request.Model)
		assert.Equal(t, 20, request.MaxTokens)
		usage := `"usage":{"prompt_tokens":22,"completion_tokens":17,"total_tokens":39,"prompt_cache_hit_tokens":13,"prompt_cache_miss_tokens":9,"prompt_tokens_details":{"cached_tokens":13},"completion_tokens_details":{"reasoning_tokens":5}}`
		if badUsage.Load() {
			usage = strings.Replace(usage, `"cached_tokens":13`, `"cached_tokens":12`, 1)
		}
		if request.Stream {
			assert.True(t, request.StreamOptions.IncludeUsage)
			require.Len(t, request.Tools, 1)
			assert.Equal(t, "function", request.Tools[0].Type)
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = fmt.Fprintf(w, "data: %s\n\ndata: %s\n\ndata: [DONE]\n\n",
				`{"id":"answer","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","content":"fixture"},"finish_reason":null}]}`,
				`{"id":"answer","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":null},"finish_reason":"stop"}],`+usage+`}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"id":"answer","object":"chat.completion","model":"deepseek-flash","choices":[{"index":0,"message":{"role":"assistant","content":"fixture"},"finish_reason":"stop"}],`+usage+`}`)
	}))
	t.Cleanup(provider.Close)
	client := service.GetHttpClient()
	oldTransport := client.Transport
	client.Transport = deepSeekProductFixtureTransport{target: provider.URL, next: oldTransport}
	t.Cleanup(func() { client.Transport = oldTransport })

	conversion := platformbilling.NativeConversion{Version: "product-fixture-1", Numerator: "1", Denominator: "1", Rounding: "lr-cumulative-ceil-v1"}
	grant := platformbilling.Claims{CreditScope: platformbilling.ProductCreditScope, SourceVerified: true, FundingProductID: product,
		ID: "stable-key", CredentialKind: "api_key", InstanceID: "default", Audience: "llmrouter", ActorID: "actor", AccountID: "team", OriginProductID: product,
		ModelScope: "llmrouter_available", PricingSource: platformbilling.NativeCreditPricingSource, CredentialVersion: 2, Asset: "CREDIT/6", FundingMode: "credits", NativeCreditConversion: &conversion}
	remote := map[string]platformbilling.RemoteCall{}
	var remoteMu sync.Mutex
	hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		remoteMu.Lock()
		defer remoteMu.Unlock()
		var output any
		switch {
		case r.URL.Path == "/internal/v1/model-credentials/verify":
			var in map[string]string
			require.NoError(t, common.DecodeJson(r.Body, &in))
			assert.Equal(t, proof, in["product_authorization"])
			output = grant
		case r.URL.Path == "/internal/v1/calls/native-reserve":
			reserves.Add(1)
			var in platformbilling.NativeReserveRequest
			require.NoError(t, common.DecodeJson(r.Body, &in))
			assert.Equal(t, "3", in.Quote.ChannelID)
			assert.Equal(t, platformbilling.ProductCreditScope, in.CreditScope)
			assert.Equal(t, proof, in.ProductAuthorization)
			row := platformbilling.RemoteCall{CreditScope: in.CreditScope, CredentialVersion: grant.CredentialVersion, TaskID: in.TaskID,
				CallID: in.CallID, CredentialID: grant.ID, InstanceID: grant.InstanceID, ActorID: grant.ActorID, AccountID: grant.AccountID,
				OriginProductID: grant.OriginProductID, RequestDigest: in.RequestDigest, WireOperation: in.WireOperation, NativePricing: &in.Quote,
				Rate: platformbilling.Rate{Model: in.Model, Asset: "CREDIT/6", Semantics: "llmrouter_native"}, Held: in.Quote.MaxAmount,
				ChargeAsset: "CREDIT/6", FundingMode: "credits", FundingProductID: product, Status: "RESERVED"}
			remote[in.CallID], output = row, row
		case strings.HasSuffix(r.URL.Path, "/native-settle"):
			settlements.Add(1)
			id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/internal/v1/calls/"), "/native-settle")
			var in platformbilling.NativeSettleRequest
			require.NoError(t, common.DecodeJson(r.Body, &in))
			assert.Equal(t, "23178", in.Amount)
			require.NotNil(t, in.Usage)
			require.NotNil(t, in.Usage.Metered)
			assert.Equal(t, "deepseek_chat_product_v1", in.Usage.Metered.Source)
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

	env := map[string]string{
		"UNIHUB_PLATFORM_ENABLED": "true", "UNIHUB_ORIGIN": hub.URL,
		"UNIHUB_MODEL_SERVICE_TOKEN":  "service-service-service-service-token",
		"UNIHUB_ROUTING_PRINCIPAL_ID": "1", "UNIHUB_ROUTING_GROUP": "platform", "UNIHUB_NATIVE_ROUTING_GROUP": "default",
		"UNIHUB_MAX_BODY_BYTES": "8192", "UNIHUB_MAX_INPUT_TOKENS": "4096", "UNIHUB_MAX_OUTPUT_TOKENS": "100",
		"UNIHUB_PRODUCT_CREDIT_TEXT_INPUT_POLICIES": `{"chat.completions.create":{"deepseek-chat-product":{
			"version":"full_body_bytes_v1","channel_id":3,"channel_type":43,"upstream_model":"deepseek-flash",
			"endpoint":"https://api.deepseek.com/v1/chat/completions","max_input_tokens":4096,"context_tokens":4096,
			"max_output_tokens":100,"provider_reference":"https://api-docs.deepseek.com/api/create-chat-completion/"}}}`,
	}
	config, err := platformbilling.ConfigFromEnv(func(key string) string { return env[key] })
	require.NoError(t, err)
	adapter, err := platformbilling.New(db, config)
	require.NoError(t, err)
	discovery := platformbilling.Claims{OriginProductID: product, InstanceID: "fixture", ActorID: "actor", AccountID: "team"}
	projected, err := buildProviderCatalog(context.Background(), adapter, discovery, true)
	require.NoError(t, err)
	require.NotEmpty(t, projected.Models)
	item := projected.Models[0]
	require.Equal(t, public, item.ID)
	require.Len(t, item.CapabilityMetadata.Operations, 1)
	require.True(t, item.CapabilityMetadata.Operations[0].Executable)
	require.NotNil(t, item.CapabilityMetadata.Operations[0].PublicTariff)
	assert.EqualValues(t, 4096, item.Context)
	assert.EqualValues(t, 100, item.MaxOutput)

	require.NoError(t, db.Model(&model.ClientModelRoute{}).Where("id = ?", 1).Update("metadata",
		`{"operations":["chat.completions.create"],"context_window":2048,"max_output_tokens":500}`).Error)
	capped, err := buildProviderCatalog(context.Background(), adapter, discovery, true)
	require.NoError(t, err)
	require.NotEmpty(t, capped.Models)
	require.Equal(t, public, capped.Models[0].ID)
	assert.EqualValues(t, 2048, capped.Models[0].Context)
	assert.EqualValues(t, 100, capped.Models[0].MaxOutput)
	require.NoError(t, db.Model(&model.ClientModelRoute{}).Where("id = ?", 1).Update("metadata",
		`{"operations":["chat.completions.create"]}`).Error)

	r := gin.New()
	SetManagedKeyDispatch(r, adapter)
	SetRelayRouter(r)
	send := func(id, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/platform/v1/chat/completions", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+credential)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", id)
		req.Header.Set(platformbilling.ProductAuthorizationHeader, proof)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}
	const request = `{"model":"deepseek-chat-product","messages":[{"role":"user","content":"hello"}],"max_tokens":20}`
	streamRequest := strings.Replace(request, `"max_tokens":20`, `"stream":true,"max_completion_tokens":20,"tools":[{"type":"function","function":{"name":"read","parameters":{"type":"object"}}}]`, 1)
	for i, body := range []string{request, streamRequest} {
		w := send(fmt.Sprintf("deepseek-%d", i), body)
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		var settled platformbilling.Call
		require.NoError(t, db.First(&settled, "call_id = ?", w.Header().Get("X-UniHub-Call-ID")).Error)
		assert.Equal(t, "SETTLED", settled.Status)
		assert.Equal(t, "23178", settled.Charged)
	}
	assert.Equal(t, http.StatusConflict, send("deepseek-0", request).Code)
	badUsage.Store(true)
	w := send("invalid-meter", request)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var pending platformbilling.Call
	require.NoError(t, db.First(&pending, "call_id = ?", w.Header().Get("X-UniHub-Call-ID")).Error)
	assert.Equal(t, "UNKNOWN", pending.Status)

	vision := strings.Replace(request, `"content":"hello"`, `"content":[{"type":"image_url","image_url":{"url":"https://example.com/image.png"}}]`, 1)
	assert.Equal(t, http.StatusBadRequest, send("vision", vision).Code)
	hosted := strings.Replace(request, `"max_tokens":20`, `"max_tokens":20,"tools":[{"type":"web_search"}]`, 1)
	assert.Equal(t, http.StatusBadRequest, send("hosted-tools", hosted).Code)
	require.NoError(t, db.Model(&model.Channel{}).Where("id = ?", 3).Update("base_url", "https://different.example").Error)
	model.InitChannelCache()
	assert.Equal(t, http.StatusBadRequest, send("different-route", request).Code)
	assert.EqualValues(t, 3, supplierRequests.Load())
	assert.EqualValues(t, 3, reserves.Load())
	assert.EqualValues(t, 2, settlements.Load())
}

type deepSeekProductFixtureTransport struct {
	target string
	next   http.RoundTripper
}

func (f deepSeekProductFixtureTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.String() != "https://api.deepseek.com/v1/chat/completions" {
		return nil, fmt.Errorf("unexpected fixture endpoint %s", r.URL.String())
	}
	u, err := url.Parse(f.target)
	if err != nil {
		return nil, err
	}
	copy := r.Clone(r.Context())
	u.Path = r.URL.Path
	copy.URL, copy.Host = u, u.Host
	return f.next.RoundTrip(copy)
}
