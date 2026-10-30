package router

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/oauth"
	"github.com/QuantumNous/new-api/service"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMixedLegacyOAuthHTTPMutationKeepsCentralSession(t *testing.T) {
	f := newUniHubRegressionFixture(t)
	t.Setenv("NATIVE_PUBLIC_ACCESS", "true")
	t.Setenv("UNIHUB_PRODUCT_RUNTIME_ENABLED", "false")
	require.NoError(t, f.db.AutoMigrate(&model.CentralAuthState{}, &model.CentralLoginCredential{}, &model.CentralSessionCredential{}, &model.NativeLoginCredential{}, &model.ProductAuthLifecycle{}, &model.AuthFlow{}, &model.UserOAuthBinding{}))
	user := model.User{Username: "mixed-oauth-http", AffCode: "mixed-oauth-http", Status: common.UserStatusEnabled, Role: common.RoleCommonUser, AuthVersion: 1}
	require.NoError(t, f.db.Create(&user).Error)
	legacy, err := service.CreateLoginSession(user.Id, "password", "", "")
	require.NoError(t, err)
	require.NoError(t, f.db.Create(&model.CentralAuthState{UserID: user.Id, AuthVersion: 7, Active: true, BindingID: "central"}).Error)
	central, err := service.CreateVerifiedCentralLoginSession(user.Id, 7, "product_broker", time.Now().Unix()+600, "", "")
	require.NoError(t, err)
	require.NoError(t, f.db.Create(&model.UserOAuthBinding{UserId: user.Id, ProviderId: 3, ProviderUserId: "old-subject"}).Error)
	oauth.Register("mixed-fixture", &nativeRealmOAuthProvider{})
	t.Cleanup(func() { oauth.Unregister("mixed-fixture") })
	send := func(method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "https://dashboard.test"+path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+legacy.AccessToken)
		req.Header.Set("Origin", "https://dashboard.test")
		req.Header.Set("Content-Type", "application/json")
		out := httptest.NewRecorder()
		f.router.ServeHTTP(out, req)
		return out
	}
	state := send("POST", "/api/oauth/state", `{"provider":"mixed-fixture","intent":"bind"}`)
	var flow struct {
		Success bool `json:"success"`
		Data    struct {
			Token string `json:"flow_token"`
		} `json:"data"`
	}
	require.NoError(t, common.Unmarshal(state.Body.Bytes(), &flow))
	require.True(t, flow.Success, state.Body.String())
	bound := send("GET", "/api/oauth/mixed-fixture?state="+flow.Data.Token+"&code=local", "")
	require.NotContains(t, bound.Body.String(), `"success":true`)
	deleted := send("DELETE", "/api/user/oauth/bindings/3", "")
	require.NotContains(t, deleted.Body.String(), `"success":true`)
	var persisted model.User
	require.NoError(t, f.db.First(&persisted, user.Id).Error)
	assert.Empty(t, persisted.OidcId)
	assert.EqualValues(t, 1, persisted.AuthVersion)
	var binding model.UserOAuthBinding
	require.NoError(t, f.db.Where("user_id = ? AND provider_id = ?", user.Id, 3).First(&binding).Error)
	assert.Equal(t, "old-subject", binding.ProviderUserId)
	centralID, err := service.ParseAccessToken(central.AccessToken)
	require.NoError(t, err)
	_, _, err = service.ValidateLoginSession(centralID)
	require.NoError(t, err)
	var count int64
	require.NoError(t, f.db.Model(&model.ProductAuthLifecycle{}).Where("user_id = ?", user.Id).Count(&count).Error)
	assert.Zero(t, count)
}
