package router

import (
	"github.com/QuantumNous/new-api/middleware"
	"github.com/stretchr/testify/require"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestSilentOIDCUsesOriginalEntryAndSingleSafeReturn(t *testing.T) {
	f := newUniHubSSOFixture(t, nil)
	w := httptest.NewRecorder()
	f.router.ServeHTTP(w, httptest.NewRequest("GET", "http://127.0.0.1:58119/api/unihub/login?prompt=none&return_to=%2Fusers", nil))
	require.Equal(t, http.StatusSeeOther, w.Code)
	target, e := url.Parse(w.Header().Get("Location"))
	require.NoError(t, e)
	require.Equal(t, "none", target.Query().Get("prompt"))
	r := httptest.NewRequest("GET", "http://127.0.0.1:58119/api/unihub/callback?"+url.Values{"error": {"interaction_required"}, "state": {target.Query().Get("state")}}.Encode(), nil)
	r.AddCookie(w.Result().Cookies()[0])
	failed := httptest.NewRecorder()
	f.router.ServeHTTP(failed, r)
	location, e := url.Parse(failed.Header().Get("Location"))
	require.NoError(t, e)
	require.Equal(t, "/sign-in", location.Path)
	require.Equal(t, "done", location.Query().Get("unihub_silent"))
	require.Equal(t, "/users", location.Query().Get("redirect"))
	for _, p := range []string{"/users", "/profile", "/playground", "/system-settings/security", "/models/deployments", "/docs"} {
		require.True(t, middleware.IsProductApplicationPath(p), p)
	}
	for _, p := range []string{"//evil.test", "/api/user/self", "/platform/v1/calls", "/sign-in", "/sign-up"} {
		require.False(t, middleware.IsProductApplicationPath(p), p)
	}
}
