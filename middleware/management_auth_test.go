package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func runManagementRequest(t *testing.T, runMode, configuredToken, authorization string) int {
	t.Helper()
	t.Setenv("RUN_MODE", runMode)
	t.Setenv("MANAGEMENT_TOKEN", configuredToken)
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.GET("/metrics", ManagementAuth(), func(c *gin.Context) {
		c.Status(http.StatusNoContent)
	})
	request := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	request.Header.Set("Authorization", authorization)
	response := httptest.NewRecorder()
	engine.ServeHTTP(response, request)
	return response.Code
}

func TestManagementAuthRequiresProductionBearerToken(t *testing.T) {
	token := "management-token-that-is-at-least-32-bytes"
	if got := runManagementRequest(t, "production", token, ""); got != http.StatusUnauthorized {
		t.Fatalf("missing token status = %d, want %d", got, http.StatusUnauthorized)
	}
	if got := runManagementRequest(t, "production", token, "Bearer wrong"); got != http.StatusUnauthorized {
		t.Fatalf("wrong token status = %d, want %d", got, http.StatusUnauthorized)
	}
	if got := runManagementRequest(t, "production", token, "Bearer "+token); got != http.StatusNoContent {
		t.Fatalf("valid token status = %d, want %d", got, http.StatusNoContent)
	}
}

func TestManagementAuthKeepsDevelopmentCompatibility(t *testing.T) {
	if got := runManagementRequest(t, "development", "", ""); got != http.StatusNoContent {
		t.Fatalf("development status = %d, want %d", got, http.StatusNoContent)
	}
}
