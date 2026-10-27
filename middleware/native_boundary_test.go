package middleware

import (
	"github.com/gin-gonic/gin"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNativePublicBoundaryMigrationResponse(t *testing.T) {
	t.Setenv("UNIHUB_PRODUCT_ENABLED", "true")
	t.Setenv("UNIHUB_PLATFORM_ENABLED", "true")
	t.Setenv("NATIVE_PUBLIC_ACCESS", "false")
	t.Setenv("NATIVE_ADMIN_ORIGIN", "https://admin.example")
	t.Setenv("MANAGEMENT_TOKEN", "secret")
	r := gin.New()
	r.POST("/api/user/login", NativePublicBoundary(), func(c *gin.Context) { c.Status(http.StatusNoContent) })
	req := httptest.NewRequest(http.MethodPost, "/api/user/login", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden || rec.Body.String() == "" {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestNativePublicBoundaryPrivateToken(t *testing.T) {
	t.Setenv("UNIHUB_PRODUCT_ENABLED", "true")
	t.Setenv("UNIHUB_PLATFORM_ENABLED", "true")
	t.Setenv("NATIVE_PUBLIC_ACCESS", "false")
	t.Setenv("NATIVE_ADMIN_ORIGIN", "https://admin.example")
	t.Setenv("MANAGEMENT_TOKEN", "secret")
	r := gin.New()
	r.POST("/api/user/login", NativePublicBoundary(), func(c *gin.Context) { c.Status(http.StatusNoContent) })
	req := httptest.NewRequest(http.MethodPost, "/api/user/login", nil)
	req.Header.Set("Authorization", "Bearer secret")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}
