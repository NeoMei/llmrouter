package router

import (
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/internal/platformbilling"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/metering"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/billing_setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestManagedNativeImageValidatesRequestBeforeAdmission(t *testing.T) {
	r := newManagedNativeRouter(nil)
	req := httptest.NewRequest("POST", "/platform/v1/native/dashscope/images/generations", strings.NewReader(`{"model":"qwen-image-2.0","input":{}}`))
	req.Header.Set("Content-Type", "application/json")
	out := httptest.NewRecorder()
	r.ServeHTTP(out, req)
	require.Equal(t, 400, out.Code, out.Body.String())
}

func TestManagedNativeImageUsesSameKeyNativePriceAndProviderProtocol(t *testing.T) {
	testManagedNativeImageProtocol(t, false)
}
func TestManagedNativeImageOpenAIUsesNativeOnlyPrice(t *testing.T) {
	testManagedNativeImageProtocol(t, true)
}
func testManagedNativeImageProtocol(t *testing.T, compatible bool) {
	enableUniHubBoundary(t)
	_, db := providerCatalogFixture(t)
	require.NoError(t, i18n.Init())
	previousCache, previousRedis, previousRetry, previousGlobal := common.MemoryCacheEnabled, common.RedisEnabled, common.RetryTimes, common.GlobalApiRateLimitEnable
	previousNativePrices := billing_setting.GetModelNativePricingCopy()
	t.Cleanup(func() {
		common.MemoryCacheEnabled, common.RedisEnabled, common.RetryTimes, common.GlobalApiRateLimitEnable = previousCache, previousRedis, previousRetry, previousGlobal
		require.NoError(t, billing_setting.SetModelNativePricing(previousNativePrices))
	})
	common.MemoryCacheEnabled, common.RedisEnabled, common.RetryTimes, common.GlobalApiRateLimitEnable = true, false, 0, false
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.Token{}, &model.UserSubscription{}, &model.UpstreamAccountPool{}, &model.UpstreamAccountMember{}, &model.UpstreamAccountState{}))
	require.NoError(t, db.Create(&model.User{Id: 1, Username: "routing", Status: common.UserStatusEnabled, Group: "native"}).Error)
	service.InitHttpClient()
	service.InitTokenEncoders()
	previousClient := imageProducerHTTPClient
	previousQuota := common.QuotaPerUnit
	previousGroups := ratio_setting.GroupRatio2JSONString()
	t.Cleanup(func() {
		imageProducerHTTPClient = previousClient
		common.QuotaPerUnit = previousQuota
		require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(previousGroups))
	})
	common.QuotaPerUnit = 1000000
	require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(`{"native":1}`))
	require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(`{"qwen-image-2.0":0.01}`))
	expectedWire := "dashscope.images.generate"
	if compatible {
		expectedWire = "images.generations.create"
		require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(`{}`))
		nativePrice := metering.Price{Version: "metered_v2", Operation: "image_generation", Policy: "successful_outputs_v1", Terms: []metering.Term{{Key: "images", Unit: "image", Price: metering.Rational{Numerator: "1", Denominator: "100"}}}}
		require.NoError(t, billing_setting.SetModelNativePricing(map[string]map[string]metering.Price{"qwen-image-2.0": {"image_generation": nativePrice}}))
	}
	calls, settles, reserves := 0, 0, 0
	mode := "success"
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		require.Equal(t, "/api/v1/services/aigc/multimodal-generation/generation", r.URL.Path)
		require.Equal(t, "Bearer supplier-image-secret", r.Header.Get("Authorization"))
		require.Empty(t, r.Header.Get("X-UniHub-Task-ID"))
		require.Empty(t, r.Header.Get("X-Account-ID"))
		var in managedQwenImageRequest
		require.NoError(t, common.DecodeJson(r.Body, &in))
		require.Equal(t, "qwen-image-2.0", in.Model)
		require.Equal(t, "a tiny green square", in.Input.Messages[0].Content[0].Text)
		require.NotNil(t, in.Parameters.Watermark)
		require.False(t, *in.Parameters.Watermark)
		require.NotNil(t, in.Parameters.Seed)
		require.Zero(t, *in.Parameters.Seed)
		if mode == "invalid" {
			_, _ = w.Write([]byte(`{"output":{"choices":[]},"usage":{"image_count":1,"width":1024,"height":1024}}`))
			return
		}
		if mode == "missing-dimensions" {
			_, _ = w.Write([]byte(`{"output":{"choices":[{"finish_reason":"stop","message":{"role":"assistant","content":[{"image":"https://images.example.test/square.png"}]}}]},"usage":{"image_count":1,"height":1024}}`))
			return
		}
		_, _ = w.Write([]byte(`{"request_id":"image-request","output":{"choices":[{"finish_reason":"stop","message":{"role":"assistant","content":[{"image":"https://images.example.test/square.png"}]}}]},"usage":{"image_count":1,"width":1024,"height":1024},"vendor_diagnostic":"must-not-leak"}`))
	}))
	t.Cleanup(provider.Close)
	imageProducerHTTPClient = func() *http.Client { return provider.Client() }
	base := provider.URL
	require.NoError(t, db.Create(&model.Channel{Id: 3, Type: constant.ChannelTypeAli, Key: "supplier-image-secret", Status: common.ChannelStatusEnabled, BaseURL: &base, Group: "native", Models: "qwen-image-2.0"}).Error)
	require.NoError(t, db.Create(&model.Ability{Group: "native", Model: "qwen-image-2.0", ChannelId: 3, Enabled: true}).Error)
	routeMetadata := `{"operations":["dashscope.images.generate","images.generations.create"],"input_modalities":["text","image"],"output_modalities":["image"],"media_limits":{"max_images":4,"max_pixels":16777216,"max_request_bytes":31457280}}`
	require.NoError(t, db.Create(&model.ClientModelRoute{Id: 1, PublicId: "qwen-image-2.0", DisplayName: "Image model", Client: "all", Enabled: true, Metadata: routeMetadata}).Error)
	require.NoError(t, db.Create(&model.ClientModelTarget{Id: 1, RouteId: 1, ChannelId: 3, UpstreamModel: "qwen-image-2.0", BillingModel: "qwen-image-2.0", PreferredWires: `[]`, Enabled: true, Weight: 1}).Error)
	model.InitChannelCache()
	var remote platformbilling.RemoteCall
	hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var output any
		switch {
		case r.URL.Path == "/internal/v1/model-credentials/verify":
			output = platformbilling.Claims{ID: "original-key", CredentialKind: "api_key", InstanceID: "fixture", Audience: "llmrouter", ActorID: "actor", AccountID: "team", OriginProductID: "superwagie", ModelScope: "llmrouter_available", PricingSource: "llmrouter_native", CredentialVersion: 2, Asset: "CNY/6", FundingMode: "prepaid_cash"}
		case r.URL.Path == "/internal/v1/calls/native-reserve":
			reserves++
			if mode == "budget" {
				w.WriteHeader(402)
				_, _ = w.Write([]byte(`{"error":"insufficient_funds"}`))
				return
			}
			var in platformbilling.NativeReserveRequest
			require.NoError(t, common.DecodeJson(r.Body, &in))
			require.Equal(t, "uh_key_same", in.Credential)
			require.Equal(t, expectedWire, in.WireOperation)
			require.Equal(t, "10000", in.Quote.NativeQuotaMax)
			remote = platformbilling.RemoteCall{CredentialVersion: 2, TaskID: in.TaskID, CallID: in.CallID, CredentialID: "original-key", InstanceID: "fixture", ActorID: "actor", AccountID: "team", OriginProductID: "superwagie", RequestDigest: in.RequestDigest, WireOperation: in.WireOperation, NativePricing: &in.Quote, Rate: platformbilling.Rate{Model: in.Model, Asset: "CNY/6", Semantics: "llmrouter_native"}, Held: in.Quote.MaxAmount, ChargeAsset: "CNY/6", FundingMode: "prepaid_cash", FundingProductID: "llmrouter", Status: "RESERVED"}
			output = remote
		case strings.HasSuffix(r.URL.Path, "/native-settle"):
			settles++
			var in platformbilling.NativeSettleRequest
			require.NoError(t, common.DecodeJson(r.Body, &in))
			require.Equal(t, "10000", in.NativeQuota)
			require.NotNil(t, in.Usage)
			require.NotNil(t, in.Usage.Metered)
			require.Equal(t, "confirmed", in.Usage.Metered.State)
			remote.Status = "SETTLED"
			remote.Charged = in.Amount
			output = remote
		default:
			t.Errorf("unexpected funding endpoint %s", r.URL.Path)
			w.WriteHeader(500)
			return
		}
		raw, err := common.Marshal(output)
		require.NoError(t, err)
		_, _ = w.Write(raw)
	}))
	t.Cleanup(hub.Close)
	adapter, err := platformbilling.New(db, platformbilling.Config{Enabled: true, Origin: hub.URL, ServiceToken: strings.Repeat("s", 32), InstanceID: "fixture", PrincipalID: 1, Group: "legacy", NativeGroup: "native", MaxBodyBytes: 8192, MaxInputTokens: 1000, MaxOutputTokens: 100})
	require.NoError(t, err)
	engine := gin.New()
	SetManagedKeyDispatch(engine, adapter)
	SetRelayRouter(engine)
	discovery := httptest.NewRequest("GET", "/v1/models", nil)
	discovery.Header.Set("Authorization", "Bearer uh_key_same")
	discoveryOut := httptest.NewRecorder()
	engine.ServeHTTP(discoveryOut, discovery)
	require.Equal(t, 200, discoveryOut.Code, discoveryOut.Body.String())
	require.Contains(t, discoveryOut.Body.String(), `"id":"qwen-image-2.0"`)
	chat := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"qwen-image-2.0","messages":[{"role":"user","content":"hello"}],"max_tokens":1}`))
	chat.Header.Set("Authorization", "Bearer uh_key_same")
	chat.Header.Set("Content-Type", "application/json")
	chat.Header.Set("Idempotency-Key", "image-as-chat")
	chatOut := httptest.NewRecorder()
	engine.ServeHTTP(chatOut, chat)
	require.Equal(t, 400, chatOut.Code, chatOut.Body.String())
	require.Zero(t, reserves, "image-only model must reject chat before any funding reservation")
	require.Zero(t, calls, "image-only model must reject chat before supplier dispatch")
	if compatible {
		for _, invalid := range []struct {
			name, body, reason string
		}{
			{"declared image count", `{"model":"qwen-image-2.0","prompt":"circle","n":5,"size":"1024x1024"}`, "exceeds declared max_images"},
			{"unknown dimensions", `{"model":"qwen-image-2.0","prompt":"circle","n":1}`, "require verified media metadata"},
			{"oversized dimensions", `{"model":"qwen-image-2.0","prompt":"circle","n":1,"size":"8192x8192"}`, "image size requires"},
		} {
			req := httptest.NewRequest("POST", "/v1/images/generations", strings.NewReader(invalid.body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Bearer uh_key_same")
			req.Header.Set("Idempotency-Key", "image-invalid-"+strings.ReplaceAll(invalid.name, " ", "-"))
			out := httptest.NewRecorder()
			engine.ServeHTTP(out, req)
			require.Equal(t, 400, out.Code, out.Body.String())
			require.Contains(t, out.Body.String(), invalid.reason)
			require.Zero(t, reserves, "image bounds must reject before funding")
			require.Zero(t, calls, "image bounds must reject before supplier dispatch")
		}
	}
	if !compatible {
		mode = "budget"
		for _, invalid := range []struct {
			name, limits, count, reason string
		}{
			{"count", `{"max_images":4,"max_pixels":16777216}`, "5", "exceeds declared max_images"},
			{"pixels", `{"max_images":4,"max_pixels":262144}`, "1", "exceeds declared max_pixels"},
			{"bytes", `{"max_request_bytes":16}`, "1", "exceeds declared max_request_bytes"},
			{"format", `{"formats":["png"]}`, "1", "format requires verified media metadata"},
		} {
			metadata := `{"operations":["dashscope.images.generate","images.generations.create"],"input_modalities":["text"],"output_modalities":["image"],"media_limits":` + invalid.limits + `}`
			require.NoError(t, db.Model(&model.ClientModelRoute{}).Where("id = ?", 1).Update("metadata", metadata).Error)
			body := `{"model":"qwen-image-2.0","input":{"messages":[{"role":"user","content":[{"text":"a tiny green square"}]}]},"parameters":{"n":` + invalid.count + `,"size":"1024*1024","seed":0,"watermark":false}}`
			req := httptest.NewRequest("POST", "/v1/native/dashscope/images/generations", strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Bearer uh_key_same")
			req.Header.Set("Idempotency-Key", "native-image-invalid-"+invalid.name)
			out := httptest.NewRecorder()
			engine.ServeHTTP(out, req)
			require.Equal(t, 400, out.Code, out.Body.String())
			require.Contains(t, out.Body.String(), invalid.reason)
			require.Zero(t, reserves, "native route limits must reject before funding")
			require.Zero(t, calls, "native route limits must reject before supplier dispatch")
		}
		require.NoError(t, db.Model(&model.ClientModelRoute{}).Where("id = ?", 1).Update("metadata", routeMetadata).Error)
		mode = "success"
	}
	requestSize := "1024x1024"
	send := func(call string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/v1/native/dashscope/images/generations", strings.NewReader(strings.ReplaceAll(`{"model":"qwen-image-2.0","input":{"messages":[{"role":"user","content":[{"text":"a tiny green square"}]}]},"parameters":{"n":1,"size":"1024*1024","seed":0,"watermark":false}}`, "1024*1024", strings.ReplaceAll(requestSize, "x", "*"))))
		if compatible {
			req = httptest.NewRequest("POST", "/v1/images/generations", strings.NewReader(strings.ReplaceAll(`{"model":"qwen-image-2.0","prompt":"a tiny green square","n":1,"size":"1024x1024","seed":0,"watermark":false}`, "1024x1024", requestSize)))
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer uh_key_same")
		req.Header.Set("X-UniHub-Task-ID", "task")
		req.Header.Set("Idempotency-Key", call)
		out := httptest.NewRecorder()
		engine.ServeHTTP(out, req)
		return out
	}
	response := send("native-image-success")
	require.Equal(t, 200, response.Code, response.Body.String())
	require.Equal(t, 1, calls)
	require.Equal(t, 1, settles)
	require.Contains(t, response.Body.String(), "https://images.example.test/square.png")
	require.NotContains(t, response.Body.String(), "must-not-leak")
	if compatible {
		require.Contains(t, response.Body.String(), `"data":[`)
		require.Contains(t, response.Body.String(), `"url":"https://images.example.test/square.png"`)
	}
	mode = "budget"
	response = send("native-image-budget")
	require.Equal(t, 402, response.Code, response.Body.String())
	require.Equal(t, 1, calls)
	mode = "invalid"
	response = send("native-image-invalid")
	require.Equal(t, 502, response.Code, response.Body.String())
	require.Equal(t, 2, calls)
	require.Equal(t, 1, settles)
	var stored platformbilling.Call
	require.NoError(t, db.First(&stored, "call_id = ?", response.Header().Get("X-UniHub-Call-ID")).Error)
	require.Equal(t, "UNKNOWN", stored.Status)
	response = send("native-image-invalid")
	require.NotEqual(t, 200, response.Code)
	require.Equal(t, 2, calls)
	mode = "missing-dimensions"
	response = send("native-image-missing-dimensions")
	require.Equal(t, 502, response.Code, response.Body.String())
	require.NotContains(t, response.Body.String(), "https://images.example.test/square.png")
	require.Equal(t, 3, calls)
	require.Equal(t, 1, settles)
	stored = platformbilling.Call{}
	require.NoError(t, db.First(&stored, "call_id = ?", response.Header().Get("X-UniHub-Call-ID")).Error)
	require.Equal(t, "UNKNOWN", stored.Status)
	response = send("native-image-missing-dimensions")
	require.NotEqual(t, 200, response.Code)
	require.Equal(t, 3, calls, "uncertain image results must never replay a supplier request")
	require.Equal(t, 1, settles)
	mode, requestSize = "success", "512x512"
	require.NoError(t, db.Model(&model.ClientModelRoute{}).Where("id = ?", 1).Update("metadata", strings.ReplaceAll(routeMetadata, "16777216", "262144")).Error)
	response = send("native-image-response-outside-route")
	require.Equal(t, 502, response.Code, response.Body.String())
	require.NotContains(t, response.Body.String(), "https://images.example.test/square.png")
	require.Equal(t, 4, calls)
	require.Equal(t, 1, settles)
	stored = platformbilling.Call{}
	require.NoError(t, db.First(&stored, "call_id = ?", response.Header().Get("X-UniHub-Call-ID")).Error)
	require.Equal(t, "UNKNOWN", stored.Status)
	response = send("native-image-response-outside-route")
	require.NotEqual(t, 200, response.Code)
	require.Equal(t, 4, calls)
	require.Equal(t, 1, settles)
}
