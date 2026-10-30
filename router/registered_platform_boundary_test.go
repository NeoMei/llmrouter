package router

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/QuantumNous/new-api/internal/platformbilling"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestRegisteredPlatformAliasesReachTheirOwnAdmission(t *testing.T) {
	enableUniHubBoundary(t)
	var hubRequests atomic.Int64
	hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hubRequests.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(hub.Close)
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "boundary.db")), &gorm.Config{})
	require.NoError(t, err)
	adapter, err := platformbilling.New(db, platformbilling.Config{
		Enabled: true, Origin: hub.URL, ServiceToken: "synthetic-boundary-service-token", PrincipalID: 1,
		Group: "platform", MaxInputTokens: 1000, MaxOutputTokens: 100, MaxBodyBytes: 8192,
	})
	require.NoError(t, err)
	r := gin.New()
	// Match production: the web boundary precedes the actual platform router
	// and a UI fallback. A hand-written alias fixture missed this interaction.
	r.Use(middleware.NativeWebBoundary())
	SetPlatformRouter(r, adapter)
	r.POST("/v1/native-only", middleware.NativePublicBoundary(), func(c *gin.Context) { c.Status(http.StatusNoContent) })
	r.NoRoute(func(c *gin.Context) { c.String(http.StatusOK, "dashboard SPA") })

	registered := []struct{ method, path string }{
		{http.MethodPost, "/platform/v1/rerank"},
		{http.MethodPost, "/platform/api/v1/services/rerank/text-rerank/text-rerank"},
		{http.MethodPost, "/platform/v1/multimodal/embeddings"},
		{http.MethodPost, "/platform/api/v1/services/embeddings/multimodal-embedding/multimodal-embedding"},
		{http.MethodPost, "/platform/v1/ocr/jobs"},
		{http.MethodPost, "/platform/api/v2/ocr/jobs"},
		{http.MethodGet, "/platform/api/v2/ocr/jobs/original-call"},
		{http.MethodPost, "/platform/v1/models/gemini-test:generateContent"},
		{http.MethodPost, "/platform/v1beta/models/gemini-test:generateContent"},
	}
	for _, route := range registered {
		t.Run(route.method+route.path, func(t *testing.T) {
			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest(route.method, route.path, nil))
			assert.Equal(t, http.StatusUnauthorized, w.Code, w.Body.String())
			assert.NotContains(t, w.Body.String(), "platform_route_not_found")
			assert.NotContains(t, w.Body.String(), "dashboard SPA")
		})
	}
	for _, route := range []struct{ method, path string }{
		{http.MethodPost, "/platform"},
		{http.MethodGet, "/platform/unknown"},
		{http.MethodPost, "/platform/api/v1/services/rerank/unregistered"},
		{http.MethodGet, "/platform/v1beta/unregistered"},
		{http.MethodPost, "/platform/api/v2/ocr/unregistered"},
		{http.MethodGet, "/platform/%2e%2e/dashboard"},
		{http.MethodPost, "/platform/api/v1/services/rerank/text-rerank/../text-rerank"},
		{http.MethodHead, "/platform/api/v1/services/rerank/text-rerank/text-rerank"},
		{http.MethodGet, "/platform/api/v1/services/rerank/text-rerank/text-rerank"},
		{http.MethodPut, "/platform/api/v1/services/rerank/text-rerank/text-rerank"},
	} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(route.method, route.path, nil))
		assert.Equal(t, http.StatusNotFound, w.Code, "%s %s: %s", route.method, route.path, w.Body.String())
		assert.NotContains(t, w.Body.String(), "dashboard SPA")
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/native-only", nil))
	assert.Equal(t, http.StatusForbidden, w.Code)
	assert.Contains(t, w.Body.String(), "NATIVE_ACCESS_DISABLED")
	// Registered aliases keep the platform deployment gate as well as auth.
	t.Setenv("UNIHUB_PLATFORM_ENABLED", "false")
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/platform/api/v1/services/rerank/text-rerank/text-rerank", nil))
	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
	assert.Zero(t, hubRequests.Load(), "unauthenticated/refused routes must not contact billing or a provider")
	var attempts int64
	require.NoError(t, db.Model(&platformbilling.Attempt{}).Count(&attempts).Error)
	assert.Zero(t, attempts)
}
