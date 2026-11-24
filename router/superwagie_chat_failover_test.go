package router

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
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
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestSuperWagieProductChatSafeFailover(t *testing.T) {
	const marker = "X-LLMRouter-Error-Origin"
	const refusal = `{"error":{"code":"platform_model_route_unavailable","stage":"pre_admission"}}`
	enableUniHubBoundary(t)
	require.NoError(t, i18n.Init())
	oldDB, oldLogDB := model.DB, model.LOG_DB
	oldMainType, oldLogType := common.MainDatabaseType(), common.LogDatabaseType()
	oldCache, oldRedis, oldRateLimit := common.MemoryCacheEnabled, common.RedisEnabled, common.GlobalApiRateLimitEnable
	oldStreamingTimeout := constant.StreamingTimeout
	oldFX, oldQuota := operation_setting.USDExchangeRate, common.QuotaPerUnit
	oldRatio, oldTariff := ratio_setting.GroupRatio2JSONString(), billing_setting.ProductCreditPricingJSONString()
	t.Cleanup(func() {
		model.DB, model.LOG_DB = oldDB, oldLogDB
		common.SetDatabaseTypes(oldMainType, oldLogType)
		model.InitDatabaseColumns()
		common.MemoryCacheEnabled, common.RedisEnabled, common.GlobalApiRateLimitEnable = oldCache, oldRedis, oldRateLimit
		constant.StreamingTimeout = oldStreamingTimeout
		operation_setting.USDExchangeRate, common.QuotaPerUnit = oldFX, oldQuota
		require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(oldRatio))
		require.NoError(t, billing_setting.SetProductCreditPricing(oldTariff))
	})
	common.MemoryCacheEnabled, common.RedisEnabled, common.GlobalApiRateLimitEnable = false, false, false
	constant.StreamingTimeout = 30
	operation_setting.USDExchangeRate, common.QuotaPerUnit = 1, 1000000
	require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(`{"default":1,"platform":1}`))
	require.NoError(t, billing_setting.SetProductCreditPricing(`{
		"version":"fixture","suppliers":{"has-supply":{"chat":{"fixture":{
			"version":"fixture-1","currency":"CNY","tariff":{"version":"metered_v2",
			"operation":"chat","policy":"provider_reported_v1","terms":[
				{"key":"input","unit":"token","price":{"numerator":"1","denominator":"1000000"}},
				{"key":"output","unit":"token","price":{"numerator":"2","denominator":"1000000"}}
			]}
		}}}}
	}`))
	db, err := gorm.Open(sqlite.Open(t.TempDir()+"/failover.db"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.Model{}, &model.Vendor{}, &model.Channel{}, &model.Ability{}, &model.ClientModelRoute{}, &model.ClientModelTarget{}))
	model.DB, model.LOG_DB = db, db
	common.SetDatabaseTypes(common.DatabaseTypeSQLite, common.DatabaseTypeSQLite)
	model.InitDatabaseColumns()
	require.NoError(t, db.Create(&model.User{Id: 1, Username: "routing", Status: common.UserStatusEnabled, Group: "default"}).Error)

	var modelChecks, reserves, sends atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sends.Add(1)
		w.Header().Set(marker, "gateway")
		var request struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		require.NoError(t, common.DecodeJson(r.Body, &request))
		if len(request.Messages) > 0 && request.Messages[0].Content == "start-stream" {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = fmt.Fprint(w, "data: "+`{"id":"fixture","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","content":"partial output"},"finish_reason":null}]}`+"\n\n")
			w.(http.Flusher).Flush()
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = fmt.Fprint(w, refusal)
	}))
	t.Cleanup(provider.Close)
	for i, name := range []string{"no-supply", "has-supply"} {
		id, status := i+1, common.ChannelStatusEnabled
		if name == "no-supply" {
			status = common.ChannelStatusManuallyDisabled
		}
		require.NoError(t, db.Create(&model.ClientModelRoute{Id: id, PublicId: name, Client: "all", Enabled: true,
			Metadata: `{"operations":["chat.completions.create"]}`}).Error)
		require.NoError(t, db.Create(&model.ClientModelTarget{Id: id, RouteId: id, ChannelId: id, UpstreamModel: name,
			BillingModel: name + "-supplier-price", PreferredWires: `["openai_chat"]`, Enabled: true, Weight: 1}).Error)
		require.NoError(t, db.Create(&model.Channel{Id: id, Type: constant.ChannelTypeOpenAI, Status: status,
			Name: name, Models: name, Group: "default", Key: "fixture-provider-key", BaseURL: &provider.URL}).Error)
		require.NoError(t, db.Create(&model.Ability{Group: "default", Model: name, ChannelId: id, Enabled: status == common.ChannelStatusEnabled}).Error)
	}
	service.InitHttpClient()
	service.InitTokenEncoders()
	conversion := platformbilling.NativeConversion{Version: "product-fixture-1", Numerator: "1", Denominator: "1", Rounding: "lr-cumulative-ceil-v1"}
	hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/internal/v1/model-credentials/verify":
			var in map[string]string
			require.NoError(t, common.DecodeJson(r.Body, &in))
			product := "superwagie"
			switch in["product_authorization"] {
			case "superwagie", "insufficient-funds", "reserve-policy-denied", "reserve-unknown", "reserve-ambiguous-403", "vision-denied":
			case "agentwiki":
				product = "agentwiki"
			default:
				w.WriteHeader(http.StatusForbidden)
				return
			}
			if in["model"] == "" {
				assert.NotContains(t, in, "managed_operation")
			} else {
				modelChecks.Add(1)
				assert.Equal(t, "chat.completions.create", in["wire_operation"])
				assert.NotContains(t, in, "purpose")
				if in["product_authorization"] == "vision-denied" {
					assert.Equal(t, "no-supply", in["model"])
					assert.Equal(t, "vision", in["managed_operation"])
					w.WriteHeader(http.StatusForbidden)
					_, _ = fmt.Fprint(w, `{"error":"managed_model_not_allowed"}`)
					return
				}
				assert.Equal(t, "chat", in["managed_operation"])
			}
			grant := platformbilling.Claims{CreditScope: platformbilling.ProductCreditScope, SourceVerified: true,
				FundingProductID: product, ID: "stable-key", CredentialKind: "api_key", InstanceID: "default", Audience: "llmrouter",
				ActorID: "actor", AccountID: "team", OriginProductID: product, ModelScope: "llmrouter_available",
				PricingSource: platformbilling.NativeCreditPricingSource, CredentialVersion: 2, Asset: "CREDIT/6",
				FundingMode: "credits", NativeCreditConversion: &conversion}
			raw, err := common.Marshal(grant)
			require.NoError(t, err)
			_, _ = w.Write(raw)
		case "/internal/v1/calls/native-reserve":
			reserves.Add(1)
			var in platformbilling.NativeReserveRequest
			require.NoError(t, common.DecodeJson(r.Body, &in))
			assert.Equal(t, "has-supply", in.Model)
			assert.Equal(t, "has-supply-supplier-price", in.Quote.BillingModel)
			assert.Equal(t, "chat", in.ManagedOperation)
			switch in.ProductAuthorization {
			case "insufficient-funds":
				w.WriteHeader(http.StatusPaymentRequired)
				_, _ = fmt.Fprint(w, `{"error":"insufficient_credits"}`)
				return
			case "reserve-policy-denied":
				w.WriteHeader(http.StatusForbidden)
				_, _ = fmt.Fprint(w, `{"error":"managed_model_not_allowed"}`)
				return
			case "reserve-unknown":
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			case "reserve-ambiguous-403":
				w.WriteHeader(http.StatusForbidden)
				_, _ = fmt.Fprint(w, `{"error":"managed_model_not_allowed","hold_state":"unknown"}`)
				return
			}
			reserved := platformbilling.RemoteCall{CreditScope: in.CreditScope, CredentialVersion: 2, TaskID: in.TaskID,
				CallID: in.CallID, CredentialID: "stable-key", InstanceID: "default", ActorID: "actor", AccountID: "team",
				OriginProductID: "superwagie", RequestDigest: in.RequestDigest, WireOperation: in.WireOperation, NativePricing: &in.Quote,
				Rate: platformbilling.Rate{Model: in.Model, Asset: "CREDIT/6", Semantics: "llmrouter_native"},
				Held: in.Quote.MaxAmount, ChargeAsset: "CREDIT/6", FundingMode: "credits", FundingProductID: "superwagie", Status: "RESERVED"}
			raw, err := common.Marshal(reserved)
			require.NoError(t, err)
			_, _ = w.Write(raw)
		default:
			t.Errorf("unexpected UniHub request %s", r.URL.Path)
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	t.Cleanup(hub.Close)
	adapter, err := platformbilling.New(db, platformbilling.Config{
		Enabled: true, Origin: hub.URL, ServiceToken: "service-service-service-service-token", InstanceID: "default",
		PrincipalID: 1, Group: "platform", NativeGroup: "default", MaxBodyBytes: 8192, MaxInputTokens: 1000, MaxOutputTokens: 100,
	})
	require.NoError(t, err)
	gateway := gin.New()
	SetManagedKeyDispatch(gateway, adapter)
	SetRelayRouter(gateway)

	for _, tc := range []struct {
		name, model, proof      string
		status                  int
		safe                    bool
		checks, reserves, sends int32
	}{
		{"configured-route-without-supply", "no-supply", "superwagie", http.StatusServiceUnavailable, true, 1, 0, 0},
		{"model-without-abilities", "no-abilities", "superwagie", http.StatusServiceUnavailable, true, 1, 0, 0},
		{"another-product", "no-supply", "agentwiki", http.StatusInternalServerError, false, 0, 0, 0},
		{"unverified-product", "no-supply", "invalid-proof", http.StatusForbidden, false, 0, 0, 0},
		{"insufficient-funds", "has-supply", "insufficient-funds", http.StatusPaymentRequired, false, 1, 1, 0},
		{"reserve-policy-denied", "has-supply", "reserve-policy-denied", http.StatusForbidden, false, 1, 1, 0},
		{"reserve-unknown", "has-supply", "reserve-unknown", http.StatusServiceUnavailable, false, 1, 1, 0},
		{"reserve-ambiguous-403", "has-supply", "reserve-ambiguous-403", http.StatusForbidden, false, 1, 1, 0},
		{"provider-503", "has-supply", "superwagie", http.StatusServiceUnavailable, false, 1, 1, 1},
		{"interrupted-stream", "has-supply", "superwagie", http.StatusOK, false, 1, 1, 1},
		{"vision-policy-denied", "no-supply", "vision-denied", http.StatusForbidden, false, 1, 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			beforeCheck, beforeReserve, beforeSend := modelChecks.Load(), reserves.Load(), sends.Load()
			content := `"hello"`
			switch tc.name {
			case "vision-policy-denied":
				content = `[{"type":"image_url","image_url":{"url":"data:image/png;base64,AA=="}}]`
			case "interrupted-stream":
				content = `"start-stream"`
			}
			req := httptest.NewRequest(http.MethodPost, "/platform/v1/chat/completions", strings.NewReader(fmt.Sprintf(
				`{"model":%q,"messages":[{"role":"user","content":%s}],"max_tokens":16,"stream":true}`, tc.model, content)))
			req.Header.Set("Authorization", "Bearer uh_key_test")
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Idempotency-Key", tc.name)
			req.Header.Set(platformbilling.ProductAuthorizationHeader, tc.proof)
			rec := httptest.NewRecorder()
			gateway.ServeHTTP(rec, req)

			assert.Equal(t, tc.status, rec.Code, rec.Body.String())
			assert.Equal(t, tc.checks, modelChecks.Load()-beforeCheck)
			assert.Equal(t, tc.reserves, reserves.Load()-beforeReserve)
			assert.Equal(t, tc.sends, sends.Load()-beforeSend)
			switch tc.name {
			case "interrupted-stream":
				assert.Contains(t, rec.Header().Get("Content-Type"), "text/event-stream")
				assert.Contains(t, rec.Body.String(), "partial output")
			case "reserve-policy-denied", "reserve-unknown", "reserve-ambiguous-403":
				var row platformbilling.Call
				require.NoError(t, db.First(&row, "call_id = ?", rec.Header().Get("X-UniHub-Call-ID")).Error)
				if tc.name == "reserve-policy-denied" {
					assert.Equal(t, "REJECTED", row.Status)
				} else {
					assert.Equal(t, "RESERVE_UNKNOWN", row.Status)
				}
			}
			if tc.safe {
				assert.Equal(t, "gateway", rec.Header().Get(marker))
				assert.JSONEq(t, refusal, rec.Body.String())
				return
			}
			if strings.HasSuffix(tc.name, "policy-denied") {
				assert.Equal(t, "gateway", rec.Header().Get(marker))
				assert.JSONEq(t, `{"error":{"code":"managed_model_not_allowed","stage":"pre_admission"}}`, rec.Body.String())
				return
			}
			assert.Empty(t, rec.Header().Get(marker))
			assert.NotContains(t, rec.Body.String(), `"stage":"pre_admission"`)
		})
	}
}
