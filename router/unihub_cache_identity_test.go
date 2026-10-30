package router

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/internal/platformaccount"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/alicebob/miniredis/v2"
	"github.com/go-redis/redis/v8"
	"github.com/stretchr/testify/require"
)

func TestUniHubSSORedisIdentitySurvivesRefreshAndAccess(t *testing.T) {
	enableUniHubBoundary(t)
	f := newUniHubSSOFixture(t, nil)
	server := miniredis.RunT(t)
	oldRedis, oldRDB := common.RedisEnabled, common.RDB
	oldCritical, oldGlobal := common.CriticalRateLimitEnable, common.GlobalApiRateLimitEnable
	oldSecure := common.SessionCookieSecure
	common.RedisEnabled = true
	common.RDB = redis.NewClient(&redis.Options{Addr: server.Addr()})
	common.CriticalRateLimitEnable, common.GlobalApiRateLimitEnable = false, false
	common.SessionCookieSecure = true
	t.Cleanup(func() {
		require.NoError(t, common.RDB.Close())
		common.RedisEnabled, common.RDB = oldRedis, oldRDB
		common.CriticalRateLimitEnable, common.GlobalApiRateLimitEnable = oldCritical, oldGlobal
		common.SessionCookieSecure = oldSecure
	})
	c := platformaccount.Config{Enabled: true, ProductID: "llmrouter", InstanceID: "default", APIOrigin: f.issuer,
		PublicOrigin: f.issuer, OIDCIssuer: f.issuer, ClientID: "router-client", RedirectURI: "http://127.0.0.1:58119/api/unihub/callback"}
	platformaccount.Configure(c)
	registerUniHubBFF(f.router, c)
	SetApiRouter(f.router)

	callback := f.callback(t, f.login(context.Background()))
	require.Equal(t, http.StatusSeeOther, callback.Code)
	requireSessionRedirect(t, callback, "/channels")
	var refreshCookie *http.Cookie
	for _, cookie := range callback.Result().Cookies() {
		if cookie.Name == service.RefreshCookieName {
			refreshCookie = cookie
		}
	}
	require.NotNil(t, refreshCookie)
	request := func(method, path, bearer string, cookie *http.Cookie) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "http://127.0.0.1:58119"+path, nil)
		req.Header.Set("Origin", "http://127.0.0.1:58119")
		if cookie != nil {
			req.AddCookie(cookie)
		}
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		w := httptest.NewRecorder()
		f.router.ServeHTTP(w, req)
		return w
	}
	refreshed, rotated := decodeUniHubBundle(t, request(http.MethodPost, "/api/user/auth/refresh", "", refreshCookie))
	require.Equal(t, "central-user", refreshed.Data.User.UniHubUserID)
	access := request(http.MethodGet, "/api/unihub/access", refreshed.Data.AccessToken, nil)
	require.Equal(t, http.StatusOK, access.Code, access.Body.String())
	require.Contains(t, access.Body.String(), `"subject":"central-user"`)
	require.Equal(t, http.StatusForbidden, request(http.MethodGet, "/api/channel/", refreshed.Data.AccessToken, nil).Code)
	_, _ = decodeUniHubBundle(t, request(http.MethodPost, "/api/user/auth/refresh", "", rotated))
	var local model.User
	require.NoError(t, f.db.First(&local, refreshed.Data.User.Id).Error)
	require.Equal(t, common.RoleCommonUser, local.Role)
	require.Zero(t, local.Quota)
	require.Zero(t, local.UsedQuota)
	require.Zero(t, local.RequestCount)
	require.Empty(t, local.Password)
	require.Nil(t, local.AccessToken)
}
