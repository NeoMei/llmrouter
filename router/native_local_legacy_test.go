package router

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/model_setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/bytedance/gopkg/util/gopool"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestNativeLocalLegacyChatFailsClosedUntilBillingRecoveryExists(t *testing.T) {
	enableUniHubBoundary(t)
	t.Setenv("UNIHUB_PRODUCT_RUNTIME_ENABLED", "true")
	t.Setenv("UNIHUB_ORIGIN", "http://127.0.0.1:1")
	t.Setenv("UNIHUB_PRODUCT_RUNTIME_TOKEN", strings.Repeat("s", 32))
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open(t.TempDir()+"/native.db"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.Token{}, &model.Channel{}, &model.Ability{}, &model.ClientModelRoute{}, &model.ClientModelTarget{}, &model.Log{}, &model.UserSubscription{}, &model.UniHubIdentityMapping{}, &model.ProductAuthLifecycle{}))
	require.NoError(t, model.RegisterProductFundingModels(db))
	oldDB, oldLogDB, oldType := model.DB, model.LOG_DB, common.MainDatabaseType()
	oldCache, oldRedis, oldLog, oldRetry, oldTranslate := common.MemoryCacheEnabled, common.RedisEnabled, common.LogConsumeEnabled, common.RetryTimes, common.TranslateMessage
	model.DB, model.LOG_DB = db, db
	common.SetMainDatabaseType(common.DatabaseTypeSQLite)
	t.Setenv("LOG_SQL_DSN", "")
	require.NoError(t, model.InitLogDB())
	common.MemoryCacheEnabled, common.RedisEnabled, common.LogConsumeEnabled, common.RetryTimes = false, false, true, 0
	grok := model_setting.GetGrokSettings()
	oldViolation := grok.ViolationDeductionEnabled
	grok.ViolationDeductionEnabled = false
	t.Cleanup(func() { grok.ViolationDeductionEnabled = oldViolation })
	require.NoError(t, i18n.Init())
	t.Cleanup(func() {
		require.Eventually(t, func() bool { return gopool.WorkerCount() == 0 }, 2*time.Second, time.Millisecond)
		model.DB, model.LOG_DB = oldDB, oldLogDB
		common.SetMainDatabaseType(oldType)
		common.MemoryCacheEnabled, common.RedisEnabled, common.LogConsumeEnabled, common.RetryTimes, common.TranslateMessage = oldCache, oldRedis, oldLog, oldRetry, oldTranslate
		require.NoError(t, sqlDB.Close())
	})
	ratio_setting.InitRatioSettings()
	oldRatios := ratio_setting.ModelRatio2JSONString()
	require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(`{"fixture-chat":1}`))
	t.Cleanup(func() { require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(oldRatios)) })
	user := model.User{Id: 901, Username: "native-local", AffCode: "native-local", AuthVersion: 1, Group: "default", Status: common.UserStatusEnabled, Quota: 100000, Setting: `{"billing_preference":"wallet_only"}`}
	require.NoError(t, db.Create(&user).Error)
	token := model.Token{Id: 902, UserId: 901, Key: "localfixturekey", Name: "fixture", Status: common.TokenStatusEnabled, ExpiredTime: -1, RemainQuota: 100000}
	require.NoError(t, db.Create(&token).Error)
	var sends atomic.Int32
	supplier := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sends.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"fixture","object":"chat.completion","model":"fixture-chat","choices":[{"index":0,"message":{"role":"assistant","content":"ready"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":0,"total_tokens":10}}`))
	}))
	t.Cleanup(supplier.Close)
	service.InitHttpClient()
	service.InitTokenEncoders()
	require.NoError(t, db.Create(&model.Channel{Id: 903, Type: constant.ChannelTypeOpenAI, Name: "fixture", Key: "synthetic-supplier", BaseURL: &supplier.URL, Models: "fixture-chat", Group: "default", Status: common.ChannelStatusEnabled}).Error)
	require.NoError(t, db.Create(&model.Ability{ChannelId: 903, Group: "default", Model: "fixture-chat", Enabled: true}).Error)
	engine := gin.New()
	SetRelayRouter(engine)
	send := func(private bool, ip string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"fixture-chat","messages":[{"role":"user","content":"hello"}],"max_tokens":10}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer sk-localfixturekey")
		req.Header.Set("X-Forwarded-For", "127.0.0.1")
		req.RemoteAddr = ip + ":1234"
		if private {
			req = req.WithContext(context.WithValue(req.Context(), http.LocalAddrContextKey, &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 3001}))
		}
		w := httptest.NewRecorder()
		engine.ServeHTTP(w, req)
		return w
	}
	denied := send(false, "203.0.113.9")
	require.Equal(t, http.StatusForbidden, denied.Code, denied.Body.String())
	assert.Zero(t, sends.Load())
	allowed := send(true, "203.0.113.9")
	require.Equal(t, http.StatusServiceUnavailable, allowed.Code, allowed.Body.String())
	assert.Contains(t, allowed.Body.String(), "local native billing recovery is unavailable")
	assert.Zero(t, sends.Load())
	t.Setenv("NATIVE_PUBLIC_ACCESS", "true")
	public := send(false, "203.0.113.9")
	require.Equal(t, http.StatusServiceUnavailable, public.Code, public.Body.String())
	assert.Zero(t, sends.Load())
	var userAfter model.User
	var tokenAfter model.Token
	require.NoError(t, db.First(&userAfter, 901).Error)
	require.NoError(t, db.First(&tokenAfter, 902).Error)
	assert.Equal(t, 100000, userAfter.Quota)
	assert.Equal(t, 100000, tokenAfter.RemainQuota)

	// The legacy non-runtime installation retains its original native behavior.
	t.Setenv("UNIHUB_PRODUCT_RUNTIME_ENABLED", "false")
	legacy := send(false, "203.0.113.9")
	require.Equal(t, http.StatusOK, legacy.Code, legacy.Body.String())
	assert.Contains(t, legacy.Body.String(), "ready")
	assert.EqualValues(t, 1, sends.Load())
}
