package middleware

import (
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func runCORSRequest(t *testing.T, origin string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(CORS())
	router.GET("/test", func(c *gin.Context) { c.String(http.StatusOK, "ok") })
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("Origin", origin)
	router.ServeHTTP(w, req)
	return w
}

func TestCORSAllowsConfiguredOriginOnly(t *testing.T) {
	t.Setenv("TRUSTED_ORIGINS", "https://admin.example.com")
	allowed := runCORSRequest(t, "https://admin.example.com")
	require.Equal(t, "https://admin.example.com", allowed.Header().Get("Access-Control-Allow-Origin"))
	require.Equal(t, "true", allowed.Header().Get("Access-Control-Allow-Credentials"))

	denied := runCORSRequest(t, "https://evil.example")
	require.Empty(t, denied.Header().Get("Access-Control-Allow-Origin"))
}

func TestCORSAllowsMultipleExactOrigins(t *testing.T) {
	t.Setenv("TRUSTED_ORIGINS", "https://admin.example.com, https://app.example.com")
	require.Equal(t, "https://admin.example.com", runCORSRequest(t, "https://admin.example.com").Header().Get("Access-Control-Allow-Origin"))
	require.Equal(t, "https://app.example.com", runCORSRequest(t, "https://app.example.com").Header().Get("Access-Control-Allow-Origin"))
	require.Empty(t, runCORSRequest(t, "https://other.example").Header().Get("Access-Control-Allow-Origin"))
}

func TestProductionRejectsMissingTrustedOrigins(t *testing.T) {
	t.Setenv("RUN_MODE", "production")
	t.Setenv("TRUSTED_ORIGINS", "")
	require.Error(t, ValidateCORSConfig())
}

func TestProductionRejectsWildcardCredentialedCORS(t *testing.T) {
	t.Setenv("RUN_MODE", "production")
	t.Setenv("TRUSTED_ORIGINS", "*")
	require.Error(t, ValidateCORSConfig())
}

func TestProductionRejectsHttpOrigin(t *testing.T) {
	t.Setenv("RUN_MODE", "production")
	t.Setenv("TRUSTED_ORIGINS", "http://localhost:3000")
	require.Error(t, ValidateCORSConfig())
}

func TestValidateCORSConfigAllowsLocalhostDevelopment(t *testing.T) {
	t.Setenv("RUN_MODE", "development")
	t.Setenv("TRUSTED_ORIGINS", "http://localhost:3000, http://127.0.0.1:3000")
	require.NoError(t, ValidateCORSConfig())
}

func TestValidateCORSConfigRejectsOriginWithPath(t *testing.T) {
	t.Setenv("TRUSTED_ORIGINS", "https://admin.example.com/path")
	require.Error(t, ValidateCORSConfig())
}

func TestMain(m *testing.M) {
	// Ensure environment is clean for tests that do not set it explicitly.
	os.Unsetenv("TRUSTED_ORIGINS")
	os.Unsetenv("RUN_MODE")
	m.Run()
}
