package router

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/alicebob/miniredis/v2"
	"github.com/gin-gonic/gin"
	"github.com/go-redis/redis/v8"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRefreshAndLogoutDoNotSharePasswordAttemptWindow(t *testing.T) {
	f := newUniHubRegressionFixture(t)
	server := miniredis.RunT(t)
	oldRedis, oldRDB := common.RedisEnabled, common.RDB
	oldCritical, oldNum, oldDuration := common.CriticalRateLimitEnable, common.CriticalRateLimitNum, common.CriticalRateLimitDuration
	common.RedisEnabled = true
	common.RDB = redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() {
		require.NoError(t, common.RDB.Close())
		common.RedisEnabled, common.RDB = oldRedis, oldRDB
		common.CriticalRateLimitEnable, common.CriticalRateLimitNum, common.CriticalRateLimitDuration = oldCritical, oldNum, oldDuration
	})
	_, cookie := decodeUniHubBundle(t, f.request(http.MethodPost, "/api/unihub/session", "subject", nil))
	common.CriticalRateLimitEnable, common.CriticalRateLimitNum, common.CriticalRateLimitDuration = true, 20, 1200
	r := gin.New()
	require.NoError(t, r.SetTrustedProxies(nil))
	SetApiRouter(r)
	const keyPrefix = "rateLimit:v2:ip:"
	const syntheticIP = "192.0.2.81"
	const criticalKey = keyPrefix + "CT:" + syntheticIP
	server.Set(criticalKey, "20")
	server.SetTTL(criticalKey, 1200*time.Second)
	request := func(path string, cookie *http.Cookie, private bool) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "https://dashboard.test"+path, nil)
		req.RemoteAddr = syntheticIP + ":1234"
		req.Header.Set("Origin", "https://dashboard.test")
		if cookie != nil {
			req.AddCookie(cookie)
		}
		if private {
			req = req.WithContext(context.WithValue(req.Context(), http.LocalAddrContextKey, &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 3001}))
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}
	bundle, rotated := decodeUniHubBundle(t, request("/api/user/auth/refresh", cookie, false))
	count, err := server.Get(criticalKey)
	require.NoError(t, err)
	assert.Equal(t, "20", count)
	refreshKey := keyPrefix + "AR:" + syntheticIP
	server.Set(refreshKey, "120")
	server.SetTTL(refreshKey, 60*time.Second)
	blocked := request("/api/user/auth/refresh", rotated, false)
	require.Equal(t, 429, blocked.Code)
	assert.Equal(t, "60", blocked.Header().Get("Retry-After"))
	assert.Empty(t, blocked.Result().Cookies())
	var before model.UserSession
	require.NoError(t, f.db.First(&before, "sid = ?", bundle.Data.Session.SID).Error)
	server.FastForward(61 * time.Second)
	_, nextCookie := decodeUniHubBundle(t, request("/api/user/auth/refresh", rotated, false))
	var after model.UserSession
	require.NoError(t, f.db.First(&after, "sid = ?", bundle.Data.Session.SID).Error)
	assert.True(t, before.Status == model.UserSessionStatusActive && after.Status == model.UserSessionStatusActive)
	logoutKey := keyPrefix + "AL:" + syntheticIP
	server.Set(logoutKey, "120")
	server.SetTTL(logoutKey, 60*time.Second)
	logoutBlocked := request("/api/user/auth/logout", nextCookie, false)
	require.Equal(t, 429, logoutBlocked.Code)
	assert.Equal(t, "60", logoutBlocked.Header().Get("Retry-After"))
	assert.Empty(t, logoutBlocked.Result().Cookies())
	server.FastForward(61 * time.Second)
	logout := request("/api/user/auth/logout", nextCookie, false)
	require.Equal(t, 200, logout.Code)
	password := request("/api/user/login", nil, true)
	assert.Equal(t, 429, password.Code)
	assert.Greater(t, server.TTL(criticalKey), time.Minute)
}
