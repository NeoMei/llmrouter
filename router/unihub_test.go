package router

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/internal/platformaccount"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestUniHubProofDoesNotMintLocalSession(t *testing.T) {
	gin.SetMode(gin.TestMode)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/capabilities":
			_, _ = w.Write([]byte(`{"kind":"product","product_id":"llmrouter","client_id":"client"}`))
		case "/v1/me":
			_, _ = w.Write([]byte(`{"id":"immutable","email":"admin@test"}`))
		case "/v1/accounts":
			_, _ = w.Write([]byte(`[{"id":"team","role":"owner"}]`))
		}
	}))
	defer up.Close()
	r := gin.New()
	SetUniHubRouter(r, platformaccount.Config{Enabled: true, APIOrigin: up.URL, PublicOrigin: up.URL, ClientID: "client", RedirectURI: up.URL + "/unihub"})
	for _, body := range []string{"", `{"origin":"https://evil.test"}`} {
		w := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/api/unihub/identity", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer product-proof")
		r.ServeHTTP(w, req)
		if body == "" {
			require.Equal(t, 200, w.Code)
			assert.Contains(t, w.Body.String(), "immutable")
			assert.Empty(t, w.Header().Get("Set-Cookie"))
		} else {
			assert.Equal(t, 400, w.Code)
		}
	}
}
func TestDisabledUniHubConfigAndProof(t *testing.T) {
	r := gin.New()
	SetUniHubRouter(r, platformaccount.Config{})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/api/unihub/config", nil))
	require.Equal(t, 200, w.Code)
	assert.NotContains(t, w.Body.String(), "APIOrigin")
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("POST", "/api/unihub/identity", nil))
	assert.Equal(t, 404, w.Code)
}
func TestNonLocalCapabilitiesCannotReachLocalFinancialNamespace(t *testing.T) {
	previous := model.DB
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.User{}))
	model.DB = db
	t.Cleanup(func() { model.DB = previous })
	r := gin.New()
	paths := []string{"/api/token", "/api/user/token", "/api/user/chat-credential", "/api/user/topup", "/api/subscription/self"}
	for _, path := range paths {
		r.POST(path, middleware.UserAuth(), func(c *gin.Context) { c.Status(204) })
	}
	r.POST("/api/admin", middleware.AdminAuth(), func(c *gin.Context) { c.Status(204) })
	for _, token := range []string{"unihub-product-access", "unihub-id-token", "uh_model_task"} {
		for _, path := range append(paths, "/api/admin") {
			w := httptest.NewRecorder()
			req := httptest.NewRequest("POST", path, nil)
			req.Header.Set("Authorization", "Bearer "+token)
			r.ServeHTTP(w, req)
			assert.Equal(t, http.StatusUnauthorized, w.Code, "%s must not bypass %s", token, path)
		}
	}
}
