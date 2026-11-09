package router

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	userkit "github.com/NeoMei/UniHub/packages/userkit-go"
	"github.com/QuantumNous/new-api/internal/platformaccount"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type centralUserViewRow struct {
	ID          string `json:"id"`
	Username    string `json:"username"`
	DisplayName string `json:"display_name"`
	Email       string `json:"email"`
	Role        int    `json:"role"`
	Status      int    `json:"status"`
	Quota       int    `json:"quota"`
	Group       string `json:"group"`
}

func TestCentralUserViewUsesAuthoritativeDirectoryAndKeepsNativeProfilesPrivate(t *testing.T) {
	f := newUniHubRegressionFixture(t)
	require.NoError(t, f.db.AutoMigrate(&model.ProductAuthLifecycle{}, &model.ProductBrokerSessionProof{}, &model.NativeLoginCredential{}))
	local := model.User{Username: "local-only-owner", AffCode: "local-only-owner", Password: "native-password-hash", Status: 1, Role: 100, AuthVersion: 1, Quota: 98765, FundingRealm: model.NativeFundingLocalOnly, FundingRealmEvidence: "test", NativePassword: true}
	require.NoError(t, f.db.Create(&local).Error)
	var usersCalls, unavailable atomic.Int64
	var rootID atomic.Int64
	const actorSubject = "central-root"
	actorToken := "upa_" + strings.Repeat("a", 64)
	cfg := platformaccount.Config{PublicOrigin: "https://hub.test", ProductID: "llmrouter", InstanceID: "default", ClientID: "client", Enabled: true}
	access := userkit.Access{Issuer: cfg.PublicOrigin, Subject: actorSubject, User: userkit.User{ID: actorSubject}, ProductID: cfg.ProductID, InstanceID: cfg.InstanceID, Active: true, Version: 2, ProductRole: 100, ExpiresAt: time.Now().Add(time.Hour).Unix(), Permissions: []string{"product:use", "user:read", "user:write"}}
	identity := userkit.ProductIdentity{Binding: userkit.RuntimeBinding{ID: "bound", LocalUserID: "1", UserID: actorSubject, ProductID: cfg.ProductID, InstanceID: cfg.InstanceID}, Access: access, AuthVersion: 1, ProductRole: 100, ActorToken: actorToken, ActorExpiresAt: time.Now().Add(time.Minute).Unix()}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/capabilities" || r.URL.Path == "/v1/me/product-access" {
			if r.Header.Get("Authorization") != "Bearer test-central-bearer" {
				w.WriteHeader(401)
				return
			}
		} else if r.Header.Get("Authorization") != "Bearer "+strings.Repeat("r", 32) {
			w.WriteHeader(401)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/internal/v1/product-runtime/identity/") {
			current := identity
			current.Binding.LocalUserID = strconv.FormatInt(rootID.Load(), 10)
			_ = json.NewEncoder(w).Encode(current)
			return
		}
		switch r.URL.Path {
		case "/v1/capabilities":
			_ = json.NewEncoder(w).Encode(map[string]any{"kind": "product", "product_id": cfg.ProductID, "client_id": cfg.ClientID, "instance_id": cfg.InstanceID, "scopes": []string{"access:read"}})
		case "/v1/me/product-access":
			_ = json.NewEncoder(w).Encode(access)
		case "/internal/v1/product-runtime/identity/authenticate":
			require.Empty(t, r.Header.Get("Origin"))
			authenticated := identity
			authenticated.Binding.LocalUserID = strconv.FormatInt(rootID.Load(), 10)
			_ = json.NewEncoder(w).Encode(authenticated)
		case "/internal/v1/product-runtime/users":
			usersCalls.Add(1)
			if unavailable.Load() != 0 {
				w.WriteHeader(503)
				return
			}
			require.Equal(t, "Bearer "+actorToken, r.Header.Get("X-UniHub-Actor-Authorization"))
			managed := []userkit.RuntimeManagedUser{
				{User: userkit.User{ID: "central-a", Name: "Alice", Email: "alice@example.test"}, Assignment: userkit.RuntimeAssignment{ProductID: cfg.ProductID, InstanceID: cfg.InstanceID, Roles: []string{"product_root"}, Active: true, Version: 3}},
				{User: userkit.User{ID: "central-b", Name: "Bob", Email: "bob@example.test"}, Assignment: userkit.RuntimeAssignment{ProductID: cfg.ProductID, InstanceID: cfg.InstanceID, Roles: []string{"member"}, Active: false, Version: 4}},
			}
			_ = json.NewEncoder(w).Encode(managed)
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(upstream.Close)
	for key, value := range map[string]string{
		"UNIHUB_PRODUCT_RUNTIME_ENABLED": "true", "UNIHUB_PRODUCT_RUNTIME_TOKEN": strings.Repeat("r", 32),
		"UNIHUB_PRODUCT_API_ORIGIN": upstream.URL, "UNIHUB_PRODUCT_PUBLIC_ORIGIN": upstream.URL,
		"UNIHUB_PRODUCT_ID": cfg.ProductID, "UNIHUB_PRODUCT_INSTANCE_ID": cfg.InstanceID,
		"UNIHUB_PRODUCT_CLIENT_ID": cfg.ClientID, "UNIHUB_PRODUCT_REDIRECT_URI": "https://dashboard.test/api/unihub/callback",
		"NATIVE_PUBLIC_ACCESS": "true",
	} {
		t.Setenv(key, value)
	}
	platformaccount.Configure(cfg)
	root, err := model.ResolveUniHubProjection(platformaccount.Identity{Issuer: cfg.PublicOrigin, User: platformaccount.User{ID: actorSubject, Name: "NeoMei", Email: "neo@example.test"}})
	require.NoError(t, err)
	identity.Binding.LocalUserID = strconv.Itoa(root.Id)
	rootID.Store(int64(root.Id))
	bundle, err := service.CreateUniHubLoginSession(root.Id, time.Now().Add(time.Hour).Unix(), "", "")
	require.NoError(t, err)
	require.NoError(t, model.BindUniHubSession(bundle.Session.SID, root.Id, access, "test-central-bearer", time.Now().Add(time.Hour).Unix()))
	for _, path := range []string{"/api/user/?p=1&page_size=10", "/api/user/search?keyword=Alice&p=1&page_size=10"} {
		got := f.request(http.MethodGet, path, bundle.AccessToken, nil)
		require.Equal(t, http.StatusOK, got.Code, got.Body.String())
		assert.NotContains(t, got.Body.String(), "local-only-owner")
		assert.NotContains(t, got.Body.String(), "98765")
		assert.NotContains(t, got.Body.String(), "native-password-hash")
		var body struct {
			Success bool `json:"success"`
			Data    struct {
				Total int                  `json:"total"`
				Items []centralUserViewRow `json:"items"`
			} `json:"data"`
		}
		require.NoError(t, json.Unmarshal(got.Body.Bytes(), &body))
		require.True(t, body.Success)
		assert.Equal(t, 2, body.Data.Total)
		require.Len(t, body.Data.Items, 2)
		first, second := body.Data.Items[0], body.Data.Items[1]
		assert.Equal(t, "central-a", first.ID)
		assert.Equal(t, "Alice", first.Username)
		assert.Equal(t, "Alice", first.DisplayName)
		assert.Equal(t, "alice@example.test", first.Email)
		assert.Equal(t, 100, first.Role)
		assert.Equal(t, 1, first.Status)
		assert.Equal(t, "central-b", second.ID)
		assert.Equal(t, 1, second.Role)
		assert.Equal(t, 2, second.Status)
		assert.Zero(t, first.Quota)
		assert.Empty(t, first.Group)
	}
	prior := usersCalls.Load()
	unavailable.Store(1)
	got := f.request(http.MethodGet, "/api/user/?p=1&page_size=10", bundle.AccessToken, nil)
	assert.Equal(t, http.StatusServiceUnavailable, got.Code)
	assert.Equal(t, prior+1, usersCalls.Load())
	assert.NotContains(t, got.Body.String(), "local-only-owner")
	unavailable.Store(0)
}
