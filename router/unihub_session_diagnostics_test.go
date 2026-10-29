package router

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/internal/platformaccount"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestUniHubSessionFailureLogsOnlySafeStage(t *testing.T) {
	const secret = "sensitive-token-should-never-be-logged"
	t.Setenv("UNIHUB_PRODUCT_RUNTIME_ENABLED", "false")
	previous := common.CriticalRateLimitEnable
	common.CriticalRateLimitEnable = false
	t.Cleanup(func() { common.CriticalRateLimitEnable = previous })
	var logs bytes.Buffer
	old := gin.DefaultErrorWriter
	gin.DefaultErrorWriter = &logs
	t.Cleanup(func() { gin.DefaultErrorWriter = old })
	for _, stage := range []string{"capabilities", "product_access"} {
		t.Run(stage, func(t *testing.T) {
			logs.Reset()
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/v1/capabilities" && stage == "product_access" {
					_, _ = w.Write([]byte(`{"kind":"product","product_id":"llmrouter","client_id":"client","instance_id":"default","scopes":["access:read"]}`))
					return
				}
				w.WriteHeader(http.StatusServiceUnavailable)
				_, _ = w.Write([]byte(secret + " private@example.test"))
			}))
			defer upstream.Close()
			r := gin.New()
			SetUniHubRouter(r, platformaccount.Config{Enabled: true, APIOrigin: upstream.URL, PublicOrigin: upstream.URL, ClientID: "client", RedirectURI: "https://dashboard.test/unihub"})
			request := httptest.NewRequest(http.MethodPost, "https://dashboard.test/api/unihub/session", nil)
			request.Header.Set("Origin", "https://dashboard.test")
			request.Header.Set("Authorization", "Bearer "+secret)
			response := httptest.NewRecorder()
			r.ServeHTTP(response, request)
			require.Equal(t, http.StatusUnauthorized, response.Code)
			require.Empty(t, strings.TrimSpace(response.Body.String()))
			require.Contains(t, logs.String(), "unihub_session_failure stage=resolve")
			require.NotContains(t, logs.String(), secret)
			require.NotContains(t, logs.String(), "private@example.test")
		})
	}
}

func TestUniHubSessionRuntimeBindFailureLogsOnlyStage(t *testing.T) {
	const secret = "client-sensitive-token"
	const runtimeSecret = "runtime-service-token-at-least-32-chars"
	t.Setenv("UNIHUB_PRODUCT_RUNTIME_ENABLED", "true")
	t.Setenv("UNIHUB_PRODUCT_RUNTIME_TOKEN", runtimeSecret)
	oldDB, oldRedis, oldSecret, oldCritical := model.DB, common.RedisEnabled, common.SessionSecret, common.CriticalRateLimitEnable
	db, err := gorm.Open(sqlite.Open(t.TempDir()+"/bind.db"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.UniHubIdentityMapping{}))
	model.DB, common.RedisEnabled, common.SessionSecret, common.CriticalRateLimitEnable = db, false, "session-secret-at-least-16-bytes", false
	t.Cleanup(func() {
		model.DB, common.RedisEnabled, common.SessionSecret, common.CriticalRateLimitEnable = oldDB, oldRedis, oldSecret, oldCritical
	})
	var logs bytes.Buffer
	oldWriter := gin.DefaultErrorWriter
	gin.DefaultErrorWriter = &logs
	t.Cleanup(func() { gin.DefaultErrorWriter = oldWriter })
	var upstream *httptest.Server
	upstream = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/capabilities":
			_, _ = w.Write([]byte(`{"kind":"product","product_id":"llmrouter","client_id":"client","instance_id":"default","scopes":["access:read"]}`))
		case "/v1/me/product-access":
			raw, _ := common.Marshal(gin.H{"issuer": upstream.URL, "subject": "private-user", "product_id": "llmrouter", "instance_id": "default", "user": gin.H{"id": "private-user", "name": "private@example.test"}, "active": true, "version": 1, "expires_at": time.Now().Add(time.Hour).Unix()})
			_, _ = w.Write(raw)
		case "/internal/v1/product-runtime/bindings":
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(secret + " " + runtimeSecret + " private@example.test"))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer upstream.Close()
	t.Setenv("UNIHUB_ORIGIN", upstream.URL)
	t.Setenv("UNIHUB_PRODUCT_API_ORIGIN", upstream.URL)
	r := gin.New()
	SetUniHubRouter(r, platformaccount.Config{Enabled: true, APIOrigin: upstream.URL, PublicOrigin: upstream.URL, ClientID: "client", RedirectURI: "https://dashboard.test/unihub"})
	request := httptest.NewRequest(http.MethodPost, "https://dashboard.test/api/unihub/session", nil)
	request.Header.Set("Origin", "https://dashboard.test")
	request.Header.Set("Authorization", "Bearer "+secret)
	response := httptest.NewRecorder()
	r.ServeHTTP(response, request)
	require.Equal(t, http.StatusUnauthorized, response.Code)
	require.Empty(t, strings.TrimSpace(response.Body.String()))
	require.Contains(t, logs.String(), "unihub_session_failure stage=runtime_bind")
	for _, sensitive := range []string{secret, runtimeSecret, "private@example.test", "private-user"} {
		require.NotContains(t, logs.String(), sensitive)
	}
}
