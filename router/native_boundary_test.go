package router

import (
	"context"
	"github.com/QuantumNous/new-api/common"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNativeBoundaryRealRoutes(t *testing.T) {
	t.Setenv("UNIHUB_PRODUCT_ENABLED", "true")
	t.Setenv("UNIHUB_PLATFORM_ENABLED", "true")
	t.Setenv("NATIVE_PUBLIC_ACCESS", "true")
	t.Setenv("NATIVE_ADMIN_LISTEN_ADDRESS", "127.0.0.1:3001")
	previous := common.GlobalApiRateLimitEnable
	common.GlobalApiRateLimitEnable = false
	t.Cleanup(func() { common.GlobalApiRateLimitEnable = previous })
	r := gin.New()
	SetApiRouter(r)
	routes := []struct{ method, path string }{
		{"POST", "/api/user/login"}, {"POST", "/api/user/register"}, {"POST", "/api/user/login/2fa"}, {"POST", "/api/user/passkey/login/begin"}, {"POST", "/api/user/passkey/login/finish"},
		{"GET", "/api/user/token"}, {"GET", "/api/user/topup/info"}, {"GET", "/api/user/topup/self"}, {"POST", "/api/user/topup"}, {"POST", "/api/user/pay"},
		{"POST", "/api/user/wechat-pay/native"}, {"GET", "/api/user/wechat-pay/orders/test"}, {"POST", "/api/user/amount"}, {"POST", "/api/user/stripe/pay"}, {"POST", "/api/user/stripe/amount"}, {"POST", "/api/user/creem/pay"}, {"POST", "/api/user/waffo/amount"}, {"POST", "/api/user/waffo/pay"}, {"POST", "/api/user/waffo-pancake/amount"}, {"POST", "/api/user/waffo-pancake/pay"},
		{"GET", "/api/subscription/plans"}, {"GET", "/api/subscription/self"}, {"PUT", "/api/subscription/self/preference"}, {"POST", "/api/subscription/balance/pay"}, {"POST", "/api/subscription/epay/pay"}, {"POST", "/api/subscription/stripe/pay"}, {"POST", "/api/subscription/creem/pay"}, {"POST", "/api/subscription/waffo-pancake/pay"},
	}
	for _, route := range routes {
		t.Run(route.method+route.path, func(t *testing.T) {
			req := httptest.NewRequest(route.method, route.path, nil)
			req.RemoteAddr = "203.0.113.1:1234"
			req.Header.Set("Origin", "https://admin.example")
			req.Header.Set("X-Forwarded-Host", "admin.example")
			req.Header.Set("Authorization", "Bearer secret")
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			require.Equal(t, 403, w.Code)
			assert.JSONEq(t, `{"success":false,"code":"NATIVE_ACCESS_DISABLED","message":"native identity and billing are managed by UniHub"}`, w.Body.String())
		})
	}
	// Private transport grants only boundary access: native authentication remains.
	req := httptest.NewRequest("GET", "/api/user/topup/info", nil)
	req = req.WithContext(context.WithValue(req.Context(), http.LocalAddrContextKey, &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 3001}))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, 401, w.Code)
	assert.NotContains(t, w.Body.String(), "NATIVE_ACCESS_DISABLED")
	// Signed callback handlers remain public for existing orders, rejecting this unsigned callback themselves.
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/api/user/epay/notify", nil))
	assert.NotEqual(t, 404, w.Code)
	assert.NotContains(t, w.Body.String(), "NATIVE_ACCESS_DISABLED")
}
