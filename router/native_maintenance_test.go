package router

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/internal/platformaccount"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPrivateNativeMaintenanceLimitsScope(t *testing.T) {
	f := newUniHubRegressionFixture(t)
	model.InitDatabaseColumns()
	t.Setenv("UNIHUB_PRODUCT_RUNTIME_ENABLED", "true")
	t.Setenv("NATIVE_PRIVATE_MAINTENANCE_ENABLED", "true")
	require.NoError(t, f.db.AutoMigrate(&model.ProductFundingAccount{}, &model.ProductAuthLifecycle{}, &model.Token{}, &model.TwoFA{}, &model.Log{}))
	previousLogDB := model.LOG_DB
	model.LOG_DB = f.db
	t.Cleanup(func() { model.LOG_DB = previousLogDB })
	password, err := common.Password2Hash("test-pass-123")
	require.NoError(t, err)
	user := model.User{Username: "local-maintainer", Password: password, AffCode: "local-maintainer", AuthVersion: 1, Status: common.UserStatusEnabled, Role: common.RoleAdminUser, Quota: 42}
	require.NoError(t, f.db.Create(&user).Error)
	token := model.Token{UserId: user.Id, Key: "maintenancefixturekey1234", Name: "existing-key", Status: common.TokenStatusEnabled, UnlimitedQuota: true, ExpiredTime: -1}
	require.NoError(t, f.db.Create(&token).Error)
	other := model.User{Username: "other-maintainer", AffCode: "other-maintainer", AuthVersion: 1, Status: common.UserStatusEnabled, Role: common.RoleCommonUser}
	require.NoError(t, f.db.Create(&other).Error)
	otherToken := model.Token{UserId: other.Id, Key: "otherfixturekey123456789", Name: "other-private-key", Status: common.TokenStatusEnabled, UnlimitedQuota: true, ExpiredTime: -1}
	require.NoError(t, f.db.Create(&otherToken).Error)
	bundle, err := service.CreateLoginSession(user.Id, "password", "", "")
	require.NoError(t, err)
	request := func(method, path, token string, private bool, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "https://dashboard.test"+path, strings.NewReader(body))
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Forwarded-For", "127.0.0.1")
		if private {
			req = req.WithContext(context.WithValue(req.Context(), http.LocalAddrContextKey, &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 3001}))
		}
		w := httptest.NewRecorder()
		f.router.ServeHTTP(w, req)
		return w
	}
	response := request("GET", "/api/token/", bundle.AccessToken, true, "")
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	assert.Contains(t, response.Body.String(), `"success":true`)
	assert.Contains(t, response.Body.String(), "existing-key")
	assert.NotContains(t, response.Body.String(), token.Key)
	assert.NotContains(t, response.Body.String(), otherToken.Name)
	assert.NotEqual(t, http.StatusOK, request("GET", "/api/token/", "", true, "").Code)
	self := request("GET", "/api/user/self", bundle.AccessToken, true, "")
	require.Equal(t, http.StatusOK, self.Code, self.Body.String())
	assert.Contains(t, self.Body.String(), `"quota":42`)
	assert.Contains(t, self.Body.String(), `"role":10`)
	history := request("GET", "/api/log/self", bundle.AccessToken, true, "")
	// Private listener proves maintenance access but not the provenance of
	// an older session or unclassified history rows.
	require.Equal(t, http.StatusServiceUnavailable, history.Code, history.Body.String())
	assert.NotEqual(t, http.StatusOK, request("GET", "/api/token/", bundle.AccessToken, false, "").Code)
	// A search oracle over key prefixes would reveal a masked key one byte at a time.
	assert.NotEqual(t, http.StatusOK, request("GET", "/api/token/search?token=maintenance%25", bundle.AccessToken, true, "").Code)
	assert.NotEqual(t, http.StatusOK, request("POST", "/api/token/", bundle.AccessToken, true, `{}`).Code)
	assert.NotEqual(t, http.StatusOK, request("POST", "/api/token/1/key", bundle.AccessToken, true, `{}`).Code)
	assert.NotEqual(t, http.StatusOK, request("POST", "/api/user/topup", bundle.AccessToken, true, `{}`).Code)
	assert.NotEqual(t, http.StatusOK, request("GET", "/api/user/topup/self", bundle.AccessToken, true, "").Code)
	assert.NotEqual(t, http.StatusOK, request("GET", "/api/log/", bundle.AccessToken, true, "").Code)
	assert.NotEqual(t, http.StatusOK, request("GET", "/api/user/", bundle.AccessToken, true, "").Code)
	// This test-only sentinel does not dispatch; the native compatibility
	// path still requires its independently configured network boundary.
	assert.Equal(t, http.StatusNoContent, request("POST", "/v1/chat/completions", "sk-"+token.Key, true, `{}`).Code)
	badLogin := request("POST", "/api/user/login", "", true, `{"username":"local-maintainer","password":"bad"}`)
	assert.NotContains(t, badLogin.Body.String(), `"success":true`)
	login := request("POST", "/api/user/login", "", true, `{"username":"local-maintainer","password":"test-pass-123"}`)
	require.Equal(t, http.StatusOK, login.Code, login.Body.String())
	assert.Contains(t, login.Body.String(), `"success":true`)
	var refreshCookie *http.Cookie
	for _, cookie := range login.Result().Cookies() {
		if cookie.Name == service.RefreshCookieName {
			refreshCookie = cookie
		}
	}
	require.NotNil(t, refreshCookie)
	t.Setenv("UNIHUB_INDEPENDENT_CENTRAL_REALM_ENABLED", "true")
	optionsReq := httptest.NewRequest(http.MethodGet, "https://dashboard.test/api/user/auth/session-options", nil)
	optionsReq.Header.Set("Origin", "https://dashboard.test")
	optionsReq.Header.Set("X-Forwarded-For", "127.0.0.1")
	optionsReq.AddCookie(refreshCookie)
	publicOptions := httptest.NewRecorder()
	f.router.ServeHTTP(publicOptions, optionsReq)
	assert.NotContains(t, publicOptions.Body.String(), "local-maintainer", "forwarded address is not a private listener")
	optionsReq = optionsReq.WithContext(context.WithValue(optionsReq.Context(), http.LocalAddrContextKey, &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 3001}))
	privateOptions := httptest.NewRecorder()
	f.router.ServeHTTP(privateOptions, optionsReq)
	require.Contains(t, privateOptions.Body.String(), "local-maintainer", privateOptions.Body.String())
	t.Setenv("NATIVE_PRIVATE_MAINTENANCE_ENABLED", "false")
	deniedPrivate := httptest.NewRecorder()
	f.router.ServeHTTP(deniedPrivate, optionsReq)
	assert.NotContains(t, deniedPrivate.Body.String(), "local-maintainer")
	t.Setenv("NATIVE_PRIVATE_MAINTENANCE_ENABLED", "true")
	forgedRefresh := httptest.NewRequest(http.MethodPost, "https://dashboard.test/api/user/auth/refresh", nil)
	forgedRefresh.Header.Set("Origin", "https://dashboard.test")
	forgedRefresh.AddCookie(&http.Cookie{Name: service.RefreshCookieName, Value: refreshCookie.Value + "forged"})
	forgedRefresh = forgedRefresh.WithContext(context.WithValue(forgedRefresh.Context(), http.LocalAddrContextKey, &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 3001}))
	forgedResponse := httptest.NewRecorder()
	f.router.ServeHTTP(forgedResponse, forgedRefresh)
	assert.NotContains(t, forgedResponse.Body.String(), `"success":true`)
	refreshRequest := httptest.NewRequest(http.MethodPost, "https://dashboard.test/api/user/auth/refresh", nil)
	refreshRequest.Header.Set("Origin", "https://dashboard.test")
	refreshRequest.AddCookie(refreshCookie)
	refreshRequest = refreshRequest.WithContext(context.WithValue(refreshRequest.Context(), http.LocalAddrContextKey, &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 3001}))
	refreshResponse := httptest.NewRecorder()
	f.router.ServeHTTP(refreshResponse, refreshRequest)
	require.Equal(t, http.StatusOK, refreshResponse.Code, refreshResponse.Body.String())
	assert.Contains(t, refreshResponse.Body.String(), `"quota":42`)
	assert.Contains(t, refreshResponse.Body.String(), `"role":10`)
	for _, pending := range []struct {
		name  string
		set   func()
		clear func()
	}{
		{"identity_pending", func() {
			require.NoError(t, f.db.Model(&user).Update("identity_source", model.ProductIdentityPending).Error)
		}, func() { require.NoError(t, f.db.Model(&user).Update("identity_source", "").Error) }},
		{"registration_pending", func() { require.NoError(t, f.db.Model(&user).Update("product_registration_pending", true).Error) }, func() { require.NoError(t, f.db.Model(&user).Update("product_registration_pending", false).Error) }},
		{"jit_pending", func() { require.NoError(t, f.db.Model(&user).Update("product_jit_pending", true).Error) }, func() { require.NoError(t, f.db.Model(&user).Update("product_jit_pending", false).Error) }},
		{"foreign_subject", func() { require.NoError(t, f.db.Model(&user).Update("unihub_user_id", "linked").Error) }, func() { require.NoError(t, f.db.Model(&user).Update("unihub_user_id", "").Error) }},
		{"nonlocal_funding", func() {
			require.NoError(t, f.db.Create(&model.ProductFundingAccount{UserID: user.Id, State: "central"}).Error)
		}, func() { require.NoError(t, f.db.Delete(&model.ProductFundingAccount{}, "user_id = ?", user.Id).Error) }},
		{"pending_lifecycle", func() {
			require.NoError(t, f.db.Create(&model.ProductAuthLifecycle{ID: "pending", UserID: user.Id, PreviousVersion: 1, NextVersion: 2}).Error)
		}, func() { require.NoError(t, f.db.Delete(&model.ProductAuthLifecycle{}, "id = ?", "pending").Error) }},
	} {
		t.Run(pending.name, func(t *testing.T) {
			pending.set()
			defer pending.clear()
			assert.NotEqual(t, http.StatusOK, request("GET", "/api/token/", bundle.AccessToken, true, "").Code)
		})
	}
	require.NoError(t, f.db.Model(&user).Update("role", common.RoleCommonUser).Error)
	ordinarySelf := request("GET", "/api/user/self", bundle.AccessToken, true, "")
	require.Equal(t, http.StatusOK, ordinarySelf.Code, ordinarySelf.Body.String())
	assert.Contains(t, ordinarySelf.Body.String(), `"role":1`)
	assert.NotEqual(t, http.StatusOK, request("GET", "/api/user/", bundle.AccessToken, true, "").Code)
	require.NoError(t, f.db.Model(&user).Update("status", common.UserStatusDisabled).Error)
	assert.NotEqual(t, http.StatusOK, request("GET", "/api/token/", bundle.AccessToken, true, "").Code)
	require.NoError(t, f.db.Model(&user).Update("status", common.UserStatusEnabled).Error)
	t.Setenv("NATIVE_PUBLIC_ACCESS", "true")
	assert.NotEqual(t, http.StatusOK, request("GET", "/api/token/", bundle.AccessToken, false, "").Code)
	t.Setenv("NATIVE_PUBLIC_ACCESS", "false")
	_, err = model.BindUniHubIdentity(platformaccount.Identity{Issuer: "https://hub.test", User: platformaccount.User{ID: "linked-user"}}, user.Id)
	require.NoError(t, err)
	assert.NotEqual(t, http.StatusOK, request("GET", "/api/token/", bundle.AccessToken, true, "").Code)
	t.Setenv("NATIVE_PRIVATE_MAINTENANCE_ENABLED", "false")
	assert.NotEqual(t, http.StatusOK, request("GET", "/api/token/", bundle.AccessToken, true, "").Code)
}
