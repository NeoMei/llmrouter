package router

import (
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/QuantumNous/new-api/service"
	"github.com/stretchr/testify/require"
)

func requireSessionRedirect(t *testing.T, response *httptest.ResponseRecorder, expected string) {
	t.Helper()
	target, err := url.Parse(response.Header().Get("Location"))
	require.NoError(t, err)
	query := target.Query()
	sid := query.Get("__auth_session")
	require.NotEmpty(t, sid)
	query.Del("__auth_session")
	target.RawQuery = query.Encode()
	require.Equal(t, expected, target.String())
	var match bool
	for _, cookie := range response.Result().Cookies() {
		if cookie.Name == service.RefreshCookieName || cookie.Name == service.CentralRefreshCookieName {
			cookieSID, ok := service.RefreshTokenSID(cookie.Value)
			match = match || (ok && cookieSID == sid)
		}
	}
	require.True(t, match, "redirect selection must identify the issued cookie")
}
