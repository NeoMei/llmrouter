package router

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/service/authz"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNativeCoexistencePreservesLoginWalletKeysAndManagement(t *testing.T) {
	f := newUniHubRegressionFixture(t)
	t.Setenv("NATIVE_PUBLIC_ACCESS", "true")
	require.NoError(t, f.db.AutoMigrate(&model.TwoFA{}, &model.Token{}, &model.TopUp{}, &model.NativeBilling{}, &model.Channel{}, &model.Ability{}, &model.CasbinRule{}, &model.AuthzRole{}, &model.Log{}))
	oldPasswordLogin, oldLogDB, oldMaster := common.PasswordLoginEnabled, model.LOG_DB, common.IsMasterNode
	common.PasswordLoginEnabled, model.LOG_DB, common.IsMasterNode = true, f.db, true
	t.Cleanup(func() {
		common.PasswordLoginEnabled, model.LOG_DB, common.IsMasterNode = oldPasswordLogin, oldLogDB, oldMaster
	})
	require.NoError(t, authz.Init(f.db))
	password, err := common.Password2Hash("native-password")
	require.NoError(t, err)
	pat := "native.operator.pat"
	native := model.User{Username: "native-operator", Password: password, Role: common.RoleRootUser, Status: common.UserStatusEnabled, AuthVersion: 1, AffCode: "native-operator", AccessToken: &pat, Group: "default", Quota: 12345}
	require.NoError(t, f.db.Create(&native).Error)
	require.NoError(t, f.db.Create(&model.TopUp{UserId: native.Id, TradeNo: "legacy-wallet-order", Amount: 10, Money: 10, CreateTime: common.GetTimestamp()}).Error)

	req := httptest.NewRequest(http.MethodPost, "https://dashboard.test/api/user/login", strings.NewReader(`{"username":"native-operator","password":"native-password"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	f.router.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var bundle struct {
		Success bool `json:"success"`
		Data    struct {
			AccessToken string     `json:"access_token"`
			User        model.User `json:"user"`
		} `json:"data"`
	}
	require.NoError(t, common.Unmarshal(w.Body.Bytes(), &bundle))
	require.True(t, bundle.Success, w.Body.String())
	require.NotEmpty(t, bundle.Data.AccessToken)
	assert.Equal(t, native.Id, bundle.Data.User.Id)
	assert.Empty(t, bundle.Data.User.IdentitySource)
	var cookie *http.Cookie
	for _, current := range w.Result().Cookies() {
		if current.Name == service.RefreshCookieName {
			cookie = current
		}
	}
	require.NotNil(t, cookie)
	for _, token := range []string{bundle.Data.AccessToken, pat} {
		for _, path := range []string{"/api/user/self", "/api/user/topup/self", "/api/native-billing/self", "/api/token/", "/api/channel/", "/api/option/"} {
			got := f.request(http.MethodGet, path, token, nil)
			require.Equal(t, http.StatusOK, got.Code, "%s: %s", path, got.Body.String())
			assert.Contains(t, got.Body.String(), `"success":true`, path)
		}
		assert.Contains(t, f.request(http.MethodGet, "/api/user/self", token, nil).Body.String(), `"quota":12345`)
		assert.Contains(t, f.request(http.MethodGet, "/api/user/topup/self", token, nil).Body.String(), "legacy-wallet-order")
	}
	req = httptest.NewRequest(http.MethodPost, "https://dashboard.test/api/token/", strings.NewReader(`{"name":"legacy-created-key","remain_quota":100,"expired_time":-1}`))
	req.Header.Set("Authorization", "Bearer "+bundle.Data.AccessToken)
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	f.router.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Contains(t, w.Body.String(), `"success":true`)
	var token model.Token
	require.NoError(t, f.db.Where("user_id = ? AND name = ?", native.Id, "legacy-created-key").First(&token).Error)
	assert.Equal(t, 100, token.RemainQuota)
	assert.NotEmpty(t, token.Key)
	assert.Contains(t, f.request(http.MethodGet, "/api/token/", bundle.Data.AccessToken, nil).Body.String(), "legacy-created-key")
	assert.Equal(t, http.StatusUnauthorized, f.request(http.MethodGet, "/api/channel/", "", nil).Code)
	refreshed := f.request(http.MethodPost, "/api/user/auth/refresh", "", cookie)
	require.Equal(t, http.StatusOK, refreshed.Code, refreshed.Body.String())
	assert.Contains(t, refreshed.Body.String(), `"success":true`)
	assert.Contains(t, refreshed.Body.String(), `"quota":12345`)
	config := f.request(http.MethodGet, "/api/unihub/config", "", nil)
	assert.Contains(t, config.Body.String(), `"native_access":true`)
}

func TestNativeCoexistenceKeepsProjectionNativeResourcesForbidden(t *testing.T) {
	f := newUniHubRegressionFixture(t)
	t.Setenv("NATIVE_PUBLIC_ACCESS", "true")
	require.NoError(t, f.db.AutoMigrate(&model.Channel{}, &model.Ability{}))
	bundle, _ := decodeUniHubBundle(t, f.request(http.MethodPost, "/api/unihub/session", "product-admin", nil))
	allowed := f.request(http.MethodGet, "/api/channel/", bundle.Data.AccessToken, nil)
	require.Equal(t, http.StatusOK, allowed.Code, allowed.Body.String())
	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/api/user/topup/self"}, {http.MethodPost, "/api/user/topup"},
		{http.MethodGet, "/api/native-billing/self"}, {http.MethodGet, "/api/user/token"},
		{http.MethodGet, "/api/token/"}, {http.MethodPost, "/api/token/"},
		{http.MethodGet, "/api/user/"}, {http.MethodPost, "/api/channel/1/key"},
		{http.MethodPost, "/v1/chat/completions"},
	} {
		t.Run(tc.method+tc.path, func(t *testing.T) {
			got := f.request(tc.method, tc.path, bundle.Data.AccessToken, nil)
			require.Equal(t, http.StatusForbidden, got.Code, got.Body.String())
		})
	}
	var projected model.User
	require.NoError(t, f.db.First(&projected, bundle.Data.User.Id).Error)
	assert.Equal(t, common.RoleCommonUser, projected.Role)
	assert.Zero(t, projected.Quota)
}
