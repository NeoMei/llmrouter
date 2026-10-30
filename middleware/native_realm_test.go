package middleware

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestNativeRealmSelfWritersRequireNativeSession(t *testing.T) {
	setupDashboardAuthMiddlewareTest(t)
	require.NoError(t, model.RegisterProductFundingModels(model.DB))
	require.NoError(t, model.DB.AutoMigrate(&model.UniHubIdentityMapping{}))
	user := createMiddlewarePATUser(t, "realm-owner", "unused")
	require.NoError(t, model.DB.Model(user).Updates(map[string]any{"funding_realm": model.NativeFundingLocalOnly, "funding_realm_evidence": "reviewed", "native_password": true}).Error)
	central, err := service.CreateLoginSession(user.Id, "password", "", "")
	require.NoError(t, err)
	native, err := service.CreateNativeLoginSessionAtAuthVersion(user.Id, user.AuthVersion, "password", "", "")
	require.NoError(t, err)
	routes := []struct{ method, path string }{
		{"PUT", "/api/user/self"}, {"DELETE", "/api/user/self"}, {"POST", "/api/user/2fa/setup"}, {"POST", "/api/user/2fa/enable"}, {"POST", "/api/user/2fa/backup_codes"}, {"POST", "/api/user/passkey/register/begin"}, {"DELETE", "/api/user/passkey"}, {"DELETE", "/api/user/oauth/bindings/:provider_id"}, {"GET", "/api/user/token"}, {"PUT", "/api/user/setting"}, {"POST", "/api/user/aff_transfer"}, {"POST", "/api/user/checkin"}, {"POST", "/api/user/topup"}, {"POST", "/api/user/stripe/pay"}, {"POST", "/api/subscription/balance/pay"}, {"PUT", "/api/subscription/self/preference"},
	}
	r := gin.New()
	for _, route := range routes {
		r.Handle(route.method, route.path, UserAuth(), func(c *gin.Context) { c.Status(204) })
	}
	for _, route := range routes {
		for _, session := range []struct {
			token string
			want  int
		}{{central.AccessToken, 403}, {native.AccessToken, 204}} {
			req := httptest.NewRequest(route.method, route.path, nil)
			req.Header.Set("Authorization", "Bearer "+session.token)
			res := httptest.NewRecorder()
			r.ServeHTTP(res, req)
			assert.Equal(t, session.want, res.Code, route.path+": "+res.Body.String())
		}
	}
	var current model.User
	require.NoError(t, model.DB.First(&current, user.Id).Error)
	assert.Equal(t, user.AuthVersion, current.AuthVersion)
	assert.Equal(t, common.UserStatusEnabled, current.Status)
}

