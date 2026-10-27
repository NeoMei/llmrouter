package router

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/controller"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/internal/platformaccount"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"time"
)

// The companion UniHub test owns the real HTTP authority, PostgreSQL schema and
// Formance ledger. This testcase never starts a replacement central stub.
func TestCrossProductRuntimeAcceptance(t *testing.T) {
	origin := os.Getenv("UNIHUB_CROSS_PRODUCT_ORIGIN")
	if origin == "" {
		t.Skip("run through UniHub TestCrossRepositoryProductBrowserAndOriginalBilling")
	}
	oldDB, oldLog, oldRedis, oldSecret := model.DB, model.LOG_DB, common.RedisEnabled, common.SessionSecret
	oldQuota, oldRegister, oldPassword, oldDefaultKey := common.QuotaForNewUser, common.RegisterEnabled, common.PasswordLoginEnabled, constant.GenerateDefaultToken
	oldPasswordRegister, oldEmailVerify, oldMode := common.PasswordRegisterEnabled, common.EmailVerificationEnabled, gin.Mode()
	common.PasswordRegisterEnabled = true
	common.EmailVerificationEnabled = false
	gin.SetMode(gin.TestMode)
	common.RedisEnabled = false
	common.SessionSecret = "cross-product-test-session-signing-secret"
	common.QuotaForNewUser = 500
	common.RegisterEnabled = true
	common.PasswordLoginEnabled = true
	constant.GenerateDefaultToken = false
	t.Cleanup(func() {
		model.DB, model.LOG_DB, common.RedisEnabled, common.SessionSecret = oldDB, oldLog, oldRedis, oldSecret
		common.QuotaForNewUser, common.RegisterEnabled, common.PasswordLoginEnabled, constant.GenerateDefaultToken = oldQuota, oldRegister, oldPassword, oldDefaultKey
		common.PasswordRegisterEnabled, common.EmailVerificationEnabled = oldPasswordRegister, oldEmailVerify
		gin.SetMode(oldMode)
	})
	t.Setenv("UNIHUB_PRODUCT_RUNTIME_ENABLED", "true")
	t.Setenv("UNIHUB_ORIGIN", origin)
	t.Setenv("UNIHUB_PRODUCT_INSTANCE_ID", "default")
	servers := map[*gin.Engine]*httptest.Server{}
	activate := func(product, instance, client, host, token string) (*gin.Engine, *gorm.DB) {
		db, e := gorm.Open(sqlite.Open(t.TempDir()+"/"+product+"-"+instance+".db"), &gorm.Config{Logger: logger.Discard})
		require.NoError(t, e)
		sqlDB, e := db.DB()
		require.NoError(t, e)
		t.Cleanup(func() { _ = sqlDB.Close() })
		require.NoError(t, db.AutoMigrate(&model.User{}, &model.Token{}, &model.UserSession{}, &model.AuthFlow{}, &model.UniHubIdentityMapping{}, &model.ProductAuthLifecycle{}, &model.ProductCreditAward{}, &model.TwoFA{}, &model.Log{}, &model.UserSubscription{}, &model.SubscriptionPlan{}, &model.SubscriptionPreConsumeRecord{}))
		require.NoError(t, model.RegisterProductFundingModels(db))
		require.NoError(t, model.RegisterProductRefundModels(db))
		model.DB, model.LOG_DB = db, db
		t.Setenv("UNIHUB_PRODUCT_ID", product)
		t.Setenv("UNIHUB_PRODUCT_INSTANCE_ID", instance)
		t.Setenv("UNIHUB_PRODUCT_RUNTIME_TOKEN", token)
		t.Setenv("UNIHUB_PRODUCT_CLIENT_ID", client)
		t.Setenv("UNIHUB_PRODUCT_REDIRECT_URI", "https://"+host+"/api/unihub/callback")
		r := gin.New()
		r.POST("/api/user/register", controller.Register)
		r.POST("/api/user/login", controller.Login)
		r.POST("/api/user/auth/refresh", controller.RefreshAuth)
		r.GET("/api/user/self", middleware.UserAuth(), controller.GetSelf)
		r.POST("/api/token/", middleware.UserAuth(), controller.AddToken)
		registerUniHubSSO(r, platformaccount.Config{Enabled: true, ClientID: client, PublicOrigin: origin, RedirectURI: "https://" + host + "/api/unihub/callback"})
		server := httptest.NewTLSServer(r)
		server.Client().Timeout = 5 * time.Second
		server.Client().CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		servers[r] = server
		t.Cleanup(server.Close)
		return r, db
	}
	request := func(r *gin.Engine, method, raw string, body any, bearer string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
		t.Helper()
		data, _ := json.Marshal(body)
		original, e := url.Parse(raw)
		require.NoError(t, e)
		req, e := http.NewRequest(method, servers[r].URL+original.RequestURI(), bytes.NewReader(data))
		require.NoError(t, e)
		req.Host = original.Host
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Sec-Fetch-Site", "same-origin")
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		for _, c := range cookies {
			req.AddCookie(c)
		}
		res, e := servers[r].Client().Do(req)
		require.NoError(t, e)
		defer res.Body.Close()
		w := httptest.NewRecorder()
		for name, values := range res.Header {
			for _, v := range values {
				w.Header().Add(name, v)
			}
		}
		w.WriteHeader(res.StatusCode)
		_, e = io.Copy(w, res.Body)
		require.NoError(t, e)
		return w
	}
	check := func(w *httptest.ResponseRecorder, want int) {
		t.Helper()
		require.Equal(t, want, w.Code, w.Body.String())
	}
	cookie := func(cookies []*http.Cookie, name string) *http.Cookie {
		t.Helper()
		for _, c := range cookies {
			if c.Name == name {
				return c
			}
		}
		t.Fatalf("missing browser cookie %s", name)
		return nil
	}
	client := &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	centralGet := func(raw string, cookies ...*http.Cookie) *http.Response {
		t.Helper()
		req, e := http.NewRequest("GET", raw, nil)
		require.NoError(t, e)
		for _, c := range cookies {
			req.AddCookie(c)
		}
		res, e := client.Do(req)
		require.NoError(t, e)
		return res
	}
	location := func(res *http.Response, want int) string {
		t.Helper()
		defer res.Body.Close()
		raw, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
		require.Equal(t, want, res.StatusCode, string(raw))
		require.NotEmpty(t, res.Header.Get("Location"))
		return res.Header.Get("Location")
	}
	source, sourceDB := activate("llmrouter", "default", "router", "router.test", strings.Repeat("a", 32))
	registered := request(source, "POST", "https://router.test/api/user/register", map[string]string{"username": "original-user", "password": "TestPassword123!"}, "")
	check(registered, 200)
	var reg struct {
		Success bool `json:"success"`
	}
	require.NoError(t, json.Unmarshal(registered.Body.Bytes(), &reg))
	require.True(t, reg.Success, registered.Body.String())
	login := request(source, "POST", "https://router.test/api/user/login", map[string]string{"username": "original-user", "password": "TestPassword123!"}, "")
	check(login, 200)
	var signed struct {
		Success bool `json:"success"`
		Data    struct {
			Access string `json:"access_token"`
			User   struct {
				ID    int `json:"id"`
				Quota int `json:"quota"`
			} `json:"user"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(login.Body.Bytes(), &signed))
	require.True(t, signed.Success, login.Body.String())
	require.NotEmpty(t, signed.Data.Access)
	require.Equal(t, 500, signed.Data.User.Quota)
	refresh := cookie(login.Result().Cookies(), service.RefreshCookieName)
	handoff := request(source, "GET", "https://router.test/api/user/auth/unihub-handoff?return_to=%2Fkeys", nil, "", refresh)
	check(handoff, 303)
	start := centralGet(handoff.Header().Get("Location"))
	centralBinding := cookie(start.Cookies(), "unihub_product_handoff")
	callback := location(start, 303)
	approval := request(source, "GET", callback, nil, "", cookie(handoff.Result().Cookies(), productHandoffCookie))
	check(approval, 303)
	finish := centralGet(approval.Header().Get("Location"), centralBinding)
	centralSession := cookie(finish.Cookies(), "unihub_session")
	doneURL := location(finish, 303)
	done := request(source, "GET", doneURL, nil, "", cookie(handoff.Result().Cookies(), productHandoffCookie))
	check(done, 303)
	require.Equal(t, "/keys", done.Header().Get("Location"))
	var sourceMap model.UniHubIdentityMapping
	require.NoError(t, sourceDB.Where("user_id = ?", signed.Data.User.ID).First(&sourceMap).Error)
	constant.GenerateDefaultToken = true
	second, secondDB := activate("llmrouter", "second", "second", "second.test", strings.Repeat("b", 32))
	begin := request(second, "GET", "https://second.test/api/unihub/login?prompt=none&return_to=%2Fkeys", nil, "")
	check(begin, 303)
	authorized := centralGet(begin.Header().Get("Location"), centralSession)
	secondCallback := location(authorized, 303)
	completed := request(second, "GET", secondCallback, nil, "", cookie(begin.Result().Cookies(), productBrokerCookie))
	check(completed, 303)
	require.Equal(t, "/keys", completed.Header().Get("Location"))
	secondRefresh := cookie(completed.Result().Cookies(), service.RefreshCookieName)
	refreshed := request(second, "POST", "https://second.test/api/user/auth/refresh", nil, "", secondRefresh)
	check(refreshed, 200)
	var secondSession struct {
		Success bool `json:"success"`
		Data    struct {
			Access string `json:"access_token"`
			User   struct {
				ID      int    `json:"id"`
				Quota   int    `json:"quota"`
				Setting string `json:"setting"`
			} `json:"user"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(refreshed.Body.Bytes(), &secondSession))
	require.True(t, secondSession.Success, refreshed.Body.String())
	require.NotEmpty(t, secondSession.Data.Access)
	require.Equal(t, 500, secondSession.Data.User.Quota)
	require.NotEmpty(t, secondSession.Data.User.Setting)
	var initialKeys int64
	require.NoError(t, secondDB.Model(&model.Token{}).Where("user_id = ?", secondSession.Data.User.ID).Count(&initialKeys).Error)
	require.EqualValues(t, 1, initialKeys, "JIT keeps the original configured default key")
	var secondMap model.UniHubIdentityMapping
	require.NoError(t, secondDB.Where("user_id = ?", secondSession.Data.User.ID).First(&secondMap).Error)
	require.Equal(t, sourceMap.Subject, secondMap.Subject)
	created := request(second, "POST", "https://second.test/api/token/", map[string]any{"name": "original-key", "expired_time": -1, "remain_quota": 200, "model_limits_enabled": true, "model_limits": "original-model", "group": "default", "cross_group_retry": false}, secondSession.Data.Access)
	check(created, 200)
	var keyReply struct {
		Success bool `json:"success"`
	}
	require.NoError(t, json.Unmarshal(created.Body.Bytes(), &keyReply))
	require.True(t, keyReply.Success, created.Body.String())
	var key model.Token
	require.NoError(t, secondDB.Where("user_id = ? AND name = ?", secondSession.Data.User.ID, "original-key").First(&key).Error)
	require.True(t, key.ModelLimitsEnabled)
	require.Equal(t, "original-model", key.ModelLimits)
	require.Equal(t, "default", key.Group)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "https://second.test/v1/chat/completions", nil)
	info := &relaycommon.RelayInfo{RequestId: "cross-product-original-billing", UserId: secondSession.Data.User.ID, TokenId: key.Id, TokenKey: key.Key, OriginModelName: "original-model", UsingGroup: "default", UserGroup: "default", UserSetting: dto.UserSetting{BillingPreference: "wallet_only"}, ForcePreConsume: true}
	require.Nil(t, service.PreConsumeBilling(c, 60, info))
	require.Equal(t, "unihub", info.BillingSource)
	require.NoError(t, model.MarkProductFundingDispatched(context.Background(), info.RequestId))
	require.NoError(t, info.Billing.Settle(37))
	require.NoError(t, info.Billing.Settle(37))
	require.NoError(t, secondDB.First(&key, key.Id).Error)
	require.Equal(t, 163, key.RemainQuota)
	require.Equal(t, 37, key.UsedQuota)
	var final model.User
	require.NoError(t, secondDB.First(&final, secondSession.Data.User.ID).Error)
	require.Zero(t, final.Quota)
	require.Equal(t, 37, final.UsedQuota)
	require.Equal(t, 1, final.RequestCount)
	runtime, e := platformaccount.RuntimeClient()
	require.NoError(t, e)
	access, e := runtime.Access(context.Background(), strconv.Itoa(final.Id))
	require.NoError(t, e)
	balance, e := runtime.Balance(context.Background(), strconv.Itoa(final.Id), access.Binding.PersonalAccountID)
	require.NoError(t, e)
	require.Equal(t, "463", balance.Available)
	require.Equal(t, "37", balance.Consumed)
	self := request(second, "GET", "https://second.test/api/user/self", nil, secondSession.Data.Access)
	check(self, 200)
	var selfReply struct {
		Success bool `json:"success"`
		Data    struct {
			Quota        int `json:"quota"`
			UsedQuota    int `json:"used_quota"`
			RequestCount int `json:"request_count"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(self.Body.Bytes(), &selfReply))
	require.True(t, selfReply.Success, self.Body.String())
	require.Equal(t, 463, selfReply.Data.Quota)
	require.Equal(t, 37, selfReply.Data.UsedQuota)
	require.Equal(t, 1, selfReply.Data.RequestCount)
	// Entering again reuses the product user and its spent balance/key resources.
	reentry := request(second, "GET", "https://second.test/api/unihub/login?prompt=none&return_to=%2Fkeys", nil, "")
	check(reentry, 303)
	reauthorized := centralGet(reentry.Header().Get("Location"), centralSession)
	reentered := request(second, "GET", location(reauthorized, 303), nil, "", cookie(reentry.Result().Cookies(), productBrokerCookie))
	check(reentered, 303)
	require.Equal(t, "/keys", reentered.Header().Get("Location"))
	var users, keys int64
	require.NoError(t, secondDB.Model(&model.User{}).Count(&users).Error)
	require.EqualValues(t, 1, users)
	require.NoError(t, secondDB.Model(&model.Token{}).Where("user_id = ?", final.Id).Count(&keys).Error)
	require.EqualValues(t, 2, keys)
	balance, e = runtime.Balance(context.Background(), strconv.Itoa(final.Id), access.Binding.PersonalAccountID)
	require.NoError(t, e)
	require.Equal(t, "463", balance.Available)
	// Revocation blocks new work but must not erase a previously accepted cost.
	require.NoError(t, secondDB.Model(&key).Update("status", common.TokenStatusDisabled).Error)
	require.NoError(t, secondDB.Delete(&key).Error)
	rejectNewCall := func(id string) {
		fresh := &relaycommon.RelayInfo{RequestId: id, UserId: final.Id, TokenId: key.Id, TokenKey: key.Key, OriginModelName: "original-model", UsingGroup: "default", UserGroup: "default", UserSetting: dto.UserSetting{BillingPreference: "wallet_only"}, ForcePreConsume: true}
		require.NotNil(t, service.PreConsumeBilling(c, 10, fresh), "a revoked/deleted key cannot fund new work")
		var admitted int64
		require.NoError(t, secondDB.Model(&model.ProductRuntimeCall{}).Where("id = ?", id).Count(&admitted).Error)
		require.Zero(t, admitted)
	}
	rejectNewCall("cross-product-new-call-after-key-revocation")
	for i := 0; i < 2; i++ {
		require.NoError(t, model.RepriceProductCall(context.Background(), info.RequestId, "final-accepted-cost", 47, "verified final cost"))
	}
	require.NoError(t, secondDB.Unscoped().First(&key, key.Id).Error)
	require.Equal(t, 153, key.RemainQuota)
	require.Equal(t, 47, key.UsedQuota)
	require.Equal(t, common.TokenStatusDisabled, key.Status)
	require.True(t, key.DeletedAt.Valid)
	require.NoError(t, secondDB.First(&final, final.Id).Error)
	require.Zero(t, final.Quota)
	require.Equal(t, 47, final.UsedQuota)
	require.Equal(t, 1, final.RequestCount)
	balance, e = runtime.Balance(context.Background(), strconv.Itoa(final.Id), access.Binding.PersonalAccountID)
	require.NoError(t, e)
	require.Equal(t, "453", balance.Available)
	require.Equal(t, "47", balance.Consumed)
	rejectNewCall("cross-product-new-call-after-accepted-adjustment")
	t.Log("original registration/password -> central browser -> second instance JIT; original settlement 37 then accepted-cost adjustment 10 after key revocation; final remaining 453")
}
