package router

import (
	"net/http"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/service"
	"github.com/stretchr/testify/require"
)

func TestLegacyUniHubSessionOptionsWithoutNativePublicAccess(t *testing.T) {
	f := newUniHubRegressionFixture(t)
	t.Setenv("UNIHUB_PRODUCT_RUNTIME_ENABLED", "false")
	t.Setenv("UNIHUB_INDEPENDENT_CENTRAL_REALM_ENABLED", "true")
	t.Setenv("NATIVE_PUBLIC_ACCESS", "false")
	bundle, cookie := decodeUniHubBundle(t, f.request(http.MethodPost, "/api/unihub/session", "legacy-public", nil))
	require.Equal(t, "LEGACY", bundle.Data.Session.AuthenticationRealm)

	type optionsResponse struct {
		Success bool `json:"success"`
		Data    struct {
			Sessions []struct {
				SID   string `json:"sid"`
				Realm string `json:"realm"`
			} `json:"sessions"`
			Unavailable bool `json:"unavailable"`
		} `json:"data"`
	}
	read := func(c *http.Cookie) optionsResponse {
		result := f.request(http.MethodGet, "/api/user/auth/session-options", "", c)
		require.Equal(t, http.StatusOK, result.Code, result.Body.String())
		var body optionsResponse
		require.NoError(t, common.Unmarshal(result.Body.Bytes(), &body))
		require.True(t, body.Success)
		return body
	}
	before := f.accessCalls.Load()
	body := read(cookie)
	require.Greater(t, f.accessCalls.Load(), before, "live UniHub source must be rechecked")
	require.Len(t, body.Data.Sessions, 1)
	require.Equal(t, bundle.Data.Session.SID, body.Data.Sessions[0].SID)
	require.Equal(t, "legacy", body.Data.Sessions[0].Realm)
	require.False(t, body.Data.Unavailable)

	before = f.accessCalls.Load()
	bad := read(&http.Cookie{Name: service.RefreshCookieName, Value: bundle.Data.Session.SID + ".forged"})
	require.Empty(t, bad.Data.Sessions)
	require.Equal(t, before, f.accessCalls.Load(), "invalid secret cannot reach source lookup")
	wrong := read(&http.Cookie{Name: service.CentralRefreshCookieName, Value: cookie.Value})
	require.Empty(t, wrong.Data.Sessions)
	f.revoked.Store(true)
	before = f.accessCalls.Load()
	denied := read(cookie)
	require.Greater(t, f.accessCalls.Load(), before)
	require.Empty(t, denied.Data.Sessions)
	require.True(t, denied.Data.Unavailable)
	f.revoked.Store(false)
	f.unavailable.Store(true)
	unavailable := read(cookie)
	require.Empty(t, unavailable.Data.Sessions)
	require.True(t, unavailable.Data.Unavailable)
	f.unavailable.Store(false)
	recovered := read(cookie)
	require.Len(t, recovered.Data.Sessions, 1)
	require.Equal(t, bundle.Data.Session.SID, recovered.Data.Sessions[0].SID)
	refreshed := f.request(http.MethodPost, "/api/user/auth/refresh", "", cookie)
	require.Equal(t, http.StatusOK, refreshed.Code, refreshed.Body.String())
	var refreshBody struct {
		Success bool `json:"success"`
		Data    struct {
			Session service.LoginSessionView `json:"session"`
		} `json:"data"`
	}
	require.NoError(t, common.Unmarshal(refreshed.Body.Bytes(), &refreshBody))
	require.True(t, refreshBody.Success)
	require.Equal(t, bundle.Data.Session.SID, refreshBody.Data.Session.SID)
	require.Equal(t, "LEGACY", refreshBody.Data.Session.AuthenticationRealm)
}
