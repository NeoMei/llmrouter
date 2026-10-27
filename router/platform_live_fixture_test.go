package router

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/signal"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/internal/platformaccount"
	"github.com/QuantumNous/new-api/internal/platformbilling"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/joho/godotenv"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Explicit opt-in local acceptance harness. UniHub and its ledger remain real;
// only the synthetic OpenAI provider is simulated. Never reads provider stores.
func TestPlatformLocalLiveFixture(t *testing.T) {
	if os.Getenv("UNIHUB_PLATFORM_E2E") != "1" {
		t.Skip("opt-in local UniHub acceptance fixture")
	}
	enableUniHubBoundary(t)
	envFile := os.Getenv("UNIHUB_PLATFORM_E2E_ENV_FILE")
	require.True(t, filepath.IsAbs(envFile), "UNIHUB_PLATFORM_E2E_ENV_FILE must explicitly name an absolute local env file")
	values, e := godotenv.Read(envFile)
	require.NoError(t, e)
	token := values["MODEL_SERVICE_TOKEN"]
	require.GreaterOrEqual(t, len(token), 32, "MODEL_SERVICE_TOKEN must exist in local UniHub env")
	gin.SetMode(gin.ReleaseMode)
	require.NoError(t, i18n.Init())
	constant.StreamingTimeout = 30
	dbPath := os.Getenv("UNIHUB_PLATFORM_E2E_DATABASE")
	if dbPath == "" {
		dbPath = filepath.Join(t.TempDir(), "gateway.db")
	}
	require.True(t, filepath.IsAbs(dbPath))
	db, e := gorm.Open(sqlite.Open("file:"+dbPath), &gorm.Config{})
	require.NoError(t, e)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.Token{}, &model.Channel{}, &model.Ability{}, &model.UserSubscription{}, &model.ClientModelRoute{}, &model.ClientModelTarget{}))
	model.DB = db
	model.LOG_DB = db
	common.MemoryCacheEnabled = true
	common.RedisEnabled = false
	common.RetryTimes = 1
	var upstreamCalls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls.Add(1)
		var body struct {
			Stream bool `json:"stream"`
		}
		if common.DecodeJson(r.Body, &body) != nil {
			w.WriteHeader(400)
			return
		}
		if body.Stream {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("data: {\"id\":\"synthetic\",\"object\":\"chat.completion.chunk\",\"model\":\"unihub-synthetic-model\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"synthetic local provider\"},\"finish_reason\":null}]}\n\ndata: {\"id\":\"synthetic\",\"object\":\"chat.completion.chunk\",\"model\":\"unihub-synthetic-model\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: {\"id\":\"synthetic\",\"object\":\"chat.completion.chunk\",\"model\":\"unihub-synthetic-model\",\"choices\":[],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":5,\"total_tokens\":15,\"prompt_tokens_details\":{\"cached_tokens\":2},\"completion_tokens_details\":{\"reasoning_tokens\":1}}}\n\ndata: [DONE]\n\n"))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"synthetic","object":"chat.completion","model":"unihub-synthetic-model","choices":[{"index":0,"message":{"role":"assistant","content":"synthetic local provider"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15,"prompt_tokens_details":{"cached_tokens":2},"completion_tokens_details":{"reasoning_tokens":1}}}`))
	}))
	defer upstream.Close()
	require.NoError(t, db.Clauses(clause.OnConflict{DoNothing: true}).Create(&model.User{Id: 1, Username: "isolated-non-financial-principal", AffCode: "fixture-principal", Status: common.UserStatusEnabled, Group: "platform", Quota: 0}).Error)
	require.NoError(t, db.Clauses(clause.OnConflict{DoNothing: true}).Create(&model.User{Id: 2, Username: "isolated-native-compatibility-marker", AffCode: "fixture-native", Status: common.UserStatusEnabled, Group: "default", Quota: 12345, UsedQuota: 7, RequestCount: 3}).Error)
	require.NoError(t, db.Clauses(clause.OnConflict{DoNothing: true}).Create(&model.Token{Id: 1, UserId: 2, Key: "fixtureNativePreserved", Status: common.TokenStatusEnabled, RemainQuota: 54321, UsedQuota: 11, ExpiredTime: -1}).Error)
	base := upstream.URL
	require.NoError(t, db.Save(&model.Channel{Id: 1, Type: constant.ChannelTypeOpenAI, Key: "synthetic-not-a-provider-key", Status: common.ChannelStatusEnabled, Name: "isolated synthetic channel", BaseURL: &base, Models: "unihub-synthetic-model", Group: "platform"}).Error)
	require.NoError(t, db.Save(&model.Ability{Group: "platform", Model: "unihub-synthetic-model", ChannelId: 1, Enabled: true}).Error)
	require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(`{"unihub-synthetic-model":0}`))
	require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(`{"platform":1}`))
	model.InitChannelCache()
	service.InitHttpClient()
	service.InitTokenEncoders()
	origin := os.Getenv("UNIHUB_PLATFORM_E2E_ORIGIN")
	if origin == "" {
		origin = "http://localhost:8088"
	}
	config := platformbilling.Config{Enabled: true, Origin: origin, ServiceToken: token, InstanceID: os.Getenv("UNIHUB_PRODUCT_INSTANCE_ID"), PrincipalID: 1, Group: "platform", MaxBodyBytes: 32768, MaxInputTokens: 65536, MaxOutputTokens: 8192, RateIDs: map[string]string{"unihub-synthetic-model": "local-e2e-chat-v1"}}
	if raw := os.Getenv("UNIHUB_PLATFORM_E2E_ASSET_RATES"); raw != "" {
		require.NoError(t, common.UnmarshalJsonStr(raw, &config.AssetRateIDs))
	}
	adapter, e := platformbilling.New(db, config)
	require.NoError(t, e)
	engine := gin.New()
	engine.Use(func(c *gin.Context) {
		if c.Request.Method != http.MethodPost || c.Request.URL.Path != "/platform/v1/chat/completions" {
			c.Next()
			return
		}
		reader := &platformFixtureCountReader{ReadCloser: c.Request.Body}
		c.Request.Body = reader
		writer := &platformFixtureResponseWriter{ResponseWriter: c.Writer}
		c.Writer = writer
		c.Next()
		var envelope struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		_ = common.Unmarshal(writer.errorBody.Bytes(), &envelope)
		code := envelope.Error.Code
		switch code {
		case "invalid_model_credential", "request_too_large", "stable_call_and_task_required", "platform_billing_unavailable", "logical_call_already_exists", "logical_call_conflict":
		default:
			code = "none_or_unrecognized"
		}
		t.Logf("platform fixture request: declared_bytes=%d read_bytes=%d status=%d error_code=%s", c.Request.ContentLength, reader.count, writer.Status(), code)
	})
	engine.Use(gin.Recovery(), middlewareBodyCleanupForTest())
	SetManagedKeyDispatch(engine, adapter)
	SetPlatformRouter(engine, adapter)
	if clientID := os.Getenv("UNIHUB_PLATFORM_E2E_PRODUCT_CLIENT_ID"); clientID != "" {
		require.NoError(t, db.AutoMigrate(&model.UserSession{}, &model.UniHubIdentityMapping{}, &model.UniHubSessionBinding{}, &model.Log{}, &model.QuotaData{}))
		publicOrigin := os.Getenv("UNIHUB_PLATFORM_E2E_PUBLIC_ORIGIN")
		require.NotEmpty(t, publicOrigin, "canonical UniHub issuer required for product-session acceptance")
		redirectURI := os.Getenv("UNIHUB_PLATFORM_E2E_REDIRECT_URI")
		require.NotEmpty(t, redirectURI, "registered product redirect URI required for product-session acceptance")
		SetUniHubRouter(engine, platformaccount.Config{Enabled: true, APIOrigin: origin, PublicOrigin: publicOrigin, ClientID: clientID, ProductID: "llmrouter", InstanceID: config.InstanceID, RedirectURI: redirectURI})
		SetApiRouter(engine)
	}
	// Only the explicit opt-in fixture exposes synthetic acceptance counters.
	engine.GET("/__fixture/status", func(c *gin.Context) {
		var user model.User
		require.NoError(t, db.Select("quota", "used_quota").First(&user, 1).Error)
		var native model.User
		var token model.Token
		require.NoError(t, db.Select("quota", "used_quota", "request_count").First(&native, 2).Error)
		require.NoError(t, db.Select("remain_quota", "used_quota").First(&token, 1).Error)
		c.JSON(200, gin.H{"provider_calls": upstreamCalls.Load(), "quota": user.Quota, "used_quota": user.UsedQuota, "native_quota": native.Quota, "native_used_quota": native.UsedQuota, "native_requests": native.RequestCount, "native_token_quota": token.RemainQuota, "native_token_used_quota": token.UsedQuota})
	})
	bind := os.Getenv("UNIHUB_PLATFORM_E2E_LISTEN")
	if bind == "" {
		bind = "127.0.0.1:3001"
	}
	listener, e := net.Listen("tcp", bind)
	require.NoError(t, e)
	require.True(t, listener.Addr().(*net.TCPAddr).IP.IsLoopback())
	server := &http.Server{Handler: engine, ReadHeaderTimeout: 5 * time.Second}
	defer server.Close()
	go func() { _ = server.Serve(listener) }()
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if os.Getenv("UNIHUB_PLATFORM_E2E_RECOVERY") != "false" {
		stop := adapter.Start(ctx)
		defer stop()
	}
	if ready := os.Getenv("UNIHUB_PLATFORM_E2E_READY"); ready != "" {
		require.True(t, filepath.IsAbs(ready))
		require.NoError(t, os.WriteFile(ready, []byte("http://"+listener.Addr().String()), 0600))
	}
	t.Log("isolated synthetic gateway ready; actual UniHub HTTP; auto-stop 15 minutes")
	select {
	case <-ctx.Done():
	case <-time.After(15 * time.Minute):
	}
}

type platformFixtureCountReader struct {
	io.ReadCloser
	count int
}

func (r *platformFixtureCountReader) Read(p []byte) (int, error) {
	n, err := r.ReadCloser.Read(p)
	r.count += n
	return n, err
}

type platformFixtureResponseWriter struct {
	gin.ResponseWriter
	errorBody bytes.Buffer
}

func (w *platformFixtureResponseWriter) Write(p []byte) (int, error) {
	if w.Status() >= 400 && w.errorBody.Len()+len(p) <= 4096 {
		_, _ = w.errorBody.Write(p)
	}
	return w.ResponseWriter.Write(p)
}
