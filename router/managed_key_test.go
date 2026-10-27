package router

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
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

func TestManagedKeyStandardChatUsesOnlyCentralBilling(t *testing.T) {
	enableUniHubBoundary(t)
	require.NoError(t, i18n.Init())
	oldDB, oldLogDB, oldCache, oldRedis, oldRetry := model.DB, model.LOG_DB, common.MemoryCacheEnabled, common.RedisEnabled, common.RetryTimes
	oldGlobal := common.GlobalApiRateLimitEnable
	oldModelRatios, oldGroupRatios := ratio_setting.ModelRatio2JSONString(), ratio_setting.GroupRatio2JSONString()
	t.Cleanup(func() {
		perfmetrics.DrainRelaySamples()
		model.DB, model.LOG_DB, common.MemoryCacheEnabled, common.RedisEnabled, common.RetryTimes = oldDB, oldLogDB, oldCache, oldRedis, oldRetry
		common.GlobalApiRateLimitEnable = oldGlobal
		require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(oldModelRatios))
		require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(oldGroupRatios))
	})
	common.MemoryCacheEnabled, common.RedisEnabled, common.RetryTimes, common.GlobalApiRateLimitEnable = true, false, 0, false
	db, err := gorm.Open(sqlite.Open(t.TempDir()+"/sdk.db"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.Token{}, &model.UserSubscription{}, &model.Channel{}, &model.Ability{}, &model.ClientModelRoute{}, &model.ClientModelTarget{}, &model.UpstreamAccountPool{}, &model.UpstreamAccountMember{}, &model.UpstreamAccountState{}))
	model.DB, model.LOG_DB = db, db
	native := model.User{Id: 1, Username: "fixed-routing-principal", Status: common.UserStatusEnabled, Group: "platform", Quota: 777, UsedQuota: 8, RequestCount: 9}
	require.NoError(t, db.Create(&native).Error)
	var providerCalls, reserves, settles, nativeWrites atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		providerCalls.Add(1)
		assert.Equal(t, "Bearer supplier-secret", r.Header.Get("Authorization"))
		assert.Empty(t, r.Header.Get("X-Account-ID"))
		assert.Empty(t, r.Header.Get("X-Channel-ID"))
		var input map[string]any
		require.NoError(t, common.DecodeJson(r.Body, &input))
		assert.EqualValues(t, 100, input["max_tokens"])
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"answer","object":"chat.completion","model":"managed-test","choices":[{"index":0,"message":{"role":"assistant","content":"central funded reply"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`))
	}))
	t.Cleanup(provider.Close)
	base := provider.URL
	require.NoError(t, db.Create(&model.Channel{Id: 1, Type: constant.ChannelTypeOpenAI, Key: "supplier-secret", Status: common.ChannelStatusEnabled, BaseURL: &base, Name: "managed", Models: "managed-test", Group: "platform"}).Error)
	require.NoError(t, db.Create(&model.Ability{Group: "platform", Model: "managed-test", ChannelId: 1, Enabled: true}).Error)
	require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(`{"managed-test":0}`))
	require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(`{"platform":1}`))
	model.InitChannelCache()
	service.InitHttpClient()
	service.InitTokenEncoders()
	rate := platformbilling.Rate{ID: "rate", Model: "managed-test", Status: "published", Semantics: "exclusive_v1", Input: platformbilling.Rational{Numerator: "1", Denominator: "1"}, Output: platformbilling.Rational{Numerator: "1", Denominator: "1"}, CacheRead: platformbilling.Rational{Numerator: "1", Denominator: "1"}, CacheWrite: platformbilling.Rational{Numerator: "1", Denominator: "1"}, Reasoning: platformbilling.Rational{Numerator: "1", Denominator: "1"}}
	remoteCalls := map[string]platformbilling.RemoteCall{}
	var remoteMu sync.Mutex
	hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "Bearer service-service-service-service-token", r.Header.Get("Authorization"))
		var out any
		switch r.URL.Path {
		case "/internal/v1/model-credentials/verify":
			var input map[string]string
			require.NoError(t, common.DecodeJson(r.Body, &input))
			assert.Equal(t, "instance-a", input["instance_id"])
			if input["token"] == "uh_key_revoked" {
				w.WriteHeader(403)
				return
			}
			instance, models := "instance-a", []string{"managed-test"}
			if input["token"] == "uh_key_wronginstance" {
				instance = "instance-b"
			}
			if input["token"] == "uh_key_wrongmodel" {
				models = []string{"different"}
			}
			out = platformbilling.Claims{ID: input["token"] + "-id", CredentialKind: "api_key", InstanceID: instance, Audience: "llmrouter", ActorID: "actor", AccountID: "payer", OriginProductID: "client", Models: models, ExpiresAt: time.Date(9999, 12, 31, 23, 59, 59, 0, time.UTC)}
		case "/internal/v1/rates":
			out = []platformbilling.Rate{rate}
		case "/internal/v1/calls/reserve":
			reserves.Add(1)
			var input map[string]string
			require.NoError(t, common.DecodeJson(r.Body, &input))
			assert.Equal(t, "instance-a", input["instance_id"])
			assert.NotEqual(t, "attacker-task", input["task_id"])
			assert.Equal(t, "task_"+input["call_id"], input["task_id"])
			if input["credential"] == "uh_key_budget" {
				w.WriteHeader(402)
				return
			}
			remote := platformbilling.RemoteCall{CallID: input["call_id"], Status: "RESERVED", ActorID: "actor", AccountID: "payer", OriginProductID: "client", InstanceID: "instance-a", CredentialID: input["credential"] + "-id", RequestDigest: input["request_digest"], Rate: rate}
			remoteMu.Lock()
			remoteCalls[input["call_id"]] = remote
			remoteMu.Unlock()
			out = remote
		default:
			parts := strings.Split(r.URL.Path, "/")
			if len(parts) != 6 || parts[5] != "settle" {
				t.Errorf("unexpected operation %s", r.URL.Path)
				w.WriteHeader(500)
				return
			}
			settles.Add(1)
			var input struct {
				Usage platformbilling.Usage `json:"usage"`
			}
			require.NoError(t, common.DecodeJson(r.Body, &input))
			assert.Equal(t, "10", input.Usage.Input)
			assert.Equal(t, "5", input.Usage.Output)
			remoteMu.Lock()
			remote := remoteCalls[parts[4]]
			remote.Status, remote.Charged = "SETTLED", "15"
			remoteCalls[parts[4]] = remote
			remoteMu.Unlock()
			out = remote
		}
		data, err := common.Marshal(out)
		require.NoError(t, err)
		_, _ = w.Write(data)
	}))
	t.Cleanup(hub.Close)
	adapter, err := platformbilling.New(db, platformbilling.Config{Enabled: true, Origin: hub.URL, ServiceToken: "service-service-service-service-token", InstanceID: "instance-a", PrincipalID: 1, Group: "platform", MaxBodyBytes: 8192, MaxInputTokens: 1000, MaxOutputTokens: 100})
	require.NoError(t, err)
	observeNative := func(tx *gorm.DB) {
		switch tx.Statement.Table {
		case "users", "tokens", "user_subscriptions":
			nativeWrites.Add(1)
		}
	}
	require.NoError(t, db.Callback().Create().Before("gorm:create").Register("test:managed-native-write", observeNative))
	require.NoError(t, db.Callback().Update().Before("gorm:update").Register("test:managed-native-write", observeNative))
	require.NoError(t, db.Callback().Delete().Before("gorm:delete").Register("test:managed-native-write", observeNative))
	engine := gin.New()
	SetManagedKeyDispatch(engine, adapter)
	SetRelayRouter(engine)
	// Native isolation remains active; unified SDK traffic must reach its own chain.
	body := []byte(`{"model":"managed-test","messages":[{"role":"user","content":"hello"}]}`)
	send := func(method, path, key, clientID string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+key)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-UniHub-Task-ID", "attacker-task")
		req.Header.Set("X-Account-ID", "attacker")
		req.Header.Set("X-Channel-ID", "999")
		if clientID != "" {
			req.Header.Set("Idempotency-Key", clientID)
		}
		w := httptest.NewRecorder()
		engine.ServeHTTP(w, req)
		return w
	}
	first := send(http.MethodPost, "/v1/chat/completions", "uh_key_valid", "")
	require.Equal(t, 200, first.Code, first.Body.String())
	assert.Contains(t, first.Body.String(), "central funded reply")
	assert.NotEmpty(t, first.Header().Get("X-UniHub-Call-ID"))
	assert.EqualValues(t, 1, providerCalls.Load())
	assert.EqualValues(t, 1, reserves.Load())
	assert.EqualValues(t, 1, settles.Load())
	second := send(http.MethodPost, "/v1/chat/completions", "uh_key_valid", "client-call")
	require.Equal(t, 200, second.Code, second.Body.String())
	duplicate := send(http.MethodPost, "/v1/chat/completions", "uh_key_valid", "client-call")
	assert.Equal(t, 409, duplicate.Code, duplicate.Body.String())
	assert.EqualValues(t, 2, providerCalls.Load())
	assert.EqualValues(t, 2, settles.Load())
	concurrent := make(chan *httptest.ResponseRecorder, 2)
	for range 2 {
		go func() { concurrent <- send(http.MethodPost, "/v1/chat/completions", "uh_key_valid", "concurrent-call") }()
	}
	statuses := []int{(<-concurrent).Code, (<-concurrent).Code}
	assert.ElementsMatch(t, []int{200, 409}, statuses)
	assert.EqualValues(t, 3, providerCalls.Load())
	assert.EqualValues(t, 3, settles.Load())
	for _, tc := range []struct {
		path, key, clientID string
		status              int
	}{
		{"/v1/chat/completions", "uh_key_revoked", "", 403}, {"/v1/chat/completions", "uh_key_wronginstance", "", 400},
		{"/v1/chat/completions", "uh_key_wrongmodel", "", 400}, {"/v1/chat/completions", "uh_key_budget", "", 402},
		{"/v1/completions", "uh_key_valid", "", 501}, {"/v1/chat/completions", "uh_key_valid", "invalid key", 400},
	} {
		got := send(http.MethodPost, tc.path, tc.key, tc.clientID)
		assert.Equal(t, tc.status, got.Code, got.Body.String())
		if tc.key == "uh_key_budget" {
			assert.Contains(t, got.Body.String(), "unified_budget_or_funds_exhausted")
		}
	}
	assert.EqualValues(t, 3, providerCalls.Load())
	assert.EqualValues(t, 3, settles.Load())
	assert.Zero(t, nativeWrites.Load())
	for _, name := range []string{"Idempotency-Key", "Authorization"} {
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer uh_key_valid")
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set(name, "duplicate")
		if name == "Authorization" {
			req.Header.Add(name, "Bearer uh_key_valid")
		} else {
			req.Header.Add(name, "second")
		}
		w := httptest.NewRecorder()
		engine.ServeHTTP(w, req)
		if name == "Authorization" {
			assert.Equal(t, 401, w.Code, w.Body.String())
		} else {
			assert.Equal(t, 400, w.Code, w.Body.String())
		}
	}
	models := send(http.MethodGet, "/v1/models", "uh_key_valid", "")
	require.Equal(t, 200, models.Code, models.Body.String())
	assert.Contains(t, models.Body.String(), `"id":"managed-test"`)
	assert.Equal(t, 404, send(http.MethodGet, "/v1/models/hidden", "uh_key_valid", "").Code)
	var after model.User
	require.NoError(t, db.First(&after, 1).Error)
	assert.Equal(t, 777, after.Quota)
	assert.Equal(t, 8, after.UsedQuota)
	assert.Equal(t, 9, after.RequestCount)
	var call platformbilling.Call
	require.NoError(t, db.First(&call, "call_id = ?", first.Header().Get("X-UniHub-Call-ID")).Error)
	assert.Equal(t, "SETTLED", call.Status)
	assert.Equal(t, "actor", call.ActorID)
	assert.Equal(t, "payer", call.AccountID)
	assert.Equal(t, "instance-a", call.InstanceID)
	require.NoError(t, db.AutoMigrate(&model.Task{}))
	jobCall := platformbilling.Call{CallID: "video-call", CredentialID: "uh_key_valid-id", ActorID: "actor", AccountID: "payer", OriginProductID: "client", InstanceID: "instance-a", TaskID: "server-job-task", Model: "managed-test", Operation: "video", ProviderTaskID: "supplier-job", Status: "UNKNOWN"}
	require.NoError(t, db.Create(&jobCall).Error)
	require.NoError(t, db.Create(&model.Task{TaskID: "public-video-id", PlatformCallID: "video-call", FundingOwner: "platform", Status: model.TaskStatusQueued, Progress: "0%", PrivateData: model.TaskPrivateData{UpstreamTaskID: "supplier-job"}}).Error)
	for _, path := range []string{"/v1/videos/video-call", "/v1/videos/supplier-job", "/v1/videos/public-video-id", "/platform/v1/videos/video-call"} {
		got := send(http.MethodGet, path, "uh_key_valid", "")
		require.Equal(t, 200, got.Code, got.Body.String())
		assert.Contains(t, got.Body.String(), `"task_id":"public-video-id"`)
	}
	assert.Equal(t, 404, send(http.MethodGet, "/v1/videos/public-video-id", "uh_key_other", "").Code)
	assert.Equal(t, 409, send(http.MethodGet, "/v1/videos/public-video-id/content", "uh_key_valid", "").Code)
	receipt := send(http.MethodGet, "/platform/v1/calls/"+call.CallID, "uh_key_valid", "")
	require.Equal(t, 200, receipt.Code, receipt.Body.String())
	assert.Contains(t, receipt.Body.String(), `"status":"SETTLED"`)
}
