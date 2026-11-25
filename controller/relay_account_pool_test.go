package controller

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/crypto"
	"github.com/QuantumNous/new-api/relay/channel"
	"github.com/QuantumNous/new-api/relay/channel/gateway"
	"github.com/QuantumNous/new-api/relay/channel/openai"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/profile"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/service/accountpool"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/QuantumNous/new-api/types"
	"github.com/bytedance/gopkg/util/gopool"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRelayAccountPoolHTTP(t *testing.T) {
	for _, mode := range []string{"native", "legacy", "gateway", "gateway-legacy"} {
		for _, scenario := range []string{"failover", "invalid", "budget-zero", "veto", "resources", "quota", "all-cooled", "byok", "cooled-backup"} {
			if mode == "gateway-legacy" && scenario != "cooled-backup" {
				continue
			}
			if mode != "gateway" && scenario == "byok" {
				continue
			}
			t.Run(mode+"/"+scenario, func(t *testing.T) { testRelayAccountPoolHTTP(t, mode, scenario) })
		}
	}
	t.Run("native/stream", func(t *testing.T) { testRelayAccountPoolHTTP(t, "native", "stream") })
	t.Run("native/route-retry", func(t *testing.T) { testRelayAccountPoolHTTP(t, "native", "route-retry") })
	t.Run("native/bad-response", func(t *testing.T) { testRelayAccountPoolHTTP(t, "native", "bad-response") })
}

