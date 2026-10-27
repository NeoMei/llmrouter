package middleware

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func setBoundaryEnv(t *testing.T) {
	t.Helper()
	t.Setenv("UNIHUB_PRODUCT_ENABLED", "true")
	t.Setenv("UNIHUB_PLATFORM_ENABLED", "true")
	t.Setenv("NATIVE_PUBLIC_ACCESS", "false")
	t.Setenv("NATIVE_ADMIN_ORIGIN", "https://admin.example")
	t.Setenv("MANAGEMENT_TOKEN", "secret")
}

func TestNativePublicBoundaryMigrationResponse(t *testing.T) {
	setBoundaryEnv(t)
	r := gin.New()
	r.POST("/api/user/login", NativePublicBoundary(), func(c *gin.Context) { c.Status(http.StatusNoContent) })
	req := httptest.NewRequest(http.MethodPost, "/api/user/login", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload["success"] != false || payload["code"] != "NATIVE_ACCESS_DISABLED" || payload["message"] != "native identity and billing are managed by UniHub" {
		t.Fatalf("unexpected payload: %#v", payload)
	}
}

func TestNativePublicBoundaryMissingProductFailsClosed(t *testing.T) {
	setBoundaryEnv(t)
	t.Setenv("UNIHUB_PRODUCT_ENABLED", "")
	r := gin.New()
	r.POST("/api/user/login", NativePublicBoundary(), func(c *gin.Context) { c.Status(http.StatusNoContent) })
	req := httptest.NewRequest(http.MethodPost, "/api/user/login", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestNativePublicBoundaryPrivateToken(t *testing.T) {
	setBoundaryEnv(t)
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
