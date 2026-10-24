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
}

func testRelayAccountPoolHTTP(t *testing.T, mode, scenario string) {
	gin.SetMode(gin.TestMode)
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
	if scenario == "cooled-backup" {
		common.RetryTimes = 0
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
		for _, ref := range refs {
			selected, err := model.SelectUpstreamAccount(context.Background(), db, []model.UpstreamAccountRef{ref}, scope, nil, time.Now())
			require.NoError(t, err)
			require.NoError(t, model.RecordUpstreamAccountFailure(context.Background(), db, selected, scope, model.UpstreamAccountFailure{Kind: model.UpstreamAccountCooldown, Reason: "rate_limit", RetryAt: time.Now().Add(120 * time.Second)}, time.Now()))
		}
		require.NoError(t, db.Create(&model.Channel{Id: 941, Type: constant.ChannelTypeOpenAI, Name: "backup", Key: "account-C", BaseURL: &upstream.URL, Models: "gpt-pool", Group: "default", Status: common.ChannelStatusEnabled}).Error)
		require.NoError(t, db.Create(&model.Ability{ChannelId: 941, Group: "default", Model: "gpt-pool", Enabled: true, Priority: common.GetPointer(int64(-1))}).Error)
		if !strings.HasSuffix(mode, "legacy") {
			require.NoError(t, db.Create(&model.ClientModelTarget{Id: 942, RouteId: 931, ChannelId: 941, UpstreamModel: "gpt-pool", BillingModel: "gpt-pool", PreferredWires: `["openai_chat"]`, Enabled: true, Priority: 10}).Error)
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
		if scenario == "resources" {
			body = `{"model":"gpt-pool","messages":[{"role":"user","content":"hi"}],"file_id":"owned-by-A","max_tokens":10}`
		}
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		engine.ServeHTTP(recorder, req)
		if scenario == "failover" || scenario == "invalid" || scenario == "cooled-backup" {
			require.Equal(t, 200, recorder.Code, recorder.Body.String())
			assert.Equal(t, 1, strings.Count(recorder.Body.String(), `"chatcmpl-pool"`))
		} else {
			assert.GreaterOrEqual(t, recorder.Code, 400, recorder.Body.String())
			if scenario == "all-cooled" && request == 1 {
				assert.Equal(t, "120", recorder.Header().Get("Retry-After"))
			}
		}
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