func testRelayAccountPoolHTTP(t *testing.T, mode, scenario string) {
	gin.SetMode(gin.TestMode)
	previousStreamingTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 60
	t.Cleanup(func() { constant.StreamingTimeout = previousStreamingTimeout })
	db := setupTieredRelayBillingDB(t)
	require.NoError(t, db.AutoMigrate(&model.Channel{}, &model.Ability{}, &model.ClientModelRoute{}, &model.ClientModelTarget{}, &model.UpstreamAccountPool{}, &model.UpstreamAccountMember{}, &model.UpstreamAccountState{}, &model.Log{}, &model.ProviderProfile{}, &model.ProviderModel{}, &model.ProviderKeyPool{}, &model.UserProviderKey{}))
	previousLogDB, previousLog, previousCache, previousRetry := model.LOG_DB, common.LogConsumeEnabled, common.MemoryCacheEnabled, common.RetryTimes
	model.LOG_DB, common.LogConsumeEnabled, common.MemoryCacheEnabled, common.RetryTimes = db, true, false, 1
	t.Setenv("LOG_SQL_DSN", "")
	require.NoError(t, model.InitLogDB())
	previousTranslator := common.TranslateMessage
	require.NoError(t, i18n.Init())
	t.Cleanup(func() { common.TranslateMessage = previousTranslator })
	t.Cleanup(func() {
		model.LOG_DB, common.LogConsumeEnabled, common.MemoryCacheEnabled, common.RetryTimes = previousLogDB, previousLog, previousCache, previousRetry
	})
	ratio_setting.InitRatioSettings()
	previousRatios, previousCompletion := ratio_setting.ModelRatio2JSONString(), ratio_setting.CompletionRatio2JSONString()
	require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(`{"gpt-pool":1}`))
	require.NoError(t, ratio_setting.UpdateCompletionRatioByJSONString(`{"gpt-pool":1}`))
	t.Cleanup(func() {
		require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(previousRatios))
		require.NoError(t, ratio_setting.UpdateCompletionRatioByJSONString(previousCompletion))
	})
	quota := 100000
	if scenario == "quota" {
		quota = 0
	}
	if scenario == "budget-zero" {
		common.RetryTimes = 0
	}
	createTieredRelayWalletFixtures(t, db, 910, 911, quota, quota, "pool-test-token")
	var mu sync.Mutex
	sends := []string{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.Header.Get("Authorization")
		mu.Lock()
		sends = append(sends, key)
		mu.Unlock()
		w.Header().Set("X-LLMRouter-Requested-Model", "forged-request")
		w.Header().Set("X-LLMRouter-Routed-Model", "forged-route")
		if scenario == "stream" {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("data: {\"id\":\"chatcmpl-pool\",\"object\":\"chat.completion.chunk\",\"model\":\"provider-reported\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"},\"finish_reason\":null}]}\n\ndata: {\"id\":\"chatcmpl-pool\",\"object\":\"chat.completion.chunk\",\"model\":\"provider-reported\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":5,\"total_tokens\":15}}\n\ndata: [DONE]\n\n"))
			return
		}
		if scenario == "bad-response" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if key == "Bearer account-A" || scenario == "all-cooled" {
			if scenario == "invalid" {
				w.WriteHeader(401)
				_, _ = w.Write([]byte(`{"error":{"code":"invalid_api_key","message":"invalid"}}`))
				return
			}
			if scenario == "veto" {
				w.Header().Set("X-Should-Retry", "false")
			}
			w.Header().Set("Retry-After", "120")
			w.WriteHeader(429)
			_, _ = w.Write([]byte(`{"error":{"code":"rate_limit","message":"limited"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":"chatcmpl-pool","object":"chat.completion","model":"gpt-pool","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`))
	}))
	defer upstream.Close()
	channelType, credential, multi := constant.ChannelTypeOpenAI, "account-A\naccount-B", true
	if scenario == "route-retry" {
		credential, multi = "account-A", false
	}
	if strings.HasPrefix(mode, "gateway") {
		channelType, credential, multi = constant.ChannelTypeGateway, "unused", false
		cipher, err := crypto.NewAESGCM(make([]byte, 32))
		require.NoError(t, err)
		require.NoError(t, db.Create(&model.ProviderProfile{Id: 71, Name: "test", BaseURL: upstream.URL + "/v1", Status: "active"}).Error)
		require.NoError(t, db.Create(&model.ProviderModel{ProfileId: 71, ModelId: "gpt-pool", Status: "active"}).Error)
		for _, key := range []string{"account-A", "account-B"} {
			encrypted, err := cipher.Encrypt([]byte(key))
			require.NoError(t, err)
			require.NoError(t, db.Create(&model.ProviderKeyPool{ProfileId: 71, EncryptedKey: encrypted, Status: "active"}).Error)
		}
		if scenario == "byok" {
			encrypted, err := cipher.Encrypt([]byte("account-A"))
			require.NoError(t, err)
			require.NoError(t, db.Create(&model.UserProviderKey{UserId: 910, ProfileId: 71, EncryptedKey: encrypted, Status: "active"}).Error)
		}
		previousLoader, previousSelector := gateway.GlobalLoader, gateway.GlobalKeySel
		gateway.InitGlobals(profile.NewLoader(nil, model.NewGormProfileQuerierForDB(db)), gateway.NewKeySelector(db, cipher))
		t.Cleanup(func() { gateway.InitGlobals(previousLoader, previousSelector) })
	}
	require.NoError(t, db.Create(&model.Channel{Id: 921, Type: channelType, Name: "pool", Key: credential, BaseURL: &upstream.URL, Models: "gpt-pool", Group: "default", Status: common.ChannelStatusEnabled, ChannelInfo: model.ChannelInfo{IsMultiKey: multi, MultiKeyMode: constant.MultiKeyModePolling}}).Error)
	require.NoError(t, db.Create(&model.Ability{ChannelId: 921, Group: "default", Model: "gpt-pool", Enabled: true}).Error)
	if !strings.HasSuffix(mode, "legacy") {
		require.NoError(t, db.Create(&model.ClientModelRoute{Id: 931, PublicId: "gpt-pool", Client: "codex", Enabled: true}).Error)
		require.NoError(t, db.Create(&model.ClientModelTarget{Id: 932, RouteId: 931, ChannelId: 921, UpstreamModel: "gpt-pool", BillingModel: "gpt-pool", PreferredWires: `["openai_chat"]`, Enabled: true}).Error)
	}
	if scenario == "cooled-backup" || scenario == "route-retry" {
		if scenario == "cooled-backup" {
			common.RetryTimes = 0
		}
		require.NoError(t, db.Model(&model.Ability{}).Where("channel_id = ?", 921).Update("priority", 10).Error)
		var refs []model.UpstreamAccountRef
		if strings.HasPrefix(mode, "gateway") {
			var err error
			refs, _, _, err = gateway.GlobalKeySel.PlatformAccounts(context.Background(), 71, 921)
			require.NoError(t, err)
		} else {
			primary, err := model.GetChannelById(921, true)
			require.NoError(t, err)
			refs, _, err = model.ChannelAccountRefs(context.Background(), db, primary)
			require.NoError(t, err)
		}
		scope := model.UpstreamAccountScope{Operation: "chat.completions.create", Model: "gpt-pool"}
		if scenario == "cooled-backup" {
			for _, ref := range refs {
				selected, err := model.SelectUpstreamAccount(context.Background(), db, []model.UpstreamAccountRef{ref}, scope, nil, time.Now())
				require.NoError(t, err)
				require.NoError(t, model.RecordUpstreamAccountFailure(context.Background(), db, selected, scope, model.UpstreamAccountFailure{Kind: model.UpstreamAccountCooldown, Reason: "rate_limit", RetryAt: time.Now().Add(120 * time.Second)}, time.Now()))
			}
		}
		backupModel := "gpt-pool"
		if mode == "native" {
			backupModel = "gpt-pool-backup"
		}
		require.NoError(t, db.Create(&model.Channel{Id: 941, Type: constant.ChannelTypeOpenAI, Name: "backup", Key: "account-C", BaseURL: &upstream.URL, Models: backupModel, Group: "default", Status: common.ChannelStatusEnabled}).Error)
		require.NoError(t, db.Create(&model.Ability{ChannelId: 941, Group: "default", Model: backupModel, Enabled: true, Priority: common.GetPointer(int64(-1))}).Error)
		if !strings.HasSuffix(mode, "legacy") {
			backupPriority := 10
			if scenario == "route-retry" {
				backupPriority = 0
				require.NoError(t, db.Model(&model.ClientModelTarget{}).Where("id = ?", 932).Update("priority", 10).Error)
			}
			require.NoError(t, db.Create(&model.ClientModelTarget{Id: 942, RouteId: 931, ChannelId: 941, UpstreamModel: backupModel, BillingModel: "gpt-pool", PreferredWires: `["openai_chat"]`, Enabled: true, Priority: backupPriority}).Error)
		}
	}
	if mode == "cache-legacy" {
		common.MemoryCacheEnabled = true
		model.InitChannelCache()
	}
	service.InitHttpClient()
	service.InitTokenEncoders()
	engine := gin.New()
	engine.Use(func(c *gin.Context) {
		common.SetContextKey(c, constant.ContextKeyUserId, 910)
		common.SetContextKey(c, constant.ContextKeyTokenId, 911)
		common.SetContextKey(c, constant.ContextKeyTokenKey, "pool-test-token")
		common.SetContextKey(c, constant.ContextKeyUserQuota, quota)
		common.SetContextKey(c, constant.ContextKeyTokenGroup, "default")
		common.SetContextKey(c, constant.ContextKeyUserGroup, "default")
		common.SetContextKey(c, constant.ContextKeyUsingGroup, "default")
		common.SetContextKey(c, constant.ContextKeyUserSetting, dto.UserSetting{BillingPreference: "wallet_only"})
		c.Next()
		common.CleanupBodyStorage(c)
	})
	t.Cleanup(func() {
		require.Eventually(t, func() bool { return gopool.WorkerCount() == 0 }, 2*time.Second, time.Millisecond, "drain real asynchronous accounting and metrics before restoring globals")
	})
	engine.POST("/v1/chat/completions", middleware.Distribute(), func(c *gin.Context) { Relay(c, types.RelayFormatOpenAI) })
	repeats := 1
	if scenario == "failover" || scenario == "invalid" || scenario == "all-cooled" {
		repeats = 2
	}
	for request := 0; request < repeats; request++ {
		if mode == "cache-legacy" && request == 1 {
			require.NoError(t, db.Model(&model.Channel{}).Where("id = ?", 921).Update("key", "account-B\naccount-A").Error)
			model.InitChannelCache()
			refreshed, err := model.CacheGetChannel(921)
			require.NoError(t, err)
			assert.Equal(t, []string{"account-B", "account-A"}, refreshed.GetKeys())
		}
		recorder := httptest.NewRecorder()
		body := `{"model":"gpt-pool","messages":[{"role":"user","content":"hi"}],"max_tokens":10}`
		if scenario == "stream" {
			body = `{"model":"gpt-pool","messages":[{"role":"user","content":"hi"}],"max_tokens":10,"stream":true}`
		}
		if scenario == "resources" {
			body = `{"model":"gpt-pool","messages":[{"role":"user","content":"hi"}],"file_id":"owned-by-A","max_tokens":10}`
		}
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		engine.ServeHTTP(recorder, req)
		if scenario == "failover" || scenario == "invalid" || scenario == "cooled-backup" || scenario == "stream" || scenario == "route-retry" {
			require.Equal(t, 200, recorder.Code, recorder.Body.String())
			if scenario == "stream" {
				assert.Contains(t, recorder.Body.String(), `"chatcmpl-pool"`)
			} else {
				assert.Equal(t, 1, strings.Count(recorder.Body.String(), `"chatcmpl-pool"`))
			}
			assert.Equal(t, "gpt-pool", recorder.Header().Get("X-LLMRouter-Requested-Model"))
			expectedRouted := "gpt-pool"
			if mode == "native" && (scenario == "cooled-backup" || scenario == "route-retry") {
				expectedRouted = "gpt-pool-backup"
			}
			assert.Equal(t, expectedRouted, recorder.Header().Get("X-LLMRouter-Routed-Model"))
		} else {
			assert.GreaterOrEqual(t, recorder.Code, 400, recorder.Body.String())
			assert.Empty(t, recorder.Header().Get("X-LLMRouter-Routed-Model"))
			if scenario == "all-cooled" && request == 1 {
				assert.Equal(t, "120", recorder.Header().Get("Retry-After"))
			}
		}
	}
	if scenario == "stream" || scenario == "route-retry" || scenario == "bad-response" {
		if scenario == "route-retry" {
			assert.Equal(t, []string{"Bearer account-A", "Bearer account-C"}, sends)
		}
		return
	}
	if scenario != "failover" && scenario != "invalid" && scenario != "cooled-backup" {
		expected := []string{}
		if scenario == "all-cooled" {
			expected = []string{"Bearer account-A", "Bearer account-B"}
		}
		if scenario == "veto" || scenario == "budget-zero" || scenario == "byok" {
			expected = []string{"Bearer account-A"}
		}
		mu.Lock()
		assert.Equal(t, expected, sends)
		mu.Unlock()
		// Refund is asynchronous in the real BillingSession. Await the observable
		// wallet/token invariant before tearing down its database.
		require.Eventually(t, func() bool {
			wallet, token, used := tieredRelayQuotaState(t, db, 910, 911)
			return wallet == quota && token == quota && used == 0
		}, time.Second, time.Millisecond)
		var logs int64
		require.NoError(t, db.Model(&model.Log{}).Where("type = ?", model.LogTypeConsume).Count(&logs).Error)
		assert.Zero(t, logs)
		return
	}

	mu.Lock()
	if scenario == "cooled-backup" {
		assert.Equal(t, []string{"Bearer account-C"}, sends)
	} else {
		assert.Equal(t, []string{"Bearer account-A", "Bearer account-B", "Bearer account-B"}, sends)
	}
	mu.Unlock()
	wallet, token, used := tieredRelayQuotaState(t, db, 910, 911)
	assert.Equal(t, 100000-15*repeats, wallet)
	assert.Equal(t, 100000-15*repeats, token)
	assert.Equal(t, 15*repeats, used)
	var logs []model.Log
	require.NoError(t, db.Where("type = ?", model.LogTypeConsume).Find(&logs).Error)
	require.Len(t, logs, repeats)
	assert.Equal(t, 15, logs[0].Quota)
	if repeats > 1 {
		assert.Equal(t, 15, logs[1].Quota)
	}
}

