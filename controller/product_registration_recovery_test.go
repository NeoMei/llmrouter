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

func TestRegistrationResumesFundingAfterIdentityAlreadyBound(t *testing.T) {
	old, redis := model.DB, common.RedisEnabled
	db, e := gorm.Open(sqlite.Open(t.TempDir()+"/register.db"), &gorm.Config{})
	require.NoError(t, e)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.UniHubIdentityMapping{}, &model.ProductFundingAccount{}, &model.ProductCreditAward{}))
	model.DB = db
	common.RedisEnabled = false
	t.Cleanup(func() { model.DB = old; common.RedisEnabled = redis })
	t.Setenv("UNIHUB_PRODUCT_RUNTIME_ENABLED", "true")
	u := model.User{Username: "registered", AffCode: "registered", AuthVersion: 1, Status: 1}
	model.PrepareProductIdentity(&u)
	require.True(t, u.ProductRegistrationPending)
	require.NoError(t, db.Create(&u).Error)
	bound, e := model.BindUniHubIdentity(platformaccount.Identity{Issuer: "https://hub.test", User: platformaccount.User{ID: "central"}}, u.Id)
	require.NoError(t, e)
	require.True(t, bound.ProductRegistrationPending)
	unavailable := true
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if unavailable {
			w.WriteHeader(503)
			return
		}
		if r.URL.Path != "/internal/v1/product-runtime/users/"+strconv.Itoa(u.Id)+"/access" {
			t.Errorf("unexpected registration replay path %s", r.URL.Path)
			w.WriteHeader(404)
			return
		}
		json.NewEncoder(w).Encode(userkit.RuntimeAccess{Binding: userkit.RuntimeBinding{ID: "binding", LocalUserID: strconv.Itoa(u.Id), UserID: "central", PersonalAccountID: "account", ProductID: "llmrouter", InstanceID: "default"}, Access: userkit.Access{Issuer: "https://hub.test", Subject: "central", User: userkit.User{ID: "central"}, ProductID: "llmrouter", InstanceID: "default", Active: true, Permissions: []string{"product:use"}, ExpiresAt: time.Now().Add(time.Minute).Unix()}})
	}))
	defer srv.Close()
	t.Setenv("UNIHUB_ORIGIN", srv.URL)
	t.Setenv("UNIHUB_PRODUCT_RUNTIME_TOKEN", strings.Repeat("s", 32))
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("GET", "/api/user/self", nil)
	require.Error(t, completePendingProductIdentity(c, bound))
	var stored model.User
	require.NoError(t, db.First(&stored, u.Id).Error)
	require.True(t, stored.ProductRegistrationPending)
	unavailable = false
	require.NoError(t, completePendingProductIdentity(c, &stored))
	require.NoError(t, db.First(&stored, u.Id).Error)
	require.False(t, stored.ProductRegistrationPending)
	var funding model.ProductFundingAccount
	require.NoError(t, db.First(&funding, "user_id = ?", u.Id).Error)
	require.Equal(t, "central", funding.State)
	legacy := model.User{Username: "legacy", AffCode: "legacy", Quota: 17, IdentitySource: model.UniHubIdentitySource, AuthVersion: 1}
	require.NoError(t, db.Create(&legacy).Error)
	require.NoError(t, completePendingProductIdentity(c, &legacy))
	require.Equal(t, 17, legacy.Quota)
}

