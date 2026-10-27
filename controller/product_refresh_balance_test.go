package controller

import (
	"encoding/json"
	userkit "github.com/NeoMei/UniHub/packages/userkit-go"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/internal/platformaccount"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestRefreshAuthReadsCurrentCentralBalanceAndFailsClosed(t *testing.T) {
	old, redis, secret := model.DB, common.RedisEnabled, common.SessionSecret
	db, e := gorm.Open(sqlite.Open(t.TempDir()+"/balance.db"), &gorm.Config{})
	require.NoError(t, e)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.UserSession{}, &model.UniHubIdentityMapping{}, &model.ProductAuthLifecycle{}, &model.ProductFundingAccount{}))
	model.DB = db
	common.RedisEnabled = false
	common.SessionSecret = "refresh-balance-test-secret"
	t.Cleanup(func() { model.DB = old; common.RedisEnabled = redis; common.SessionSecret = secret })
	u := model.User{Username: "central-balance", AffCode: "central-balance", AuthVersion: 1, Status: 1, Quota: 0}
	require.NoError(t, db.Create(&u).Error)
	_, e = model.BindUniHubIdentity(platformaccount.Identity{Issuer: "https://hub.test", User: platformaccount.User{ID: "central"}}, u.Id)
	require.NoError(t, e)
	failedBalance := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/internal/v1/product-runtime/identity/"+strconv.Itoa(u.Id) {
			json.NewEncoder(w).Encode(userkit.ProductIdentity{Binding: userkit.RuntimeBinding{LocalUserID: strconv.Itoa(u.Id), UserID: "central", ProductID: "llmrouter", InstanceID: "default"}, Access: userkit.Access{Issuer: "https://hub.test", Subject: "central", User: userkit.User{ID: "central"}, ProductID: "llmrouter", InstanceID: "default", ProductRole: 1, Active: true, Permissions: []string{"product:use"}, ExpiresAt: time.Now().Add(time.Minute).Unix()}, ProductRole: 1, AuthVersion: 1})
			return
		}
		if r.URL.Path == "/internal/v1/product-runtime/users/"+strconv.Itoa(u.Id)+"/balance" {
			if failedBalance {
				w.WriteHeader(503)
				return
			}
			json.NewEncoder(w).Encode(userkit.RuntimeBalance{ProductID: "llmrouter", InstanceID: "default", AccountID: "account", Asset: userkit.RuntimeAsset, UnitVersion: userkit.RuntimeUnitVersion, Available: "500", Held: "0", Consumed: "0"})
			return
		}
		t.Errorf("unexpected endpoint %s", r.URL.Path)
		w.WriteHeader(404)
	}))
	defer srv.Close()
	t.Setenv("UNIHUB_PRODUCT_RUNTIME_ENABLED", "true")
	t.Setenv("UNIHUB_ORIGIN", srv.URL)
	t.Setenv("UNIHUB_PRODUCT_RUNTIME_TOKEN", strings.Repeat("s", 32))
	digest, e := model.ProductRuntimeScopeDigest()
	require.NoError(t, e)
	require.NoError(t, db.Create(&model.ProductFundingAccount{UserID: u.Id, AccountID: "account", State: "central", Asset: userkit.RuntimeAsset, UnitVersion: userkit.RuntimeUnitVersion, ScopeDigest: digest}).Error)
	bundle, e := service.CreateLoginSession(u.Id, "password", "", "")
	require.NoError(t, e)
	call := func(raw string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest("POST", "/api/user/auth/refresh", nil)
		c.Request.AddCookie(&http.Cookie{Name: service.RefreshCookieName, Value: raw})
		RefreshAuth(c)
		return w
	}
	first := call(bundle.RefreshToken)
	require.Equal(t, 200, first.Code, first.Body.String())
	var result struct {
		Data struct {
			User struct {
				Quota int `json:"quota"`
			} `json:"user"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(first.Body.Bytes(), &result))
	require.Equal(t, 500, result.Data.User.Quota, "refresh must never expose frozen local quota as central balance")
	var rotated *http.Cookie
	for _, c := range first.Result().Cookies() {
		if c.Name == service.RefreshCookieName {
			rotated = c
		}
	}
	require.NotNil(t, rotated)
	failedBalance = true
	failed := call(rotated.Value)
	require.Equal(t, 503, failed.Code, failed.Body.String())
	require.NotContains(t, failed.Body.String(), `"user":`)
	var retryCookie *http.Cookie
	for _, c := range failed.Result().Cookies() {
		if c.Name == service.RefreshCookieName {
			retryCookie = c
		}
	}
	require.NotNil(t, retryCookie, "balance failure must preserve already rotated cookie")
	var stored model.User
	require.NoError(t, db.First(&stored, u.Id).Error)
	require.Zero(t, stored.Quota, "DTO refresh must not recreate local spendable money")
}