func TestPATRealmClassificationKeepsNativeLegacyAndRejectsConflicts(t *testing.T) {
	setupDashboardAuthMiddlewareTest(t)
	require.NoError(t, model.RegisterProductFundingModels(model.DB))
	require.NoError(t, model.DB.AutoMigrate(&model.UniHubIdentityMapping{}, &model.NativeLoginCredential{}, &model.CentralLoginCredential{}, &model.CentralSessionCredential{}, &model.CentralAuthState{}))
	t.Setenv("UNIHUB_INDEPENDENT_CENTRAL_REALM_ENABLED", "true")
	t.Setenv("UNIHUB_PRODUCT_RUNTIME_ENABLED", "false")
	nativeUser := createMiddlewarePATUser(t, "native-pat-review", "native-pat-value")
	require.NoError(t, model.DB.Model(nativeUser).Updates(map[string]any{"funding_realm": model.NativeFundingLocalOnly, "funding_realm_evidence": "reviewed", "native_password": true}).Error)
	require.NoError(t, model.UpdateUserAccessToken(nativeUser.Id, "native-pat-value", model.NativeFundingLocalOnly))
	legacyUser := createMiddlewarePATUser(t, "legacy-pat-review", "legacy-pat-value")
	r := gin.New()
	r.GET("/protected", UserAuth(), func(c *gin.Context) { c.Status(http.StatusNoContent) })
	request := func(token string) int {
		req := httptest.NewRequest(http.MethodGet, "/protected", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w.Code
	}
	require.Equal(t, http.StatusNoContent, request("native-pat-value"))
	require.Equal(t, http.StatusNoContent, request("legacy-pat-value"))
	for _, evidence := range []struct {
		name  string
		value any
	}{
		{"central state", &model.CentralAuthState{UserID: legacyUser.Id, AuthVersion: 1, Active: true}},
		{"central session", &model.UserSession{SID: "evidence-central", UserID: legacyUser.Id, AuthRealm: model.CentralAuthRealm, Status: model.UserSessionStatusActive, ExpiresAt: time.Now().Unix() + 600}},
		{"other central credential", &model.CentralLoginCredential{UserID: legacyUser.Id, Kind: "passkey", Digest: "proof", BindingID: "binding", AuthVersion: 1}},
		{"central session credential", &model.CentralSessionCredential{SID: "evidence-proof", UserID: legacyUser.Id, Kind: "pat", Digest: "proof"}},
	} {
		require.NoError(t, model.DB.Create(evidence.value).Error, evidence.name)
		require.Equal(t, http.StatusUnauthorized, request("legacy-pat-value"), evidence.name)
		require.NoError(t, model.DB.Delete(evidence.value).Error, evidence.name)
		require.Equal(t, http.StatusNoContent, request("legacy-pat-value"), evidence.name)
	}
	require.NoError(t, model.DB.Create(&model.CentralLoginCredential{UserID: nativeUser.Id, Kind: "pat", Digest: "conflict", BindingID: "conflict", AuthVersion: 1}).Error)
	require.Equal(t, http.StatusUnauthorized, request("native-pat-value"), "two realm proofs cannot be resolved by preference")
	require.NoError(t, model.DB.Where("user_id = ? AND kind = ?", nativeUser.Id, "pat").Delete(&model.CentralLoginCredential{}).Error)
	require.NoError(t, model.DB.Callback().Query().Before("gorm:query").Register("test:pat_proof_store_error", func(tx *gorm.DB) {
		if tx.Statement.Table == "central_login_credentials" {
			tx.AddError(errors.New("proof store unavailable"))
		}
	}))
	require.Equal(t, http.StatusServiceUnavailable, request("native-pat-value"))
	require.Equal(t, http.StatusServiceUnavailable, request("legacy-pat-value"))
	require.NoError(t, model.DB.Callback().Query().Remove("test:pat_proof_store_error"))
	require.Equal(t, http.StatusNoContent, request("native-pat-value"))
	require.Equal(t, http.StatusNoContent, request("legacy-pat-value"))
	for _, table := range []string{"central_auth_states", "user_sessions", "central_session_credentials"} {
		callback := "test:pat_evidence_store_error:" + table
		require.NoError(t, model.DB.Callback().Query().Before("gorm:query").Register(callback, func(tx *gorm.DB) {
			if tx.Statement.Table == table {
				tx.AddError(errors.New("evidence store unavailable"))
			}
		}))
		require.Equal(t, http.StatusServiceUnavailable, request("legacy-pat-value"), table)
		require.NoError(t, model.DB.Callback().Query().Remove(callback))
		require.Equal(t, http.StatusNoContent, request("legacy-pat-value"), table)
	}
	t.Setenv("UNIHUB_INDEPENDENT_CENTRAL_REALM_ENABLED", "false")
	require.Equal(t, http.StatusNoContent, request("native-pat-value"))
	require.Equal(t, http.StatusNoContent, request("legacy-pat-value"))
	require.NoError(t, model.DB.Create(&model.CentralAuthState{UserID: legacyUser.Id, AuthVersion: 1, Active: true}).Error)
	require.Equal(t, http.StatusUnauthorized, request("legacy-pat-value"), "disabled flag must not erase central evidence")
}

func TestNativeRealmNewPATUsesExplicitServerBinding(t *testing.T) {
	setupDashboardAuthMiddlewareTest(t)
	require.NoError(t, model.RegisterProductFundingModels(model.DB))
	require.NoError(t, model.DB.AutoMigrate(&model.NativeLoginCredential{}, &model.UniHubIdentityMapping{}))
	user := createMiddlewarePATUser(t, "native-pat-owner", "old-unclassified-pat")
	require.NoError(t, model.DB.Model(user).Updates(map[string]any{"funding_realm": model.NativeFundingLocalOnly, "funding_realm_evidence": "reviewed", "native_password": true}).Error)
	t.Setenv("UNIHUB_PRODUCT_RUNTIME_ENABLED", "true")
	t.Setenv("UNIHUB_ORIGIN", "http://127.0.0.1:1")
	t.Setenv("UNIHUB_PRODUCT_RUNTIME_TOKEN", "ssssssssssssssssssssssssssssssss")
	r := gin.New()
	r.GET("/api/user/self", UserAuth(), func(c *gin.Context) { assert.True(t, NativeRealmAuthenticated(c)); c.Status(204) })
	request := func(key string) int {
		req := httptest.NewRequest("GET", "/api/user/self", nil)
		req.Header.Set("Authorization", "Bearer "+key)
		out := httptest.NewRecorder()
		r.ServeHTTP(out, req)
		return out.Code
	}
	assert.NotEqual(t, 204, request("old-unclassified-pat"))
	require.Error(t, model.UpdateUserAccessToken(user.Id, "central-pat", ""))
	require.NoError(t, model.UpdateUserAccessToken(user.Id, "issued-native-pat", model.NativeFundingLocalOnly))
	assert.Equal(t, 204, request("issued-native-pat"))
	require.NoError(t, model.UpdateUserAccessToken(user.Id, "rotated-native-pat", model.NativeFundingLocalOnly))
	assert.NotEqual(t, 204, request("issued-native-pat"))
	assert.Equal(t, 204, request("rotated-native-pat"))
}