func TestManagementTargetUsesCentralRoleEvenWhenInactive(t *testing.T) {
	old := model.DB
	oldRedis := common.RedisEnabled
	common.RedisEnabled = false
	t.Cleanup(func() { common.RedisEnabled = oldRedis })
	db, e := gorm.Open(sqlite.Open(t.TempDir()+"/target.db"), &gorm.Config{})
	require.NoError(t, e)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.UniHubIdentityMapping{}, &model.ProductAuthLifecycle{}))
	model.DB = db
	t.Cleanup(func() { model.DB = old })
	u := model.User{Username: "managed-target", AffCode: "managed-target", AuthVersion: 1, Role: 1, Status: 1}
	require.NoError(t, db.Create(&u).Error)
	_, e = model.BindUniHubIdentity(platformaccount.Identity{Issuer: "https://hub.test", User: platformaccount.User{ID: "target-central"}}, u.Id)
	require.NoError(t, e)
	role := 100
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(userkit.ProductIdentity{Binding: userkit.RuntimeBinding{LocalUserID: strconv.Itoa(u.Id), UserID: "target-central", ProductID: "llmrouter", InstanceID: "default"}, Access: userkit.Access{Issuer: "https://hub.test", Subject: "target-central", User: userkit.User{ID: "target-central"}, ProductID: "llmrouter", InstanceID: "default", ProductRole: role, Active: false, ExpiresAt: time.Now().Add(time.Minute).Unix()}, ProductRole: role, AuthVersion: 1})
	}))
	defer srv.Close()
	t.Setenv("UNIHUB_PRODUCT_RUNTIME_ENABLED", "true")
	t.Setenv("UNIHUB_ORIGIN", srv.URL)
	t.Setenv("UNIHUB_PRODUCT_RUNTIME_TOKEN", strings.Repeat("s", 32))
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/api/user/manage", nil)
	require.True(t, refreshManagedTarget(c, &u))
	require.Equal(t, 100, u.Role)
	require.False(t, canManageTargetRole(10, u.Role))
	require.Equal(t, common.UserStatusDisabled, u.Status)
	role = 1
	require.True(t, refreshManagedTarget(c, &u))
	require.True(t, canManageTargetRole(10, u.Role), "inactive ordinary target must remain manageable for enable")
}

func TestRefreshReturnsRotatedCookieBeforeAuthorityFailure(t *testing.T) {
	old, redis, secret := model.DB, common.RedisEnabled, common.SessionSecret
	db, e := gorm.Open(sqlite.Open(t.TempDir()+"/refresh.db"), &gorm.Config{})
	require.NoError(t, e)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.UserSession{}, &model.UniHubIdentityMapping{}, &model.ProductAuthLifecycle{}, &model.ProductFundingAccount{}))
	model.DB = db
	common.RedisEnabled = false
	common.SessionSecret = "refresh-recovery-secret"
	t.Cleanup(func() { model.DB = old; common.RedisEnabled = redis; common.SessionSecret = secret })
	u := model.User{Username: "refresh-user", AffCode: "refresh-user", AuthVersion: 1, Status: 1}
	require.NoError(t, db.Create(&u).Error)
	_, e = model.BindUniHubIdentity(platformaccount.Identity{Issuer: "https://hub.test", User: platformaccount.User{ID: "central"}}, u.Id)
	require.NoError(t, e)
	b, e := service.CreateLoginSession(u.Id, "password", "", "")
	require.NoError(t, e)
	unavailable := true
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if unavailable {
			w.WriteHeader(503)
			return
		}
		json.NewEncoder(w).Encode(userkit.ProductIdentity{Binding: userkit.RuntimeBinding{LocalUserID: strconv.Itoa(u.Id), UserID: "central", ProductID: "llmrouter", InstanceID: "default"}, Access: userkit.Access{Issuer: "https://hub.test", Subject: "central", User: userkit.User{ID: "central"}, ProductID: "llmrouter", InstanceID: "default", ProductRole: 1, Active: true, Permissions: []string{"product:use"}, ExpiresAt: time.Now().Add(time.Minute).Unix()}, ProductRole: 1, AuthVersion: 1})
	}))
	defer srv.Close()
	t.Setenv("UNIHUB_PRODUCT_RUNTIME_ENABLED", "true")
	t.Setenv("UNIHUB_ORIGIN", srv.URL)
	t.Setenv("UNIHUB_PRODUCT_RUNTIME_TOKEN", strings.Repeat("s", 32))
	call := func(raw string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest("POST", "/api/user/auth/refresh", nil)
		c.Request.AddCookie(&http.Cookie{Name: service.RefreshCookieName, Value: raw})
		RefreshAuth(c)
		return w
	}
	first := call(b.RefreshToken)
	require.Equal(t, 503, first.Code)
	var cookie *http.Cookie
	for _, v := range first.Result().Cookies() {
		if v.Name == service.RefreshCookieName {
			cookie = v
		}
	}
	require.NotNil(t, cookie, "central failure must deliver rotated original refresh credential")
	require.NotEqual(t, b.RefreshToken, cookie.Value)
	require.NoError(t, db.Model(&model.UserSession{}).Where("sid = ?", b.Session.SID).Update("previous_valid_until", time.Now().Add(-time.Minute).Unix()).Error)
	unavailable = false
	retry := call(cookie.Value)
	require.Equal(t, 200, retry.Code, retry.Body.String())
	current, e := model.GetUserSessionBySID(b.Session.SID)
	require.NoError(t, e)
	require.Equal(t, model.UserSessionStatusActive, current.Status)
}
