package router

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUniHubBFFBoundaries(t *testing.T) {
	f := newUniHubRegressionFixture(t)
	b, _ := decodeUniHubBundle(t, f.request("POST", "/api/unihub/session", "viewer", nil))
	access := f.request("GET", "/api/unihub/access", b.Data.AccessToken, nil)
	require.Equal(t, 200, access.Code)
	var current struct {
		CSRF string `json:"csrf_token"`
	}
	require.NoError(t, common.Unmarshal(access.Body.Bytes(), &current))
	require.NotEmpty(t, current.CSRF)
	assert.NotContains(t, access.Body.String(), "Bearer")
	assert.Equal(t, 200, f.request("GET", "/api/unihub/bff/v1/accounts", b.Data.AccessToken, nil).Code)
	send := func(path, origin, csrf, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "https://dashboard.test"+path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+b.Data.AccessToken)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", origin)
		req.Header.Set("X-UniHub-CSRF", csrf)
		req.Header.Set("Idempotency-Key", "same-original-key")
		w := httptest.NewRecorder()
		f.router.ServeHTTP(w, req)
		return w
	}
	for _, tc := range []struct {
		path, origin, csrf, body string
		want                     int
	}{
		{"/api/unihub/bff/v1/accounts", "https://dashboard.test", current.CSRF, `{"name":"test"}`, 200},
		{"/api/unihub/bff/v1/accounts", "https://dashboard.test", "", `{}`, 403},
		{"/api/unihub/bff/v1/accounts", "https://attacker.test", current.CSRF, `{}`, 403},
		{"/api/unihub/bff/v1/user-management/users", "https://dashboard.test", current.CSRF, `{}`, 403},
		{"/api/unihub/bff/v1/identity/token", "https://dashboard.test", current.CSRF, `{}`, 403},
		{"/api/unihub/bff/v1/accounts", "https://dashboard.test", current.CSRF, strings.Repeat("x", (1<<20)+1), 413},
	} {
		assert.Equal(t, tc.want, send(tc.path, tc.origin, tc.csrf, tc.body).Code, tc.path)
	}
	req := httptest.NewRequest("GET", "https://dashboard.test/api/unihub/bff/v1/accounts", nil)
	req.Header.Add("Authorization", "Bearer "+b.Data.AccessToken)
	req.Header.Add("Authorization", "Bearer other")
	w := httptest.NewRecorder()
	f.router.ServeHTTP(w, req)
	assert.NotEqual(t, http.StatusOK, w.Code)
	assert.Equal(t, 502, f.request("GET", "/api/unihub/bff/v1/accounts?redirect=1", b.Data.AccessToken, nil).Code)
	assert.Equal(t, 502, f.request("GET", "/api/unihub/bff/v1/accounts?oversized=1", b.Data.AccessToken, nil).Code)
	assert.Equal(t, 503, send("/api/unihub/bff/v1/accounts/personal/recharges", "https://dashboard.test", current.CSRF, `{"amount":"1"}`).Code)
	assert.Equal(t, int32(1), f.forwarded.Load(), "financial write was retried")
	f.unavailable.Store(true)
	assert.Equal(t, 401, f.request("GET", "/api/unihub/access", b.Data.AccessToken, nil).Code)
	assert.Equal(t, 401, f.request("GET", "/api/unihub/bff/v1/accounts", b.Data.AccessToken, nil).Code)
	f.unavailable.Store(false)
	// A second valid SID for this same user must not accept another SID's ciphertext.
	other, _ := decodeUniHubBundle(t, f.request("POST", "/api/unihub/session", "viewer", nil))
	var first, second model.UniHubSessionBinding
	require.NoError(t, f.db.First(&first, "sid = ?", b.Data.Session.SID).Error)
	require.NoError(t, f.db.First(&second, "sid = ?", other.Data.Session.SID).Error)
	require.NoError(t, f.db.Model(&second).Update("ciphertext", first.Ciphertext).Error)
	assert.Equal(t, 401, f.request("GET", "/api/unihub/access", other.Data.AccessToken, nil).Code)
	// Restore nothing: an unbound legacy UniHub session must reauthenticate.
	require.NoError(t, f.db.Delete(&first).Error)
	assert.Equal(t, 401, f.request("GET", "/api/unihub/access", b.Data.AccessToken, nil).Code)
}
