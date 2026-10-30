package router

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	userkit "github.com/NeoMei/UniHub/packages/userkit-go"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/internal/platformaccount"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/oauth"
	"github.com/QuantumNous/new-api/service"
	"github.com/stretchr/testify/require"
)

func TestCentralOAuthBindLoginAndCredentialRevocation(t *testing.T) {
	f := newUniHubRegressionFixture(t)
	require.NoError(t, model.RegisterProductFundingModels(f.db))
	require.NoError(t, f.db.AutoMigrate(&model.CentralAuthState{}, &model.CentralLoginCredential{}, &model.CentralSessionCredential{}, &model.NativeLoginCredential{}, &model.ProductAuthLifecycle{}, &model.ProductBrokerSessionProof{}, &model.AuthFlow{}, &model.Log{}))
	oldLog := model.LOG_DB
	model.LOG_DB = f.db
	t.Cleanup(func() { model.LOG_DB = oldLog })
	user := model.User{Username: "central-oauth", AffCode: "central-oauth", Password: "unused", Role: 1, Status: 1, AuthVersion: 1, FundingRealm: model.NativeFundingLocalOnly, FundingRealmEvidence: "reviewed", NativePassword: true}
	require.NoError(t, f.db.Create(&user).Error)
	var hub *httptest.Server
	identityUnavailable := false
	identityStatus := 0
	identityCalls := 0
	hub = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/internal/v1/product-runtime/identity/browser/source/verify" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		identityCalls++
		if identityStatus != 0 {
			w.WriteHeader(identityStatus)
			return
		}
		if identityUnavailable {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		access := userkit.Access{Issuer: hub.URL, Subject: "central-subject", User: userkit.User{ID: "central-subject"}, ProductID: "llmrouter", InstanceID: "default", Active: true, Version: 1, ExpiresAt: time.Now().Unix() + 600, Permissions: []string{"product:use"}}
		identity := userkit.ProductIdentity{Binding: userkit.RuntimeBinding{ID: "bound", LocalUserID: strconv.Itoa(user.Id), UserID: "central-subject", ProductID: "llmrouter", InstanceID: "default"}, Access: access, ProductRole: 1, AuthVersion: 7}
		data, err := common.Marshal(identity)
		require.NoError(t, err)
		_, err = w.Write(data)
		require.NoError(t, err)
	}))
	t.Cleanup(hub.Close)
	for key, value := range map[string]string{"UNIHUB_INDEPENDENT_CENTRAL_REALM_ENABLED": "true", "UNIHUB_PRODUCT_RUNTIME_ENABLED": "true", "UNIHUB_PRODUCT_API_ORIGIN": hub.URL, "UNIHUB_PRODUCT_PUBLIC_ORIGIN": hub.URL, "UNIHUB_PRODUCT_RUNTIME_TOKEN": strings.Repeat("s", 32), "UNIHUB_PRODUCT_ID": "llmrouter", "UNIHUB_PRODUCT_INSTANCE_ID": "default", "UNIHUB_PRODUCT_CLIENT_ID": "client", "UNIHUB_PRODUCT_REDIRECT_URI": "https://dashboard.test/api/unihub/callback", "NATIVE_PUBLIC_ACCESS": "true"} {
		t.Setenv(key, value)
	}
	_, err := model.BindUniHubIdentity(platformaccount.Identity{Issuer: hub.URL, User: platformaccount.User{ID: "central-subject"}}, user.Id)
	require.NoError(t, err)
	require.NoError(t, f.db.Create(&model.CentralAuthState{UserID: user.Id, AuthVersion: 7, AccessVersion: 1, Active: true, BindingID: "bound", Issuer: hub.URL, Subject: "central-subject", ProductID: "llmrouter", InstanceID: "default"}).Error)
	central, err := service.CreateVerifiedCentralLoginSession(user.Id, 7, "product_broker", time.Now().Unix()+600, "", "")
	require.NoError(t, err)
	access := userkit.Access{Issuer: hub.URL, Subject: "central-subject", User: userkit.User{ID: "central-subject"}, ProductID: "llmrouter", InstanceID: "default", Active: true, ExpiresAt: time.Now().Unix() + 600}
	config := platformaccount.Config{PublicOrigin: hub.URL, ProductID: "llmrouter", InstanceID: "default", ClientID: "client"}
	priorConfig, err := platformaccount.Current()
	require.NoError(t, err)
	platformaccount.Configure(config)
	t.Cleanup(func() { platformaccount.Configure(priorConfig) })
	require.NoError(t, model.BindBrokerSourceProof(central.Session.SID, user.Id, access, config, "ubsp_"+strings.Repeat("a", 64), access.ExpiresAt))
	oauth.Register("central-fixture", &nativeRealmOAuthProvider{})
	t.Cleanup(func() { oauth.Unregister("central-fixture") })
	send := func(method, path, token, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "https://dashboard.test"+path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", "https://dashboard.test")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		out := httptest.NewRecorder()
		f.router.ServeHTTP(out, req)
		return out
	}
	issuePAT := func() string {
		response := send("GET", "/api/user/token", central.AccessToken, "")
		var data struct {
			Success bool   `json:"success"`
			Data    string `json:"data"`
		}
		require.NoError(t, common.Unmarshal(response.Body.Bytes(), &data))
		require.True(t, data.Success, response.Body.String())
		require.NotEmpty(t, data.Data)
		return data.Data
	}
	firstPAT := issuePAT()
	require.Equal(t, http.StatusNoContent, send("GET", "/native-control", firstPAT, "").Code)
	secondPAT := issuePAT()
	require.NotEqual(t, http.StatusNoContent, send("GET", "/native-control", firstPAT, "").Code)
	require.Equal(t, http.StatusNoContent, send("GET", "/native-control", secondPAT, "").Code)
	request := httptest.NewRequest(http.MethodDelete, "https://dashboard.test/api/user/token", nil)
	request.Header.Set("Authorization", "Bearer "+central.AccessToken)
	request.Header.Set("Origin", "https://dashboard.test")
	revokedPAT := httptest.NewRecorder()
	f.router.ServeHTTP(revokedPAT, request)
	require.Contains(t, revokedPAT.Body.String(), `"success":true`)
	require.NotEqual(t, http.StatusNoContent, send("GET", "/native-control", secondPAT, "").Code)
	state := send("POST", "/api/oauth/state", central.AccessToken, `{"provider":"central-fixture","intent":"bind"}`)
	var flow struct {
		Success bool `json:"success"`
		Data    struct {
			Token string `json:"flow_token"`
		} `json:"data"`
	}
	require.NoError(t, common.Unmarshal(state.Body.Bytes(), &flow))
	require.True(t, flow.Success, state.Body.String())
	bound := send("GET", "/api/oauth/central-fixture?state="+flow.Data.Token+"&code=local", central.AccessToken, "")
	require.Contains(t, bound.Body.String(), `"success":true`)
	state = send("POST", "/api/oauth/state", "", `{"provider":"central-fixture","intent":"login"}`)
	require.NoError(t, common.Unmarshal(state.Body.Bytes(), &flow))
	require.True(t, flow.Success, state.Body.String())
	logged := send("GET", "/api/oauth/central-fixture?state="+flow.Data.Token+"&code=local", "", "")
	var bundle struct {
		Success bool `json:"success"`
		Data    struct {
			AccessToken string `json:"access_token"`
			Session     struct {
				AuthenticationRealm string `json:"authentication_realm"`
			} `json:"session"`
			User struct {
				AuthenticationRealm string `json:"authentication_realm"`
				Username            string `json:"username"`
				Role                int    `json:"role"`
				Quota               *int   `json:"quota"`
			} `json:"user"`
		} `json:"data"`
	}
	require.NoError(t, common.Unmarshal(logged.Body.Bytes(), &bundle))
	require.True(t, bundle.Success, logged.Body.String())
	require.Equal(t, "central-subject", bundle.Data.User.Username)
	require.Equal(t, "CENTRAL", bundle.Data.User.AuthenticationRealm)
	require.Equal(t, "CENTRAL", bundle.Data.Session.AuthenticationRealm)
	require.Equal(t, 1, bundle.Data.User.Role)
	require.Nil(t, bundle.Data.User.Quota)
	id, err := service.ParseAccessToken(bundle.Data.AccessToken)
	require.NoError(t, err)
	_, _, err = service.ValidateLoginSession(id)
	require.NoError(t, err)
	t.Setenv("NATIVE_PUBLIC_ACCESS", "false")
	self := send("GET", "/api/user/self", bundle.Data.AccessToken, "")
	require.Equal(t, http.StatusOK, self.Code, self.Body.String())
	var selfBody struct {
		Data struct {
			AuthenticationRealm string `json:"authentication_realm"`
			Username            string `json:"username"`
			Role                int    `json:"role"`
			Quota               *int   `json:"quota"`
		} `json:"data"`
	}
	require.NoError(t, common.Unmarshal(self.Body.Bytes(), &selfBody))
	require.Equal(t, "central-subject", selfBody.Data.Username)
	require.Equal(t, "CENTRAL", selfBody.Data.AuthenticationRealm)
	require.Equal(t, 1, selfBody.Data.Role)
	require.Nil(t, selfBody.Data.Quota, "central response must not expose native wallet")
	var centralCookie *http.Cookie
	for _, cookie := range logged.Result().Cookies() {
		if cookie.Name == service.CentralRefreshCookieName {
			centralCookie = cookie
		}
	}
	require.NotNil(t, centralCookie)
	native, err := service.CreateNativeLoginSessionAtAuthVersion(user.Id, 1, "password", "", "")
	require.NoError(t, err)
	t.Setenv("NATIVE_PUBLIC_ACCESS", "true")
	optionsReq := httptest.NewRequest(http.MethodGet, "https://dashboard.test/api/user/auth/session-options", nil)
	optionsReq.Header.Set("Origin", "https://dashboard.test")
	optionsReq.Header.Set("Sec-Fetch-Site", "same-origin")
	optionsReq.AddCookie(centralCookie)
	optionsReq.AddCookie(&http.Cookie{Name: service.RefreshCookieName, Value: native.RefreshToken})
	options := httptest.NewRecorder()
	f.router.ServeHTTP(options, optionsReq)
	require.Equal(t, http.StatusOK, options.Code, options.Body.String())
	var choices struct {
		Success bool `json:"success"`
		Data    struct {
			Sessions []struct {
				SID   string `json:"sid"`
				Realm string `json:"realm"`
			} `json:"sessions"`
		} `json:"data"`
	}
	require.NoError(t, common.Unmarshal(options.Body.Bytes(), &choices))
	require.True(t, choices.Success)
	require.Len(t, choices.Data.Sessions, 2)
	require.NotContains(t, options.Body.String(), central.RefreshToken)
	require.NotContains(t, options.Body.String(), native.RefreshToken)
	identityUnavailable = true
	unavailableOptions := httptest.NewRecorder()
	f.router.ServeHTTP(unavailableOptions, optionsReq)
	require.Equal(t, http.StatusOK, unavailableOptions.Code)
	require.Contains(t, unavailableOptions.Body.String(), `"unavailable":true`)
	require.NotContains(t, unavailableOptions.Body.String(), id.SessionID)
	identityUnavailable = false
	optionsReq.Header.Set("Sec-Fetch-Site", "cross-site")
	deniedOptions := httptest.NewRecorder()
	f.router.ServeHTTP(deniedOptions, optionsReq)
	require.Equal(t, http.StatusForbidden, deniedOptions.Code)
	badReq := httptest.NewRequest(http.MethodGet, "https://dashboard.test/api/user/auth/session-options", nil)
	badReq.Header.Set("Origin", "https://dashboard.test")
	badReq.Header.Set("Sec-Fetch-Site", "same-origin")
	badReq.AddCookie(centralCookie)
	badReq.AddCookie(&http.Cookie{Name: service.RefreshCookieName, Value: native.Session.SID + ".invalid"})
	badResponse := httptest.NewRecorder()
	f.router.ServeHTTP(badResponse, badReq)
	require.Equal(t, http.StatusOK, badResponse.Code)
	require.NoError(t, common.Unmarshal(badResponse.Body.Bytes(), &choices))
	require.Len(t, choices.Data.Sessions, 1, "a guessed SID without the refresh secret must not be listed")
	nativeReq := httptest.NewRequest(http.MethodPost, "https://dashboard.test/api/user/auth/refresh", nil)
	nativeReq.Header.Set("Origin", "https://dashboard.test")
	nativeReq.Header.Set("X-Auth-Session", native.Session.SID)
	nativeReq.AddCookie(&http.Cookie{Name: service.RefreshCookieName, Value: native.RefreshToken})
	nativeResponse := httptest.NewRecorder()
	f.router.ServeHTTP(nativeResponse, nativeReq)
	require.Equal(t, http.StatusOK, nativeResponse.Code, nativeResponse.Body.String())
	var nativeBody struct {
		Data struct {
			Session struct {
				AuthenticationRealm string `json:"authentication_realm"`
			} `json:"session"`
			User struct {
				AuthenticationRealm string `json:"authentication_realm"`
				IdentitySource      string `json:"identity_source"`
				UniHubUserID        string `json:"unihub_user_id"`
			} `json:"user"`
		} `json:"data"`
	}
	require.NoError(t, common.Unmarshal(nativeResponse.Body.Bytes(), &nativeBody))
	require.Equal(t, "LOCAL_ONLY", nativeBody.Data.User.AuthenticationRealm)
	require.Equal(t, "LOCAL_ONLY", nativeBody.Data.Session.AuthenticationRealm)
	require.Empty(t, nativeBody.Data.User.IdentitySource, "a native session must not identify as central")
	require.Empty(t, nativeBody.Data.User.UniHubUserID, "native profile must not expose the central identity")
	centralRow, err := model.GetUserSessionBySID(id.SessionID)
	require.NoError(t, err)
	require.Equal(t, model.UserSessionStatusActive, centralRow.Status)
	t.Setenv("NATIVE_PUBLIC_ACCESS", "false")
	ambiguousReq := httptest.NewRequest(http.MethodPost, "https://dashboard.test/api/user/auth/refresh", nil)
	ambiguousReq.Header.Set("Origin", "https://dashboard.test")
	ambiguousReq.AddCookie(centralCookie)
	ambiguousReq.AddCookie(&http.Cookie{Name: service.RefreshCookieName, Value: native.RefreshToken})
	ambiguous := httptest.NewRecorder()
	f.router.ServeHTTP(ambiguous, ambiguousReq)
	require.Equal(t, http.StatusConflict, ambiguous.Code, ambiguous.Body.String())
	require.Contains(t, ambiguous.Body.String(), "AUTH_SESSION_MISMATCH")
	refreshReq := httptest.NewRequest(http.MethodPost, "https://dashboard.test/api/user/auth/refresh", nil)
	refreshReq.Header.Set("X-Auth-Session", id.SessionID)
	refreshReq.Header.Set("Origin", "https://dashboard.test")
	refreshReq.AddCookie(centralCookie)
	refreshReq.AddCookie(&http.Cookie{Name: service.RefreshCookieName, Value: native.RefreshToken})
	refreshed := httptest.NewRecorder()
	f.router.ServeHTTP(refreshed, refreshReq)
	require.Equal(t, http.StatusOK, refreshed.Code, refreshed.Body.String())
	var refreshBody struct {
		Success bool `json:"success"`
		Data    struct {
			User struct {
				Username string `json:"username"`
				Role     int    `json:"role"`
			} `json:"user"`
		} `json:"data"`
	}
	require.NoError(t, common.Unmarshal(refreshed.Body.Bytes(), &refreshBody))
	require.True(t, refreshBody.Success, refreshed.Body.String())
	require.Equal(t, "central-subject", refreshBody.Data.User.Username)
	require.Equal(t, 1, refreshBody.Data.User.Role)
	var currentCookie *http.Cookie
	for _, cookie := range refreshed.Result().Cookies() {
		if cookie.Name == service.CentralRefreshCookieName {
			currentCookie = cookie
		}
	}
	require.NotNil(t, currentCookie)
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusConflict, http.StatusServiceUnavailable} {
		identityStatus = status
		beforeCalls := identityCalls
		faultReq := httptest.NewRequest(http.MethodPost, "https://dashboard.test/api/user/auth/refresh", nil)
		faultReq.Header.Set("X-Auth-Session", id.SessionID)
		faultReq.Header.Set("Origin", "https://dashboard.test")
		faultReq.AddCookie(currentCookie)
		fault := httptest.NewRecorder()
		f.router.ServeHTTP(fault, faultReq)
		require.Greater(t, identityCalls, beforeCalls, "fault must reach the central identity service")
		require.Equal(t, http.StatusServiceUnavailable, fault.Code, "runtime status %d: %s", status, fault.Body.String())
		var rotatedCookie *http.Cookie
		for _, cookie := range fault.Result().Cookies() {
			if cookie.Name == service.CentralRefreshCookieName {
				rotatedCookie = cookie
			}
		}
		require.NotNil(t, rotatedCookie, "the rotated cookie must survive authority failure")
		require.NotEmpty(t, rotatedCookie.Value)
		require.NotEqual(t, currentCookie.Value, rotatedCookie.Value)
		centralRow, err = model.GetUserSessionBySID(id.SessionID)
		require.NoError(t, err)
		require.Equal(t, model.UserSessionStatusActive, centralRow.Status)
		identityStatus = 0
		retryReq := httptest.NewRequest(http.MethodPost, "https://dashboard.test/api/user/auth/refresh", nil)
		retryReq.Header.Set("X-Auth-Session", id.SessionID)
		retryReq.Header.Set("Origin", "https://dashboard.test")
		retryReq.AddCookie(rotatedCookie)
		retry := httptest.NewRecorder()
		f.router.ServeHTTP(retry, retryReq)
		require.Equal(t, http.StatusOK, retry.Code, retry.Body.String())
		currentCookie = nil
		for _, cookie := range retry.Result().Cookies() {
			if cookie.Name == service.CentralRefreshCookieName {
				currentCookie = cookie
			}
		}
		require.NotNil(t, currentCookie)
	}
	require.NoError(t, f.db.Model(&model.User{}).Where("id = ?", user.Id).Update("oidc_id", "replaced").Error)
	_, _, err = service.ValidateLoginSession(id)
	require.Error(t, err)
	revokedReq := httptest.NewRequest(http.MethodPost, "https://dashboard.test/api/user/auth/refresh", nil)
	revokedReq.Header.Set("X-Auth-Session", id.SessionID)
	revokedReq.Header.Set("Origin", "https://dashboard.test")
	revokedReq.AddCookie(currentCookie)
	revoked := httptest.NewRecorder()
	f.router.ServeHTTP(revoked, revokedReq)
	require.Equal(t, http.StatusUnauthorized, revoked.Code, revoked.Body.String())
	var cleared bool
	for _, cookie := range revoked.Result().Cookies() {
		if cookie.Name == service.CentralRefreshCookieName && cookie.MaxAge < 0 {
			cleared = true
		}
	}
	require.True(t, cleared, "confirmed local revocation must clear the affected refresh cookie")
}

func TestReturnToIncludesChosenSessionOnlyOnApplicationPath(t *testing.T) {
	target := returnToWithSession("/dashboard?tab=chat&__auth_session=old#section", "new-session")
	parsed, err := url.Parse(target)
	require.NoError(t, err)
	require.Equal(t, "/dashboard", parsed.Path)
	require.Equal(t, "chat", parsed.Query().Get("tab"))
	require.Equal(t, []string{"new-session"}, parsed.Query()["__auth_session"])
	require.Equal(t, "section", parsed.Fragment)
	require.Equal(t, "/", returnToWithSession("//evil.example/path", "new-session"))
}