func TestPooledAzureAuthenticationCannotBeOverridden(t *testing.T) {
	service.InitHttpClient()
	for _, form := range []bool{false, true} {
		for _, rule := range []string{"fixed", "explicit-passthrough", "wildcard", "regex"} {
			t.Run(fmt.Sprintf("form=%t/%s", form, rule), func(t *testing.T) {
				var mu sync.Mutex
				keys := []string{}
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					mu.Lock()
					keys = append(keys, r.Header.Get("api-key"))
					mu.Unlock()
					w.WriteHeader(400)
				}))
				defer upstream.Close()
				c, _ := gin.CreateTestContext(httptest.NewRecorder())
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{}`))
				c.Request.Header.Set("api-key", "account-B")
				c.Request.Header.Set("Content-Type", "application/json")
				defer common.CleanupBodyStorage(c)
				accountpool.State(c).Selected = &model.UpstreamAccountSelection{}
				override := map[string]any{"Api-Key": "account-B"}
				switch rule {
				case "explicit-passthrough":
					override = map[string]any{"Api-Key": "{client_header:api-key}"}
				case "wildcard":
					override = map[string]any{"*": ""}
				case "regex":
					override = map[string]any{"re:.*": ""}
				}
				info := &relaycommon.RelayInfo{RequestURLPath: "/v1/chat/completions", ChannelMeta: &relaycommon.ChannelMeta{ChannelType: constant.ChannelTypeAzure, ChannelBaseUrl: upstream.URL, ApiKey: "account-A", HeadersOverride: override}}
				adaptor := &openai.Adaptor{}
				var resp *http.Response
				var err error
				if form {
					resp, err = channel.DoFormRequest(adaptor, c, info, strings.NewReader(`{}`))
				} else {
					resp, err = channel.DoApiRequest(adaptor, c, info, strings.NewReader(`{}`))
				}
				if resp != nil {
					resp.Body.Close()
				}
				mu.Lock()
				defer mu.Unlock()
				if rule == "fixed" || rule == "explicit-passthrough" {
					require.Error(t, err)
					assert.Empty(t, keys)
					assert.Zero(t, accountpool.State(c).Sends)
				} else {
					require.NoError(t, err)
					assert.Equal(t, []string{"account-A"}, keys, "wildcard and regex must exclude client credentials")
				}
			})
		}
	}
}

func TestRelayAccountPoolNativeCacheRefresh(t *testing.T) {
	testRelayAccountPoolHTTP(t, "cache-legacy", "failover")
}

func TestRelayTaskAccountPoolRotatesOn429WithoutDisablingChannel(t *testing.T) {
	gin.SetMode(gin.TestMode)
	savedDB, savedLogDB := model.DB, model.LOG_DB
	savedRedis, savedBatch := common.RedisEnabled, common.BatchUpdateEnabled
	savedMemory := common.MemoryCacheEnabled
	savedMain, savedLog := common.MainDatabaseType(), common.LogDatabaseType()
	savedRetries, savedDisable, savedErrors := common.RetryTimes, common.AutomaticDisableChannelEnabled, constant.ErrorLogEnabled
	savedPrices := ratio_setting.ModelPrice2JSONString()
	savedGroups := ratio_setting.GroupRatio2JSONString()

	db := setupTieredRelayBillingDB(t)
	require.NoError(t, db.AutoMigrate(&model.Channel{}, &model.Ability{}, &model.Task{}, &model.UpstreamAccountPool{}, &model.UpstreamAccountMember{}, &model.UpstreamAccountState{}, &model.Log{}))
	model.DB, model.LOG_DB = db, db
	common.SetDatabaseTypes(common.DatabaseTypeSQLite, common.DatabaseTypeSQLite)
	common.RedisEnabled, common.BatchUpdateEnabled = false, false
	common.MemoryCacheEnabled = false
	common.RetryTimes, common.AutomaticDisableChannelEnabled, constant.ErrorLogEnabled = 1, true, false
	require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(`{"task-pool-model":0}`))
	require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(`{"default":1}`))
	t.Cleanup(func() {
		require.Eventually(t, func() bool { return gopool.WorkerCount() == 0 }, 2*time.Second, time.Millisecond)
		model.DB, model.LOG_DB = savedDB, savedLogDB
		common.RedisEnabled, common.BatchUpdateEnabled = savedRedis, savedBatch
		common.MemoryCacheEnabled = savedMemory
		common.SetDatabaseTypes(savedMain, savedLog)
		common.RetryTimes, common.AutomaticDisableChannelEnabled, constant.ErrorLogEnabled = savedRetries, savedDisable, savedErrors
		require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(savedPrices))
		require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(savedGroups))
	})

	createTieredRelayWalletFixtures(t, db, 930, 931, 100000, 100000, "task-pool-token")
	autoBan := 1
	ch := model.Channel{
		Id: 932, Name: "task-pool-suno", Type: constant.ChannelTypeSunoAPI,
		Key: "task-key-A\ntask-key-B", Models: "task-pool-model", Group: "default",
		Status: common.ChannelStatusEnabled, AutoBan: &autoBan,
		ChannelInfo: model.ChannelInfo{IsMultiKey: true, MultiKeyMode: constant.MultiKeyModePolling},
	}
	require.NoError(t, db.Create(&ch).Error)
	require.NoError(t, db.Create(&model.Ability{ChannelId: ch.Id, Group: "default", Model: "task-pool-model", Enabled: true}).Error)

	var mu sync.Mutex
	sends := []string{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		mu.Lock()
		sends = append(sends, auth)
		mu.Unlock()
		if auth == "Bearer task-key-A" {
			w.Header().Set("Retry-After", "120")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"code":"rate_limit","message":"limited"}}`))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"code":"success","data":"job-from-B"}`))
	}))
	defer upstream.Close()
	require.NoError(t, db.Model(&model.Channel{}).Where("id = ?", ch.Id).Update("base_url", upstream.URL).Error)
	ch.BaseURL = &upstream.URL

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/suno/submit/LYRICS", strings.NewReader(`{"prompt":"hello"}`))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Params = gin.Params{{Key: "action", Value: constant.SunoActionLyrics}}
	c.Set("platform", string(constant.TaskPlatformSuno))
	common.SetContextKey(c, constant.ContextKeyUserId, 930)
	common.SetContextKey(c, constant.ContextKeyTokenId, 931)
	common.SetContextKey(c, constant.ContextKeyTokenKey, "task-pool-token")
	common.SetContextKey(c, constant.ContextKeyUserGroup, "default")
	common.SetContextKey(c, constant.ContextKeyUsingGroup, "default")
	common.SetContextKey(c, constant.ContextKeyOriginalModel, "task-pool-model")
	common.SetContextKey(c, constant.ContextKeyUserSetting, dto.UserSetting{BillingPreference: "wallet_only"})
	require.Nil(t, middleware.SetupContextForSelectedChannel(c, &ch, "task-pool-model"))

	RelayTask(c)
	require.Eventually(t, func() bool { return gopool.WorkerCount() == 0 }, 2*time.Second, time.Millisecond)

	assert.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	assert.Equal(t, []string{"Bearer task-key-A", "Bearer task-key-B"}, sends)

	var updatedChannel model.Channel
	require.NoError(t, db.First(&updatedChannel, ch.Id).Error)
	assert.Equal(t, common.ChannelStatusEnabled, updatedChannel.Status, "single-key 429 must not disable multi-key task channel")

	var states []model.UpstreamAccountState
	require.NoError(t, db.Where("kind = ?", model.UpstreamAccountCooldown).Find(&states).Error)
	require.Len(t, states, 1)
	assert.Equal(t, "rate_limit", states[0].Reason)
}

func TestRelayLegacyRetryExcludesFailedSingleKeyChannel(t *testing.T) {
	gin.SetMode(gin.TestMode)
	savedDB, savedLogDB := model.DB, model.LOG_DB
	savedMemory, savedLog, savedRetry := common.MemoryCacheEnabled, common.LogConsumeEnabled, common.RetryTimes
	savedMain, savedLogType := common.MainDatabaseType(), common.LogDatabaseType()
	savedRatios, savedCompletion := ratio_setting.ModelRatio2JSONString(), ratio_setting.CompletionRatio2JSONString()

	db := setupTieredRelayBillingDB(t)
	require.NoError(t, db.AutoMigrate(&model.Channel{}, &model.Ability{}, &model.UpstreamAccountPool{}, &model.UpstreamAccountMember{}, &model.UpstreamAccountState{}, &model.Log{}))
	model.DB, model.LOG_DB = db, db
	common.SetDatabaseTypes(common.DatabaseTypeSQLite, common.DatabaseTypeSQLite)
	common.MemoryCacheEnabled, common.LogConsumeEnabled, common.RetryTimes = false, false, 1
	require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(`{"gpt-exclude":1}`))
	require.NoError(t, ratio_setting.UpdateCompletionRatioByJSONString(`{"gpt-exclude":1}`))
	t.Cleanup(func() {
		model.DB, model.LOG_DB = savedDB, savedLogDB
		common.MemoryCacheEnabled, common.LogConsumeEnabled, common.RetryTimes = savedMemory, savedLog, savedRetry
		common.SetDatabaseTypes(savedMain, savedLogType)
		require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(savedRatios))
		require.NoError(t, ratio_setting.UpdateCompletionRatioByJSONString(savedCompletion))
	})

	createTieredRelayWalletFixtures(t, db, 940, 941, 100000, 100000, "exclude-token")
	var mu sync.Mutex
	sends := []string{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		mu.Lock()
		sends = append(sends, auth)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if auth == "Bearer single-A" {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":{"message":"boom"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":"chatcmpl-ex","object":"chat.completion","model":"gpt-exclude","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`))
	}))
	defer upstream.Close()

	chA := model.Channel{Id: 942, Name: "single-A", Type: constant.ChannelTypeOpenAI, Key: "single-A", Models: "gpt-exclude", Group: "default", Status: common.ChannelStatusEnabled, BaseURL: &upstream.URL}
	chB := model.Channel{Id: 943, Name: "single-B", Type: constant.ChannelTypeOpenAI, Key: "single-B", Models: "gpt-exclude", Group: "default", Status: common.ChannelStatusEnabled, BaseURL: &upstream.URL}
	require.NoError(t, db.Create(&chA).Error)
	require.NoError(t, db.Create(&chB).Error)
	require.NoError(t, db.Create(&model.Ability{ChannelId: chA.Id, Group: "default", Model: "gpt-exclude", Enabled: true, Priority: common.GetPointer(int64(10)), Weight: 1}).Error)
	require.NoError(t, db.Create(&model.Ability{ChannelId: chB.Id, Group: "default", Model: "gpt-exclude", Enabled: true, Priority: common.GetPointer(int64(10)), Weight: 1}).Error)

	// Run 10 iterations starting on chA: retry must deterministically move to chB without re-picking chA.
	for i := 0; i < 10; i++ {
		sends = nil
		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gpt-exclude","messages":[{"role":"user","content":"hi"}]}`))
		c.Request.Header.Set("Content-Type", "application/json")
		common.SetContextKey(c, constant.ContextKeyUserId, 940)
		common.SetContextKey(c, constant.ContextKeyTokenId, 941)
		common.SetContextKey(c, constant.ContextKeyTokenKey, "exclude-token")
		common.SetContextKey(c, constant.ContextKeyUserGroup, "default")
		common.SetContextKey(c, constant.ContextKeyUsingGroup, "default")
		common.SetContextKey(c, constant.ContextKeyOriginalModel, "gpt-exclude")
		common.SetContextKey(c, constant.ContextKeyUserSetting, dto.UserSetting{BillingPreference: "wallet_only"})
		c.Set("relay_mode", 1)
		require.Nil(t, middleware.SetupContextForSelectedChannel(c, &chA, "gpt-exclude"))

		Relay(c, types.RelayFormatOpenAI)
		common.CleanupBodyStorage(c)
		assert.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
		assert.Equal(t, []string{"Bearer single-A", "Bearer single-B"}, sends)
	}
}
