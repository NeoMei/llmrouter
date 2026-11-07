package router

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/internal/platformbilling"
	"github.com/QuantumNous/new-api/model"
	perfmetrics "github.com/QuantumNous/new-api/pkg/perf_metrics"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestManagedNativeChatUsesNativePriceAndOriginalKeyWithoutLocalDebit(t *testing.T) {
	enableUniHubBoundary(t)
	require.NoError(t, i18n.Init())
	oldDB, oldLogDB, oldCache, oldRedis, oldRetry := model.DB, model.LOG_DB, common.MemoryCacheEnabled, common.RedisEnabled, common.RetryTimes
	oldGlobal := common.GlobalApiRateLimitEnable
	oldStreamingTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	oldModelRatios, oldGroupRatios := ratio_setting.ModelRatio2JSONString(), ratio_setting.GroupRatio2JSONString()
	t.Cleanup(func() {
		perfmetrics.DrainRelaySamples()
		model.DB, model.LOG_DB, common.MemoryCacheEnabled, common.RedisEnabled, common.RetryTimes = oldDB, oldLogDB, oldCache, oldRedis, oldRetry
		common.GlobalApiRateLimitEnable = oldGlobal
		constant.StreamingTimeout = oldStreamingTimeout
		require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(oldModelRatios))
		require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(oldGroupRatios))
	})
	common.MemoryCacheEnabled, common.RedisEnabled, common.RetryTimes, common.GlobalApiRateLimitEnable = true, false, 0, false
	db, err := gorm.Open(sqlite.Open(t.TempDir()+"/native-sdk.db"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Model{}, &model.Vendor{}, &model.Log{}, &model.User{}, &model.Token{}, &model.UserSubscription{}, &model.Channel{}, &model.Ability{}, &model.ClientModelRoute{}, &model.ClientModelTarget{}, &model.UpstreamAccountPool{}, &model.UpstreamAccountMember{}, &model.UpstreamAccountState{}))
	model.DB, model.LOG_DB = db, db
	require.NoError(t, db.Create(&model.User{Id: 1, Username: "routing", Status: common.UserStatusEnabled, Group: "default", Quota: 777, UsedQuota: 8, RequestCount: 9}).Error)
	providerCalls, nativeWrites, settles := 0, 0, 0
	responseMode, authorityMode := "", ""
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		providerCalls++
		if responseMode == "unknown" {
			conn, _, err := w.(http.Hijacker).Hijack()
			require.NoError(t, err)
			_ = conn.Close()
			return
		}
		switch {
		case strings.HasSuffix(r.URL.Path, "/messages"):
			assert.Equal(t, "supplier-secret", r.Header.Get("x-api-key"))
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"msg_native","type":"message","role":"assistant","model":"native-claude","content":[{"type":"text","text":"native claude reply"}],"stop_reason":"end_turn","usage":{"input_tokens":10,"output_tokens":5}}`))
			return
		case strings.HasSuffix(r.URL.Path, "/responses"):
			var input struct {
				Store *bool `json:"store"`
			}
			require.NoError(t, common.DecodeJson(r.Body, &input))
			require.NotNil(t, input.Store)
			assert.False(t, *input.Store)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"resp_native","object":"response","status":"completed","model":"native-responses","output":[{"id":"msg_native","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"native responses reply","annotations":[]}]}],"usage":{"input_tokens":10,"output_tokens":5,"total_tokens":15}}`))
			return
		case strings.HasSuffix(r.URL.Path, "/embeddings"):
			w.Header().Set("Content-Type", "application/json")
			vectors := make([]float64, 1024)
			body, e := common.Marshal(gin.H{"model": "text-embedding-v4", "object": "list", "data": []gin.H{{"index": 0, "embedding": vectors}}, "usage": gin.H{"prompt_tokens": 15, "total_tokens": 15}})
			require.NoError(t, e)
			_, _ = w.Write(body)
			return
		case strings.HasSuffix(r.URL.Path, "/text-rerank"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"output":{"results":[{"index":0,"relevance_score":0.9,"document":{"text":"hello"}}]},"usage":{"total_tokens":15}}`))
			return
		}
		if strings.Contains(r.URL.Path, "native-gemini") {
			w.Header().Set("Content-Type", "application/json")
			if strings.HasSuffix(r.URL.Path, ":streamGenerateContent") {
				assert.Equal(t, "sse", r.URL.Query().Get("alt"))
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = w.Write([]byte("data: "))
			}
			_, _ = w.Write([]byte(`{"candidates":[{"content":{"parts":[{"text":"native gemini reply"}],"role":"model"},"finishReason":"STOP","index":0}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":5,"totalTokenCount":15}}`))
			if strings.HasSuffix(r.URL.Path, ":streamGenerateContent") {
				_, _ = w.Write([]byte("\n\n"))
			}
			return
		}
		if responseMode == "missing_usage" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"answer","object":"chat.completion","model":"native-test","choices":[{"index":0,"message":{"role":"assistant","content":"unmetered reply"},"finish_reason":"stop"}]}`))
			return
		}
		assert.Equal(t, "Bearer supplier-secret", r.Header.Get("Authorization"))
		assert.Empty(t, r.Header.Get("X-Account-ID"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"answer","object":"chat.completion","model":"native-test","choices":[{"index":0,"message":{"role":"assistant","content":"native funded reply"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`))
	}))
	t.Cleanup(provider.Close)
	base := provider.URL
	require.NoError(t, db.Create(&model.Channel{Id: 1, Type: constant.ChannelTypeOpenAI, Key: "supplier-secret", Status: common.ChannelStatusEnabled, BaseURL: &base, Name: "native", Models: "native-test", Group: "default"}).Error)
	require.NoError(t, db.Create(&model.Ability{Group: "default", Model: "native-test", ChannelId: 1, Enabled: true}).Error)
	require.NoError(t, db.Create(&model.Channel{Id: 2, Type: constant.ChannelTypeGemini, Key: "supplier-secret", Status: common.ChannelStatusEnabled, BaseURL: &base, Name: "gemini", Models: "native-gemini", Group: "default"}).Error)
	require.NoError(t, db.Create(&model.Ability{Group: "default", Model: "native-gemini", ChannelId: 2, Enabled: true}).Error)
	for i, entry := range []struct {
		name string
		kind int
	}{{"native-claude", constant.ChannelTypeAnthropic}, {"native-responses", constant.ChannelTypeCodex}, {"text-embedding-v4", constant.ChannelTypeAli}, {"qwen3-rerank", constant.ChannelTypeAli}} {
		id := i + 3
		key := "supplier-secret"
		if entry.kind == constant.ChannelTypeCodex {
			key = `{"access_token":"supplier-secret","account_id":"supplier-account"}`
		}
		require.NoError(t, db.Create(&model.Channel{Id: id, Type: entry.kind, Key: key, Status: common.ChannelStatusEnabled, BaseURL: &base, Name: entry.name, Models: entry.name, Group: "default"}).Error)
		require.NoError(t, db.Create(&model.Ability{Group: "default", Model: entry.name, ChannelId: id, Enabled: true}).Error)
	}
	for i, entry := range []struct {
		name, metadata string
		channel        int
	}{
		{"text-embedding-v4", `{"operations":["embeddings.create"],"input_modalities":["text"],"output_modalities":["embedding"],"embedding_space":{"provider":"aliyun","model":"text-embedding-v4","version":"v4","dimensions":1024,"input_semantics":"text/query-document-v1"}}`, 5},
		{"qwen3-rerank", `{"operations":["rerank.create"],"input_modalities":["text"],"output_modalities":["text"]}`, 6},
	} {
		id := i + 1
		require.NoError(t, db.Create(&model.ClientModelRoute{Id: id, PublicId: entry.name, DisplayName: entry.name, Client: "all", Enabled: true, Metadata: entry.metadata, Aliases: func() string {
			if i == 0 {
				return `["embed-alias"]`
			}
			return `[]`
		}()}).Error)
		require.NoError(t, db.Create(&model.ClientModelTarget{Id: id, RouteId: id, ChannelId: entry.channel, UpstreamModel: entry.name, BillingModel: entry.name, PreferredWires: `["openai_chat"]`, Enabled: true, Weight: 1}).Error)
	}
	require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(`{"native-test":1,"native-gemini":1,"native-claude":1,"native-responses":1,"text-embedding-v4":1,"qwen3-rerank":1}`))
	require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(`{"default":1,"platform":1}`))
	model.InitChannelCache()
	service.InitHttpClient()
	service.InitTokenEncoders()
	var remote platformbilling.RemoteCall
	hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var out any
		switch {
		case r.URL.Path == "/internal/v1/model-credentials/verify":
			if authorityMode == "revoked" || authorityMode == "membership" {
				w.WriteHeader(403)
				return
			}
			grant := platformbilling.Claims{ID: "original-key-id", CredentialKind: "api_key", InstanceID: "default", Audience: "llmrouter", ActorID: "actor", AccountID: "team", OriginProductID: "superwagie", ModelScope: "llmrouter_available", PricingSource: "llmrouter_native", CredentialVersion: 2, Asset: "CNY/6", FundingMode: "prepaid_cash"}
			if authorityMode == "expired" {
				grant.ExpiresAt = time.Now().Add(-time.Hour)
			}
			out = grant
		case r.URL.Path == "/internal/v1/calls/native-reserve":
			if authorityMode == "budget" {
				w.WriteHeader(402)
				_, _ = w.Write([]byte(`{"error":"usage_budget_exceeded"}`))
				return
			}
			var in platformbilling.NativeReserveRequest
			require.NoError(t, common.DecodeJson(r.Body, &in))
			assert.Equal(t, "uh_key_original", in.Credential)
			assert.Equal(t, "default", in.Quote.Group)
			remote = platformbilling.RemoteCall{CredentialVersion: 2, TaskID: in.TaskID, CallID: in.CallID, CredentialID: "original-key-id", InstanceID: "default", ActorID: "actor", AccountID: "team", OriginProductID: "superwagie", RequestDigest: in.RequestDigest, WireOperation: in.WireOperation, NativePricing: &in.Quote, Rate: platformbilling.Rate{Model: in.Model, Asset: "CNY/6", Semantics: "llmrouter_native"}, Held: in.Quote.MaxAmount, ChargeAsset: "CNY/6", FundingMode: "prepaid_cash", FundingProductID: "llmrouter", Status: "RESERVED"}
			out = remote
			if authorityMode == "revoke-after-reserve" {
				authorityMode = "revoked"
			}
		case strings.HasSuffix(r.URL.Path, "/native-settle"):
			settles++
			var in platformbilling.NativeSettleRequest
			require.NoError(t, common.DecodeJson(r.Body, &in))
			assert.Equal(t, "15", in.NativeQuota)
			remote.Status, remote.Charged = "SETTLED", in.Amount
			out = remote
		case strings.HasSuffix(r.URL.Path, "/release"):
			remote.Status, remote.Charged = "RELEASED", "0"
			out = remote
		default:
			t.Errorf("unexpected central endpoint %s", r.URL.Path)
			w.WriteHeader(500)
			return
		}
		b, e := common.Marshal(out)
		require.NoError(t, e)
		_, _ = w.Write(b)
	}))
	t.Cleanup(hub.Close)
	a, err := platformbilling.New(db, platformbilling.Config{Enabled: true, Origin: hub.URL, ServiceToken: "service-service-service-service-token", InstanceID: "default", PrincipalID: 1, Group: "platform", NativeGroup: "default", MaxBodyBytes: 8192, MaxInputTokens: 1000, MaxOutputTokens: 100})
	require.NoError(t, err)
	observe := func(tx *gorm.DB) {
		if tx.Statement.Table == "users" || tx.Statement.Table == "tokens" || tx.Statement.Table == "user_subscriptions" {
			nativeWrites++
		}
	}
	require.NoError(t, db.Callback().Update().Before("gorm:update").Register("native-test-writes", observe))
	r := gin.New()
	SetManagedKeyDispatch(r, a)
	SetRelayRouter(r)
	request := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewBufferString(`{"model":"native-test","messages":[{"role":"user","content":"hello"}]}`))
	request.Header.Set("Authorization", "Bearer uh_key_original")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Account-ID", "attacker")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, request)
	require.Equal(t, 200, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), "native funded reply")
	assert.Equal(t, 1, providerCalls)
	assert.Equal(t, 1, settles)
	assert.Zero(t, nativeWrites)
	request = httptest.NewRequest("POST", "/v1beta/models/native-gemini:generateContent", bytes.NewBufferString(`{"contents":[{"role":"user","parts":[{"text":"hello"}]}]}`))
	request.Header.Set("Authorization", "Bearer uh_key_original")
	request.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	r.ServeHTTP(w, request)
	require.Equal(t, 200, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), "native gemini reply")
	assert.Equal(t, 2, settles)
	request = httptest.NewRequest("POST", "/v1beta/models/native-gemini:streamGenerateContent?alt=sse", bytes.NewBufferString(`{"contents":[{"role":"user","parts":[{"text":"hello"}]}]}`))
	request.Header.Set("x-goog-api-key", "uh_key_original")
	request.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	r.ServeHTTP(w, request)
	require.Equal(t, 200, w.Code, w.Body.String())
	assert.Contains(t, w.Header().Get("Content-Type"), "text/event-stream")
	assert.Contains(t, w.Body.String(), "native gemini reply")
	assert.Equal(t, 3, settles)
	for i, entry := range []struct{ path, body, header string }{
		{"/v1/messages", `{"model":"native-claude","messages":[{"role":"user","content":"hello"}],"max_tokens":100}`, "x-api-key"},
		{"/v1/responses", `{"model":"native-responses","input":"hello"}`, "Authorization"},
		{"/v1/embeddings", `{"model":"text-embedding-v4","input":["hello"]}`, "Authorization"},
		{"/v1/embeddings", `{"model":"embed-alias","input":["hello"]}`, "Authorization"},
		{"/v1/rerank", `{"model":"qwen3-rerank","query":"hello","documents":["hello"],"top_n":1}`, "Authorization"},
	} {
		request = httptest.NewRequest("POST", entry.path, bytes.NewBufferString(entry.body))
		value := "uh_key_original"
		if entry.header == "Authorization" {
			value = "Bearer " + value
		}
		request.Header.Set(entry.header, value)
		request.Header.Set("Content-Type", "application/json")
		w = httptest.NewRecorder()
		r.ServeHTTP(w, request)
		require.Equal(t, 200, w.Code, w.Body.String())
		assert.Equal(t, 4+i, settles, entry.path)
	}
	for _, field := range []string{`"conversation":"conv_private"`, `"cachedContent":"cachedContents/private"`, `"container":"private-container"`, `"previous_response_id":"resp_private"`, `"store":true`, `"background":true`, `"tools":[{"type":"file_search","vector_store_ids":["vs_private"]}]`, `"tools":[{"type":"code_interpreter","container":"cntr_private"}]`} {
		request = httptest.NewRequest("POST", "/v1/responses", bytes.NewBufferString(`{"model":"native-responses","input":"hello",`+field+`}`))
		request.Header.Set("Authorization", "Bearer uh_key_original")
		request.Header.Set("Content-Type", "application/json")
		w = httptest.NewRecorder()
		r.ServeHTTP(w, request)
		assert.Equal(t, 400, w.Code, w.Body.String())
		assert.Equal(t, 8, providerCalls)
	}
	for _, headers := range []http.Header{
		{"Authorization": {"Bearer uh_key_original"}, "X-Api-Key": {"uh_key_original"}},
		{"X-Goog-Api-Key": {"uh_key_original", "uh_key_original"}},
		{"Authorization": {"Bearer uh_key_original", "Bearer native-secret"}},
	} {
		request = httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewBufferString(`{"model":"native-test","messages":[{"role":"user","content":"hello"}]}`))
		request.Header = headers
		request.Header.Set("Content-Type", "application/json")
		w = httptest.NewRecorder()
		r.ServeHTTP(w, request)
		assert.Equal(t, 401, w.Code, w.Body.String())
		assert.Equal(t, 8, providerCalls)
	}
	for _, denial := range []string{"revoked", "membership", "expired", "budget", "revoke-after-reserve"} {
		authorityMode = denial
		request = httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewBufferString(`{"model":"native-test","messages":[{"role":"user","content":"hello"}]}`))
		request.Header.Set("Authorization", "Bearer uh_key_original")
		request.Header.Set("Content-Type", "application/json")
		w = httptest.NewRecorder()
		r.ServeHTTP(w, request)
		assert.GreaterOrEqual(t, w.Code, 400, denial)
		if denial == "budget" {
			assert.Equal(t, 402, w.Code, w.Body.String())
		}
		assert.Equal(t, 8, providerCalls, denial)
	}
	authorityMode, responseMode = "", "missing_usage"
	request = httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewBufferString(`{"model":"native-test","messages":[{"role":"user","content":"hello"}]}`))
	request.Header.Set("Authorization", "Bearer uh_key_original")
	request.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	r.ServeHTTP(w, request)
	assert.Equal(t, 200, w.Code, w.Body.String())
	assert.Equal(t, 8, settles, "estimated usage must not become a charge")
	var unmetered platformbilling.Call
	require.NoError(t, db.First(&unmetered, "call_id = ?", w.Header().Get("X-UniHub-Call-ID")).Error)
	assert.Equal(t, "UNKNOWN", unmetered.Status)
	authorityMode, responseMode = "", "unknown"
	for i := 0; i < 2; i++ {
		request = httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewBufferString(`{"model":"native-test","messages":[{"role":"user","content":"hello"}]}`))
		request.Header.Set("Authorization", "Bearer uh_key_original")
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Idempotency-Key", "uncertain-call")
		w = httptest.NewRecorder()
		r.ServeHTTP(w, request)
		assert.GreaterOrEqual(t, w.Code, 400)
	}
	assert.Equal(t, 10, providerCalls)
	assert.Equal(t, 8, settles)
	var uncertain platformbilling.Call
	require.NoError(t, db.First(&uncertain, "status = ?", "UNKNOWN").Error)
	assert.Equal(t, "original-key-id", uncertain.CredentialID)
	assert.Zero(t, nativeWrites)

	// An unavailable Ali model is a proved rejection, not an unknown billable
	// outcome. Exercise the actual managed router and its financial lifecycle.
	aliBase := "https://dashscope.aliyuncs.com"
	require.NoError(t, db.Create(&model.Channel{Id: 101, Type: constant.ChannelTypeAli, Key: "supplier-secret", Status: common.ChannelStatusEnabled, BaseURL: &aliBase, Name: "Ali vision", Models: "qwen-vl-max", Group: "default"}).Error)
	require.NoError(t, db.Create(&model.Ability{Group: "default", Model: "qwen-vl-max", ChannelId: 101, Enabled: true}).Error)
	require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(`{"qwen-vl-max":1}`))
	model.InitChannelCache()
	transport := service.GetHttpClient().Transport
	t.Cleanup(func() { service.GetHttpClient().Transport = transport })
	service.GetHttpClient().Transport = aliMissingModelTransport{calls: &providerCalls}
	request = httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewBufferString(`{"model":"qwen-vl-max","messages":[{"role":"user","content":"hello"}]}`))
	request.Header.Set("Authorization", "Bearer uh_key_original")
	request.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	r.ServeHTTP(w, request)
	require.Equal(t, 404, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), "model_not_found")
	var rejected platformbilling.Call
	require.NoError(t, db.First(&rejected, "call_id = ?", w.Header().Get("X-UniHub-Call-ID")).Error)
	assert.Equal(t, "RELEASED", rejected.Status)
	assert.Equal(t, "0", rejected.Charged)
	assert.Contains(t, rejected.Evidence, "supplier_rejection")
	assert.Equal(t, "superwagie", rejected.OriginProductID)
	assert.Equal(t, "original-key-id", rejected.CredentialID)
	assert.Equal(t, 11, providerCalls, "a native rejected request must never be retried")
	assert.Equal(t, 8, settles)
	assert.Zero(t, nativeWrites)
}

type aliMissingModelTransport struct{ calls *int }

func (tr aliMissingModelTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	*tr.calls++
	return &http.Response{StatusCode: http.StatusNotFound, Request: req, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"error":{"message":"Model not exist.","type":"invalid_request_error","param":null,"code":"model_not_found"},"request_id":"supplier-test"}`))}, nil
}
