package middleware

import (
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"net/http/httptest"
	"testing"
)

func TestNativeDashboardSessionPropagatesRelayRealm(t *testing.T) {
	setupDashboardAuthMiddlewareTest(t)
	require.NoError(t, model.RegisterProductFundingModels(model.DB))
	require.NoError(t, model.DB.AutoMigrate(&model.UniHubIdentityMapping{}))
	user := createMiddlewarePATUser(t, "native-playground-owner", "unused")
	require.NoError(t, model.DB.Model(user).Updates(map[string]any{"funding_realm": model.NativeFundingLocalOnly, "funding_realm_evidence": "reviewed", "native_password": true}).Error)
	bundle, err := service.CreateNativeLoginSessionAtAuthVersion(user.Id, user.AuthVersion, "password", "", "")
	require.NoError(t, err)
	r := gin.New()
	r.POST("/pg/chat/completions", UserAuth(), func(c *gin.Context) {
		assert.True(t, NativeRealmAuthenticated(c))
		assert.True(t, c.GetBool(string(constant.ContextKeyNativeRealm)), "relay metadata must preserve the authenticated SID realm")
		c.Status(204)
	})
	req := httptest.NewRequest("POST", "/pg/chat/completions", nil)
	req.Header.Set("Authorization", "Bearer "+bundle.AccessToken)
	out := httptest.NewRecorder()
	r.ServeHTTP(out, req)
	assert.Equal(t, 204, out.Code)
}
