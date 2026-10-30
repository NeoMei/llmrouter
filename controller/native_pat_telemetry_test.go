package controller

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNativePATReadsHistoryAndUsageThroughPublicAuth(t *testing.T) {
	db, _, token := nativeQuotaControllerFixture(t)
	t.Setenv("UNIHUB_PRODUCT_RUNTIME_ENABLED", "false")
	t.Setenv("UNIHUB_INDEPENDENT_CENTRAL_REALM_ENABLED", "false")
	require.NoError(t, db.AutoMigrate(&model.NativeLoginCredential{}, &model.CentralLoginCredential{}, &model.CentralAuthState{}, &model.CentralSessionCredential{}, &model.UserSession{}, &model.UniHubIdentityMapping{}, &model.Log{}, &model.QuotaData{}))
	oldLog := model.LOG_DB
	model.LOG_DB = db
	t.Cleanup(func() { model.LOG_DB = oldLog })
	require.NoError(t, db.Model(&model.User{}).Where("id = ?", token.UserId).Updates(map[string]any{"funding_realm": model.NativeFundingLocalOnly, "funding_realm_evidence": "synthetic-native-pat", "native_password": true, "role": common.RoleCommonUser, "auth_version": int64(1)}).Error)
	require.NoError(t, db.Create(&model.Log{UserId: token.UserId, Type: model.LogTypeConsume, ModelName: "native-history-record", CreatedAt: 100}).Error)
	require.NoError(t, db.Create(&model.QuotaData{UserID: token.UserId, ModelName: "native-usage-record", CreatedAt: 100, Quota: 17}).Error)
	require.NoError(t, db.Create(&model.Log{UserId: 999, Type: model.LogTypeConsume, ModelName: "other-owner-record", CreatedAt: 100}).Error)
	pat := strings.Repeat("p", 30)
	require.NoError(t, model.UpdateUserAccessToken(token.UserId, pat, model.NativeFundingLocalOnly))
	engine := gin.New()
	engine.GET("/authenticated", middleware.UserAuth(), func(c *gin.Context) {
		assert.True(t, middleware.NativeRealmAuthenticated(c))
		c.Status(http.StatusNoContent)
	})
	engine.GET("/api/log/self", middleware.UserAuth(), GetUserLogs)
	engine.GET("/api/data/self", middleware.UserAuth(), GetUserQuotaDates)
	engine.GET("/api/user/sessions", middleware.UserAuth(), GetLoginSessions)
	request := func(path, credential string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Authorization", "Bearer "+credential)
		response := httptest.NewRecorder()
		engine.ServeHTTP(response, req)
		return response
	}
	require.Equal(t, http.StatusNoContent, request("/authenticated", pat).Code)
	for _, path := range []string{"/api/log/self?start_timestamp=1&end_timestamp=200", "/api/data/self?start_timestamp=1&end_timestamp=200"} {
		response := request(path, pat)
		assert.Equal(t, http.StatusOK, response.Code, path)
		assert.Contains(t, response.Body.String(), `"success":true`, path)
		assert.Contains(t, response.Body.String(), "native-", path)
		assert.NotContains(t, response.Body.String(), "other-owner-record", path)
	}
	response := request("/api/user/sessions", pat)
	assert.Equal(t, http.StatusForbidden, response.Code)
	assert.Contains(t, response.Body.String(), "AUTH_SESSION_REQUIRED")
	// Rotation revokes the old PAT at the same public auth boundary.
	rotated := strings.Repeat("r", 30)
	require.NoError(t, model.UpdateUserAccessToken(token.UserId, rotated, model.NativeFundingLocalOnly))
	assert.NotEqual(t, http.StatusOK, request("/api/log/self", pat).Code)
	require.NoError(t, db.Where("user_id = ? AND kind = ?", token.UserId, "pat").Delete(&model.NativeLoginCredential{}).Error)
	assert.NotEqual(t, http.StatusOK, request("/api/log/self", rotated).Code, "ambiguous PAT must not inherit local history")
	require.NoError(t, model.UpdateUserAccessToken(token.UserId, rotated, model.NativeFundingLocalOnly))
	require.NoError(t, db.Model(&model.User{}).Where("id = ?", token.UserId).Update("status", common.UserStatusDisabled).Error)
	assert.NotEqual(t, http.StatusOK, request("/api/data/self", rotated).Code, "disabled owner cannot read native usage")
	require.NoError(t, db.Model(&model.User{}).Where("id = ?", token.UserId).Update("status", common.UserStatusEnabled).Error)
	require.NoError(t, db.Create(&model.CentralLoginCredential{UserID: token.UserId, Kind: "pat", Digest: "conflicting-central-proof"}).Error)
	assert.NotEqual(t, http.StatusOK, request("/api/log/self", rotated).Code, "central provenance cannot fall through to local history")
}
