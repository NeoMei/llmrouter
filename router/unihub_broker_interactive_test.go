package router

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/stretchr/testify/require"
)

// The broker first probes for a central session. If no session exists, a
// deliberate sign-in must be allowed to show the central identity provider;
// a background sign-in probe must remain silent and never surprise the user.
func TestBrokerLoginRequiredPreservesInteractiveIntent(t *testing.T) {
	f := newUniHubSSOFixture(t, nil)
	require.NoError(t, f.db.AutoMigrate(&model.AuthFlow{}))
	t.Setenv("UNIHUB_PRODUCT_RUNTIME_ENABLED", "true")
	t.Setenv("UNIHUB_ORIGIN", f.issuer)
	t.Setenv("UNIHUB_PRODUCT_RUNTIME_TOKEN", strings.Repeat("s", 32))
	for _, tc := range []struct {
		name, prompt string
		wantSilent   bool
		legacy       bool
	}{
		{"interactive", "", false, false}, {"silent", "none", true, false}, {"legacy flow", "none", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q := url.Values{"return_to": {"/dashboard"}}
			if tc.prompt != "" {
				q.Set("prompt", tc.prompt)
			}
			start := httptest.NewRecorder()
			f.router.ServeHTTP(start, httptest.NewRequest(http.MethodGet, "http://127.0.0.1:58119/api/unihub/login?"+q.Encode(), nil))
			require.Equal(t, http.StatusSeeOther, start.Code)
			target, err := url.Parse(start.Header().Get("Location"))
			require.NoError(t, err)
			require.Equal(t, "/auth/product/authorize", target.Path)
			if tc.legacy {
				flow, err := model.GetAuthFlow(target.Query().Get("state"), model.AuthFlowMatch{Purpose: productBrokerPurpose})
				require.NoError(t, err)
				var data map[string]any
				require.NoError(t, common.UnmarshalJsonStr(flow.Payload, &data))
				delete(data, "silent")
				payload, err := common.Marshal(data)
				require.NoError(t, err)
				require.NoError(t, f.db.Model(&model.AuthFlow{}).Where("id = ?", flow.Id).Update("payload", string(payload)).Error)
			}
			callback := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:58119/api/unihub/callback?"+url.Values{"broker": {"1"}, "error": {"login_required"}, "state": {target.Query().Get("state")}}.Encode(), nil)
			cookies := start.Result().Cookies()
			require.Len(t, cookies, 1)
			callback.AddCookie(cookies[0])
			fallback := httptest.NewRecorder()
			f.router.ServeHTTP(fallback, callback)
			require.Equal(t, http.StatusSeeOther, fallback.Code)
			fallbackURL, err := url.Parse(fallback.Header().Get("Location"))
			require.NoError(t, err)
			require.Equal(t, "/api/unihub/login", fallbackURL.Path)
			require.Equal(t, "/dashboard", fallbackURL.Query().Get("return_to"))
			require.Equal(t, tc.wantSilent, fallbackURL.Query().Get("prompt") == "none")
			second := httptest.NewRecorder()
			f.router.ServeHTTP(second, httptest.NewRequest(http.MethodGet, "http://127.0.0.1:58119"+fallbackURL.String(), nil))
			require.Equal(t, http.StatusSeeOther, second.Code)
			authorize, err := url.Parse(second.Header().Get("Location"))
			require.NoError(t, err)
			require.Equal(t, f.issuer+"/authorize", authorize.Scheme+"://"+authorize.Host+authorize.Path)
			require.Equal(t, tc.wantSilent, authorize.Query().Get("prompt") == "none")
		})
	}
}

func TestInteractiveSSOFailureReturnsToPlatformPanel(t *testing.T) {
	f := newUniHubSSOFixture(t, nil)
	f.unavailable.Store(true)
	response := f.login(context.Background())
	require.Equal(t, http.StatusSeeOther, response.Code)
	location, err := url.Parse(response.Header().Get("Location"))
	require.NoError(t, err)
	require.Equal(t, "/unihub", location.Path)
	require.Equal(t, "1", location.Query().Get("sso_error"))
	require.Equal(t, "/channels", location.Query().Get("redirect"))
}
