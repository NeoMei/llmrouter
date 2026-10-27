package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/internal/platformaccount"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNativeCoexistenceKeepsAPIKeyAuthenticationConstraints(t *testing.T) {
	setupDashboardAuthMiddlewareTest(t)
	oldLogDB, oldLogType := model.LOG_DB, common.LogDatabaseType()
	t.Setenv("LOG_SQL_DSN", "")
	require.NoError(t, model.InitLogDB())
	t.Cleanup(func() { model.LOG_DB = oldLogDB; common.SetLogDatabaseType(oldLogType) })
	require.NoError(t, model.DB.AutoMigrate(&model.Token{}, &model.UniHubIdentityMapping{}))
	t.Setenv("UNIHUB_PRODUCT_ENABLED", "true")
	t.Setenv("UNIHUB_PLATFORM_ENABLED", "true")
	t.Setenv("NATIVE_PUBLIC_ACCESS", "true")
	t.Setenv("NATIVE_ADMIN_LISTEN_ADDRESS", "127.0.0.1:3001")
	native := createMiddlewarePATUser(t, "native-key-owner", "native-pat")
	projected, err := model.ResolveUniHubProjection(platformaccount.Identity{Issuer: "https://hub.test", User: platformaccount.User{ID: "key-owner"}})
	require.NoError(t, err)
	allowIP := "192.0.2.1/32"
	for _, token := range []model.Token{
		{Key: "nativevalid", UserId: native.Id, Status: common.TokenStatusEnabled, RemainQuota: 100, ExpiredTime: -1},
		{Key: "nativeexpired", UserId: native.Id, Status: common.TokenStatusEnabled, RemainQuota: 100, ExpiredTime: time.Now().Add(-time.Minute).Unix()},
		{Key: "nativeempty", UserId: native.Id, Status: common.TokenStatusEnabled, RemainQuota: 0, ExpiredTime: -1},
		{Key: "nativedisabled", UserId: native.Id, Status: common.TokenStatusDisabled, RemainQuota: 100, ExpiredTime: -1},
		{Key: "nativeip", UserId: native.Id, Status: common.TokenStatusEnabled, RemainQuota: 100, ExpiredTime: -1, AllowIps: &allowIP},
		{Key: "projectedkey", UserId: projected.Id, Status: common.TokenStatusEnabled, RemainQuota: 100, ExpiredTime: -1},
	} {
		require.NoError(t, model.DB.Create(&token).Error)
	}
	r := gin.New()
	require.NoError(t, r.SetTrustedProxies(nil))
	r.GET("/relay", NativePublicBoundary(), TokenAuth(), func(c *gin.Context) { c.JSON(200, gin.H{"id": c.GetInt("id")}) })
	r.GET("/usage", NativePublicBoundary(), TokenAuthReadOnly(), func(c *gin.Context) { c.JSON(200, gin.H{"id": c.GetInt("id")}) })
	for _, tc := range []struct {
		path, key string
		status    int
	}{
		{"/relay", "nativevalid", 200}, {"/relay", "nativeexpired", 401},
		{"/relay", "nativeempty", 401}, {"/relay", "nativedisabled", 401},
		{"/relay", "nativeip", 403}, {"/relay", "missing", 401},
		{"/usage", "nativeexpired", 200}, {"/usage", "nativeempty", 200},
		{"/usage", "nativedisabled", 401},
		{"/relay", "projectedkey", 403}, {"/usage", "projectedkey", 403},
	} {
		t.Run(tc.path+"/"+tc.key, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			req.RemoteAddr = "198.51.100.1:1234"
			req.Header.Set("Authorization", "Bearer sk-"+tc.key)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			require.Equal(t, tc.status, w.Code, w.Body.String())
			if tc.status == 200 {
				assert.JSONEq(t, `{"id":1}`, w.Body.String())
			}
		})
	}
}
