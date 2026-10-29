package router

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/internal/platformbilling"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestManagedModelsDistinguishesAuthorityOutageFromCredentialRejection(t *testing.T) {
	enableUniHubBoundary(t)
	oldDB, oldLimit := model.DB, common.GlobalApiRateLimitEnable
	t.Cleanup(func() { model.DB, common.GlobalApiRateLimitEnable = oldDB, oldLimit })
	common.GlobalApiRateLimitEnable = false
	db, err := gorm.Open(sqlite.Open(t.TempDir()+"/managed-model-errors.db"), &gorm.Config{})
	require.NoError(t, err)
	model.DB = db
	const userKey = "uh_key_sensitive_test_value"
	const authorityBody = "upstream-sensitive-test-response"

	cases := []struct {
		name       string
		upstream   int
		body       string
		connection bool
		wantStatus int
		wantCode   string
	}{
		{"unauthorized", 401, authorityBody, false, 401, "model_credential_rejected"},
		{"forbidden", 403, authorityBody, false, 403, "model_credential_rejected"},
		{"rate limited", 429, authorityBody, false, 429, "model_credential_rejected"},
		{"service failure", 500, authorityBody, false, 503, "platform_billing_unavailable"},
		{"redirect", 302, authorityBody, false, 503, "platform_billing_unavailable"},
		{"invalid success body", 200, authorityBody, false, 503, "platform_billing_unavailable"},
		{"oversized success body", 200, strings.Repeat("x", (1<<20)+1), false, 503, "platform_billing_unavailable"},
		{"invalid claim", 200, `{}`, false, 400, "model_credential_rejected"},
		{"connection failure", 0, "", true, 503, "platform_billing_unavailable"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, "/internal/v1/model-credentials/verify", r.URL.Path)
				w.WriteHeader(tc.upstream)
				_, _ = w.Write([]byte(tc.body))
			}))
			if tc.connection {
				hub.Close()
			} else {
				t.Cleanup(hub.Close)
			}
			adapter, err := platformbilling.New(db, platformbilling.Config{Enabled: true, Origin: hub.URL, ServiceToken: strings.Repeat("s", 32), InstanceID: "fixture", PrincipalID: 1, Group: "platform", MaxBodyBytes: 8192, MaxInputTokens: 1000, MaxOutputTokens: 100})
			require.NoError(t, err)
			engine := gin.New()
			SetManagedKeyDispatch(engine, adapter)
			for _, path := range []string{"/v1/models", "/v1/models/example"} {
				r := httptest.NewRequest(http.MethodGet, path, nil)
				r.Header.Set("Authorization", "Bearer "+userKey)
				w := httptest.NewRecorder()
				engine.ServeHTTP(w, r)
				require.Equal(t, tc.wantStatus, w.Code, w.Body.String())
				var got struct {
					Error struct {
						Code string `json:"code"`
					} `json:"error"`
				}
				require.NoError(t, common.Unmarshal(w.Body.Bytes(), &got))
				assert.Equal(t, tc.wantCode, got.Error.Code)
				assert.NotContains(t, w.Body.String(), userKey)
				assert.NotContains(t, w.Body.String(), authorityBody)
				assert.NotContains(t, w.Body.String(), hub.URL)
			}
		})
	}
}
