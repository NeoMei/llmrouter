package router

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/controller"
	"github.com/QuantumNous/new-api/internal/platformaccount"
	"github.com/QuantumNous/new-api/internal/platformbilling"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestUniHubSessionCreatesNativeBrowserBundle(t *testing.T) {
	oldDB, oldRedis, oldSecret := model.DB, common.RedisEnabled, common.SessionSecret
	db, err := gorm.Open(sqlite.Open(t.TempDir()+"/session.db"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.UserSession{}, &model.UniHubIdentityMapping{}, &model.UniHubSessionBinding{}, &model.Channel{}, &model.Ability{}, &model.ClientModelRoute{}, &model.ClientModelTarget{}))
	model.DB, common.RedisEnabled, common.SessionSecret = db, false, "unihub-session-test-secret"
	t.Cleanup(func() { model.DB, common.RedisEnabled, common.SessionSecret = oldDB, oldRedis, oldSecret })
	var up *httptest.Server
	up = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/capabilities":
			_, _ = w.Write([]byte(`{"kind":"product","product_id":"llmrouter","client_id":"client","instance_id":"default","scopes":["access:read"]}`))
		case "/v1/me/product-access":
			raw, _ := common.Marshal(gin.H{"issuer": up.URL, "subject": "subject-1", "product_id": "llmrouter", "instance_id": "default", "user": gin.H{"id": "subject-1", "name": "Ada", "email": "ada@test"}, "roles": []string{"member"}, "permissions": []string{"product:use"}, "version": 1, "active": true, "expires_at": time.Now().Add(time.Hour).Unix()})
			_, _ = w.Write(raw)
		case "/v1/me":
			_, _ = w.Write([]byte(`{"id":"subject-1","name":"Ada","email":"ada@test"}`))
		case "/v1/accounts":
			_, _ = w.Write([]byte(`[]`))
		}
	}))
	defer up.Close()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	adapter, e := platformbilling.New(db, platformbilling.Config{Enabled: true, Origin: up.URL, ServiceToken: strings.Repeat("f", 32), InstanceID: "default", PrincipalID: 1, Group: "platform", MaxBodyBytes: 8192, MaxInputTokens: 1000, MaxOutputTokens: 100})
	require.NoError(t, e)
	SetModelCatalogRouter(r, adapter, platformbilling.CatalogConfig{})
	SetUniHubRouter(r, platformaccount.Config{Enabled: true, APIOrigin: up.URL, PublicOrigin: up.URL, ClientID: "client", RedirectURI: up.URL + "/unihub"})
	r.GET("/api/user/self", middleware.UserAuth(), controller.GetSelf)
	r.POST("/api/user/topup", middleware.UserAuth(), func(c *gin.Context) { c.Status(http.StatusNoContent) })
	r.POST("/v1/chat/completions", middleware.TokenOrUserAuth(), func(c *gin.Context) { c.Status(http.StatusNoContent) })
	for _, tc := range []struct {
		name, target string
		headers      []string
		body         string
		want         int
	}{
		{"body", "/api/unihub/session", []string{"Bearer proof"}, `{}`, 401},
		{"query", "/api/unihub/session?x=1", []string{"Bearer proof"}, "", 401},
		{"duplicate", "/api/unihub/session", []string{"Bearer proof", "Bearer proof"}, "", 401},
	} {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, tc.target, strings.NewReader(tc.body))
		for _, h := range tc.headers {
			req.Header.Add("Authorization", h)
		}
		r.ServeHTTP(w, req)
		require.Equal(t, tc.want, w.Code, tc.name)
	}
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/unihub/session", nil)
	req.Header.Set("Authorization", "Bearer proof")
	req.Header.Set("Origin", "http://example.com")
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)
	var response struct {
		Success bool `json:"success"`
		Data    struct {
			AccessToken string `json:"access_token"`
			User        struct {
				IdentitySource string `json:"identity_source"`
				UniHubUserID   string `json:"unihub_user_id"`
			} `json:"user"`
		} `json:"data"`
	}
	require.NoError(t, common.Unmarshal(w.Body.Bytes(), &response))
	require.True(t, response.Success)
	require.NotEmpty(t, response.Data.AccessToken)
	require.Equal(t, "unihub", response.Data.User.IdentitySource)
	require.Equal(t, "subject-1", response.Data.User.UniHubUserID)
	require.Contains(t, w.Header().Get("Set-Cookie"), service.RefreshCookieName)
	other, err := platformbilling.New(db, platformbilling.Config{Enabled: true, Origin: up.URL, ServiceToken: strings.Repeat("f", 32), InstanceID: "other", PrincipalID: 1, Group: "platform", MaxBodyBytes: 8192, MaxInputTokens: 1000, MaxOutputTokens: 100})
	require.NoError(t, err)
	wrongInstance := gin.New()
	SetModelCatalogRouter(wrongInstance, other, platformbilling.CatalogConfig{})
	req = httptest.NewRequest(http.MethodGet, "/api/unihub/chat-models", nil)
	req.Header.Set("Authorization", "Bearer "+response.Data.AccessToken)
	w = httptest.NewRecorder()
	wrongInstance.ServeHTTP(w, req)
	require.Equal(t, http.StatusServiceUnavailable, w.Code)
	for _, tc := range []struct {
		path string
		want int
	}{{"/api/unihub/chat-models", 200}, {"/api/user/self", 200}, {"/api/user/topup", 204}, {"/v1/chat/completions", 204}} {
		w = httptest.NewRecorder()
		method := http.MethodGet
		if tc.path != "/api/user/self" && tc.path != "/api/unihub/chat-models" {
			method = http.MethodPost
		}
		req = httptest.NewRequest(method, tc.path, nil)
		req.Header.Set("Authorization", "Bearer "+response.Data.AccessToken)
		r.ServeHTTP(w, req)
		require.Equal(t, tc.want, w.Code, tc.path)
		if tc.path == "/api/unihub/chat-models" {
			require.Contains(t, w.Body.String(), `"product_id":"llmrouter"`)
			require.Contains(t, w.Body.String(), `"instance_id":"default"`)
			require.Contains(t, w.Body.String(), `"admission_mode":"legacy"`)
		}
		if tc.path == "/api/user/self" {
			require.Contains(t, w.Body.String(), `"identity_source":"unihub"`)
		}
	}
}
