package router

import (
	"context"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/controller"
	"github.com/QuantumNous/new-api/internal/platformaccount"
	"github.com/QuantumNous/new-api/middleware"
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
	t.Setenv("NATIVE_PUBLIC_ACCESS", "false")
	t.Setenv("NATIVE_ADMIN_LISTEN_ADDRESS", "127.0.0.1:3001")
	previous := common.GlobalApiRateLimitEnable
	common.GlobalApiRateLimitEnable = false
	t.Cleanup(func() { common.GlobalApiRateLimitEnable = previous })
	r := gin.New()
	r.Use(gin.Recovery())
	SetApiRouter(r)
	SetDashboardRouter(r)
	SetOCRRouter(r, &controller.OCRHandler{})
	SetRelayRouter(r)
	SetVideoRouter(r)
	routes := []struct{ method, path string }{
		{"GET", "/api/native-billing/self"}, {"GET", "/api/native-billing/"}, {"GET", "/api/native-billing/test"}, {"POST", "/api/native-billing/test/resolve"},
		{"GET", "/api/oauth/github"}, {"GET", "/api/oauth/telegram/login"}, {"POST", "/api/oauth/state"}, {"GET", "/api/verification"}, {"GET", "/api/reset_password"}, {"POST", "/api/user/reset"},
		{"POST", "/api/user/checkin"}, {"POST", "/api/user/aff_transfer"}, {"POST", "/api/user/chat-credential"}, {"GET", "/api/token/"}, {"POST", "/api/token/"}, {"GET", "/api/usage/token/"}, {"POST", "/api/subscription/admin/bind"},
		{"POST", "/v1/chat/completions"}, {"POST", "/v1/responses"}, {"GET", "/v1/models"}, {"POST", "/pg/chat/completions"}, {"POST", "/compatible-mode/v1/chat/completions"}, {"POST", "/compatible-mode/v1/embeddings"},
		{"POST", "/api/v1/services/embeddings/multimodal-embedding/multimodal-embedding"}, {"POST", "/api/v1/services/rerank/text-rerank/text-rerank"}, {"GET", "/v1beta/models"}, {"GET", "/v1beta/openai/models"}, {"POST", "/v1beta/models/test:generateContent"},
		{"POST", "/mj/submit/imagine"}, {"POST", "/fast/mj/submit/imagine"}, {"POST", "/suno/submit/music"}, {"POST", "/v1/videos"}, {"POST", "/kling/v1/videos/text2video"}, {"POST", "/jimeng/"},
		{"GET", "/dashboard/billing/usage"}, {"GET", "/v1/dashboard/billing/subscription"}, {"POST", "/api/v2/ocr/jobs"}, {"POST", "/v1/ocr"},

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

// Native compatibility contracts opt into standalone mode explicitly.
func enableNativeCompatibility(t *testing.T) {
	t.Helper()
	t.Setenv("UNIHUB_PRODUCT_ENABLED", "false")
	t.Setenv("UNIHUB_PLATFORM_ENABLED", "false")
	t.Setenv("NATIVE_PUBLIC_ACCESS", "true")
	t.Setenv("NATIVE_ADMIN_LISTEN_ADDRESS", "")
}

func TestPublicNativePagesAndPlatformFallback(t *testing.T) {
	enableUniHubBoundary(t)
	r := gin.New()
	r.Use(middleware.NativeWebBoundary())
	r.NoRoute(func(c *gin.Context) { c.String(200, "dashboard SPA") })
	// Registration order is the production order: web then platform/UniHub.
	SetPlatformRouter(r, nil)
	SetUniHubRouter(r, platformaccount.Config{})
	for _, path := range []string{"/sign-up", "/oauth/github", "/forgot-password", "/user/reset", "/subscriptions", "/playground", "/index.html"} {
		req := httptest.NewRequest("GET", path, nil)
		req.Header.Set("Origin", "https://admin.example")
		req.Header.Set("X-Forwarded-For", "127.0.0.1")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		assert.Equal(t, 403, w.Code, path)
		assert.NotContains(t, w.Body.String(), "dashboard SPA")
		private := req.WithContext(context.WithValue(req.Context(), http.LocalAddrContextKey, &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 3001}))
		w = httptest.NewRecorder()
		r.ServeHTTP(w, private)
		assert.Equal(t, 200, w.Code, path)
	}
	for _, path := range []string{"/platform/v1/chat/completions", "/platform/v1/unsupported", "/platform/v1/calls/call"} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest("POST", path, nil))
		assert.Equal(t, 404, w.Code)
		assert.NotContains(t, w.Body.String(), "dashboard SPA")
	}
	for _, path := range []string{"/unihub", "/keys", "/profile", "/pricing", "/api/unihub/config", "/install/", "/assets/application.js", "/static/js/application.js", "/static/css/application.css", "/brand/llmrouter-confluence.svg"} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		assert.Equal(t, 200, w.Code, path)
	}
}

func TestProductShellDirectRoutesRemainRenderOnly(t *testing.T) {
	enableUniHubBoundary(t)
	r := gin.New()
	r.Use(middleware.NativeWebBoundary())
	r.NoRoute(func(c *gin.Context) { c.Header("Content-Type", "text/html"); c.String(200, "product shell") })
	for _, path := range []string{"/sign-in", "/dashboard", "/channels", "/channels/", "/wallet", "/keys", "/403"} {
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest(method, path, nil))
			require.Equal(t, 200, w.Code, "%s %s", method, path)
			require.Contains(t, w.Body.String(), "product shell", "%s %s", method, path)
		}
	}
	for _, path := range []string{"/api/channel/", "/api/user/topup", "/api/token/", "/v1/chat/completions", "/unknown", "/channels/secrets", "/keys/secrets", "/keys/", "/sign-up"} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		require.Equal(t, 403, w.Code, path)
		require.NotContains(t, w.Body.String(), "product shell", path)
	}
	for _, path := range []string{"/channels", "/keys"} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest("POST", path, nil))
		require.Equal(t, 403, w.Code, path)
	}
}
