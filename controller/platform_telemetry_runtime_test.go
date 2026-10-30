package controller

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	userkit "github.com/NeoMei/UniHub/packages/userkit-go"
	"github.com/QuantumNous/new-api/internal/platformaccount"
	"github.com/QuantumNous/new-api/service/authz"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestProductTelemetryRejectsInvalidRuntimePrincipal(t *testing.T) {
	previous, err := platformaccount.Current()
	require.NoError(t, err)
	t.Cleanup(func() { platformaccount.Configure(previous) })
	platformaccount.Configure(platformaccount.Config{Enabled: false, PublicOrigin: "https://authority.example", ProductID: "llmrouter", InstanceID: "default"})
	t.Setenv("UNIHUB_PRODUCT_RUNTIME_ENABLED", "true")
	valid := userkit.Principal{Access: userkit.Access{Issuer: "https://authority.example", Subject: "actor", ProductID: "llmrouter", InstanceID: "default", Active: true, ExpiresAt: time.Now().Add(time.Minute).Unix(), Permissions: []string{"product:use"}}}
	for _, tc := range []struct {
		name   string
		modify func(*userkit.Principal)
	}{
		{"inactive", func(p *userkit.Principal) { p.Access.Active = false }},
		{"expired", func(p *userkit.Principal) { p.Access.ExpiresAt = time.Now().Add(-time.Minute).Unix() }},
		{"missing_actor", func(p *userkit.Principal) { p.Access.Subject = "" }},
		{"wrong_issuer", func(p *userkit.Principal) { p.Access.Issuer = "https://other.example" }},
		{"wrong_product", func(p *userkit.Principal) { p.Access.ProductID = "other" }},
		{"wrong_instance", func(p *userkit.Principal) { p.Access.InstanceID = "other" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			principal := valid
			tc.modify(&principal)
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodGet, "/api/log/self", nil)
			authz.SetProductPrincipal(c, principal)
			_, projected, ok := productTelemetry(c, false)
			assert.True(t, projected)
			assert.False(t, ok)
			assert.Equal(t, http.StatusUnauthorized, recorder.Code)
		})
	}
	for _, runtime := range []string{"false", "true"} {
		t.Run("native_runtime_"+runtime, func(t *testing.T) {
			t.Setenv("UNIHUB_PRODUCT_RUNTIME_ENABLED", runtime)
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodGet, "/api/log/self", nil)
			_, projected, ok := productTelemetry(c, false)
			assert.True(t, projected)
			assert.False(t, ok)
			assert.Equal(t, http.StatusServiceUnavailable, recorder.Code)
		})
	}
	t.Run("native_marker_without_session_cannot_read_local_history", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		c.Request = httptest.NewRequest(http.MethodGet, "/api/log/self", nil)
		c.Set("authenticated_native_realm", true)
		c.Set("id", 42)
		_, projected, ok := productTelemetry(c, false)
		assert.True(t, projected)
		assert.False(t, ok)
		assert.Equal(t, http.StatusServiceUnavailable, recorder.Code)
	})
	t.Run("central_missing_principal_never_falls_back_to_local_history", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		c.Request = httptest.NewRequest(http.MethodGet, "/api/log/self", nil)
		c.Set("authenticated_central_realm", true)
		_, projected, ok := productTelemetry(c, false)
		assert.True(t, projected)
		assert.False(t, ok)
		assert.Equal(t, http.StatusServiceUnavailable, recorder.Code)
	})
	t.Run("central_actor_cannot_use_native_root_privilege", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		c.Request = httptest.NewRequest(http.MethodGet, "/api/log/", nil)
		c.Set("role", 100)
		authz.SetProductPrincipal(c, valid)
		_, projected, ok := productTelemetry(c, true)
		assert.True(t, projected)
		assert.False(t, ok)
		assert.Equal(t, http.StatusForbidden, recorder.Code)
	})
	t.Run("disabled_modes_reject_principal", func(t *testing.T) {
		t.Setenv("UNIHUB_PRODUCT_RUNTIME_ENABLED", "false")
		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		c.Request = httptest.NewRequest(http.MethodGet, "/api/log/self", nil)
		authz.SetProductPrincipal(c, valid)
		_, projected, ok := productTelemetry(c, false)
		assert.True(t, projected)
		assert.False(t, ok)
		assert.Equal(t, http.StatusUnauthorized, recorder.Code)
	})
}
