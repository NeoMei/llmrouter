package router

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/oauth"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type nativeRealmOAuthProvider struct{}

func (*nativeRealmOAuthProvider) GetName() string { return "Native realm local fixture" }
func (*nativeRealmOAuthProvider) IsEnabled() bool { return true }
func (*nativeRealmOAuthProvider) ExchangeToken(context.Context, string, *gin.Context) (*oauth.OAuthToken, error) {
	return &oauth.OAuthToken{}, nil
}
func (*nativeRealmOAuthProvider) GetUserInfo(context.Context, *oauth.OAuthToken) (*oauth.OAuthUser, error) {
	return &oauth.OAuthUser{ProviderUserID: "synthetic-provider-subject"}, nil
}
func (*nativeRealmOAuthProvider) IsUserIDTaken(id string) bool {
	var n int64
	model.DB.Model(&model.User{}).Where("oidc_id = ?", id).Count(&n)
	return n > 0
}
func (*nativeRealmOAuthProvider) FillUserByProviderID(u *model.User, id string) error {
	return model.DB.Where("oidc_id = ?", id).First(u).Error
}
func (*nativeRealmOAuthProvider) SetProviderUserID(u *model.User, id string) { u.OidcId = id }
func (*nativeRealmOAuthProvider) GetProviderPrefix() string                  { return "native_fixture_" }
func (*nativeRealmOAuthProvider) ProviderUserIDColumn() string               { return "oidc_id" }

func TestNativeRealmOAuthBindThenLoginUsesPersistedNativeCredential(t *testing.T) {
	f := newUniHubRegressionFixture(t)
	t.Setenv("UNIHUB_PRODUCT_RUNTIME_ENABLED", "true")
	t.Setenv("UNIHUB_ORIGIN", "http://127.0.0.1:1")
	t.Setenv("UNIHUB_PRODUCT_RUNTIME_TOKEN", strings.Repeat("s", 32))
	t.Setenv("NATIVE_PUBLIC_ACCESS", "true")
	require.NoError(t, model.RegisterProductFundingModels(f.db))
	require.NoError(t, f.db.AutoMigrate(&model.NativeLoginCredential{}, &model.ProductAuthLifecycle{}, &model.AuthFlow{}, &model.Log{}))
	oldLog := model.LOG_DB
	model.LOG_DB = f.db
	t.Cleanup(func() { model.LOG_DB = oldLog })
	user := model.User{Username: "native-sso", AffCode: "native-sso", Password: "synthetic-password", Role: 100, Status: 1, AuthVersion: 1, FundingRealm: model.NativeFundingLocalOnly, FundingRealmEvidence: "reviewed", NativePassword: true}
	require.NoError(t, f.db.Create(&user).Error)
	oauth.Register("native-local-fixture", &nativeRealmOAuthProvider{})
	t.Cleanup(func() { oauth.Unregister("native-local-fixture") })
	native, err := service.CreateNativeLoginSessionAtAuthVersion(user.Id, 1, "password", "", "")
	require.NoError(t, err)
	send := func(method, path, token, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "https://dashboard.test"+path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", "https://dashboard.test")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		out := httptest.NewRecorder()
		f.router.ServeHTTP(out, req)
		return out
	}
	state := send("POST", "/api/oauth/state", native.AccessToken, `{"provider":"native-local-fixture","intent":"bind"}`)
	require.Equal(t, 200, state.Code, state.Body.String())
	var flow struct {
		Success bool `json:"success"`
		Data    struct {
			Token string `json:"flow_token"`
		} `json:"data"`
	}
	require.NoError(t, common.Unmarshal(state.Body.Bytes(), &flow))
	require.True(t, flow.Success, state.Body.String())
	bound := send("GET", "/api/oauth/native-local-fixture?state="+flow.Data.Token+"&code=local", native.AccessToken, "")
	require.Equal(t, 200, bound.Code, bound.Body.String())
	require.Contains(t, bound.Body.String(), `"success":true`)
	state = send("POST", "/api/oauth/state", "", `{"provider":"native-local-fixture","intent":"login"}`)
	require.NoError(t, common.Unmarshal(state.Body.Bytes(), &flow))
	require.True(t, flow.Success, state.Body.String())
	logged := send("GET", "/api/oauth/native-local-fixture?state="+flow.Data.Token+"&code=local", "", "")
	require.Equal(t, 200, logged.Code, logged.Body.String())
	var bundle struct {
		Success bool `json:"success"`
		Data    struct {
			AccessToken string `json:"access_token"`
		} `json:"data"`
	}
	require.NoError(t, common.Unmarshal(logged.Body.Bytes(), &bundle))
	require.True(t, bundle.Success, logged.Body.String())
	identity, err := service.ParseAccessToken(bundle.Data.AccessToken)
	require.NoError(t, err)
	ok, err := service.IsNativeLoginSession(context.Background(), identity)
	require.NoError(t, err)
	assert.True(t, ok)
	require.NoError(t, f.db.First(&user, user.Id).Error)
	assert.Equal(t, 100, user.Role)
	assert.Equal(t, common.UserStatusEnabled, user.Status)
}
