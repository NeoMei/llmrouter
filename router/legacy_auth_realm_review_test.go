package router

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	userkit "github.com/NeoMei/UniHub/packages/userkit-go"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/internal/platformaccount"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/bytedance/gopkg/util/gopool"
	"github.com/stretchr/testify/require"
)

func TestLegacyProjectionPasswordSessionRealmAndNativeIndependence(t *testing.T) {
	f := newUniHubRegressionFixture(t)
	require.NoError(t, f.db.AutoMigrate(&model.TwoFA{}, &model.AuthFlow{}, &model.Log{}, &model.ProductAuthLifecycle{}, &model.CentralAuthState{}, &model.NativeLoginCredential{}))
	require.NoError(t, model.RegisterProductFundingModels(f.db))
	oldLog, oldEnabled := model.LOG_DB, common.PasswordLoginEnabled
	model.LOG_DB, common.PasswordLoginEnabled = f.db, true
	t.Cleanup(func() {
		require.Eventually(t, func() bool { return gopool.WorkerCount() == 0 }, time.Second, time.Millisecond)
		model.LOG_DB, common.PasswordLoginEnabled = oldLog, oldEnabled
	})
	hash, err := common.Password2Hash("legacy-test-password")
	require.NoError(t, err)
	user := model.User{Username: "legacy-projected", Password: hash, Role: 1, Status: 1, AuthVersion: 1, AffCode: "legacy-projected"}
	require.NoError(t, f.db.Create(&user).Error)
	var hub *httptest.Server
	runtimeUnavailable := false
	hub = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if runtimeUnavailable {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		access := userkit.Access{Issuer: hub.URL, Subject: "legacy-subject", User: userkit.User{ID: "legacy-subject"}, ProductID: "llmrouter", InstanceID: "default", Active: true, Version: 1, ExpiresAt: time.Now().Unix() + 600, Permissions: []string{"product:use"}}
		identity := userkit.ProductIdentity{Binding: userkit.RuntimeBinding{ID: "binding", LocalUserID: strconv.Itoa(user.Id), UserID: "legacy-subject", ProductID: "llmrouter", InstanceID: "default"}, Access: access, AuthVersion: 1, ProductRole: 1}
		b, e := common.Marshal(identity)
		if e != nil {
			w.WriteHeader(500)
			return
		}
		_, _ = w.Write(b)
	}))
	t.Cleanup(hub.Close)
	for key, value := range map[string]string{"UNIHUB_INDEPENDENT_CENTRAL_REALM_ENABLED": "true", "UNIHUB_PRODUCT_RUNTIME_ENABLED": "true", "UNIHUB_PRODUCT_API_ORIGIN": hub.URL, "UNIHUB_PRODUCT_PUBLIC_ORIGIN": hub.URL, "UNIHUB_PRODUCT_RUNTIME_TOKEN": strings.Repeat("s", 32), "UNIHUB_PRODUCT_ID": "llmrouter", "UNIHUB_PRODUCT_INSTANCE_ID": "default", "UNIHUB_PRODUCT_CLIENT_ID": "client", "UNIHUB_PRODUCT_REDIRECT_URI": "https://dashboard.test/api/unihub/callback", "NATIVE_PUBLIC_ACCESS": "true"} {
		t.Setenv(key, value)
	}
	_, err = model.BindUniHubIdentity(platformaccount.Identity{Issuer: hub.URL, User: platformaccount.User{ID: "legacy-subject"}}, user.Id)
	require.NoError(t, err)
	send := func(method, path, token, body string, cookie *http.Cookie, sid string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "https://dashboard.test"+path, strings.NewReader(body))
		req.Header.Set("Origin", "https://dashboard.test")
		req.Header.Set("Content-Type", "application/json")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		if sid != "" {
			req.Header.Set("X-Auth-Session", sid)
		}
		if cookie != nil {
			req.AddCookie(cookie)
		}
		out := httptest.NewRecorder()
		f.router.ServeHTTP(out, req)
		return out
	}
	type result struct {
		Success bool `json:"success"`
		Data    struct {
			AccessToken string                   `json:"access_token"`
			Session     service.LoginSessionView `json:"session"`
			User        struct {
				IdentitySource string `json:"identity_source"`
				Realm          string `json:"authentication_realm"`
			} `json:"user"`
			Realm   string `json:"authentication_realm"`
			Enabled bool   `json:"enabled"`
		} `json:"data"`
	}
	login := send("POST", "/api/user/login", "", `{"username":"legacy-projected","password":"legacy-test-password"}`, nil, "")
	var logged result
	require.NoError(t, common.Unmarshal(login.Body.Bytes(), &logged))
	require.True(t, logged.Success, login.Body.String())
	require.Equal(t, "LEGACY", logged.Data.Session.AuthenticationRealm)
	require.Equal(t, "LEGACY", logged.Data.User.Realm)
	require.Equal(t, "unihub", logged.Data.User.IdentitySource)
	identity, err := service.ParseAccessToken(logged.Data.AccessToken)
	require.NoError(t, err)
	row, err := model.GetUserSessionBySID(identity.SessionID)
	require.NoError(t, err)
	require.Empty(t, row.AuthRealm)
	self := send("GET", "/api/user/self", logged.Data.AccessToken, "", nil, "")
	var selfBody result
	require.NoError(t, common.Unmarshal(self.Body.Bytes(), &selfBody))
	require.True(t, selfBody.Success, self.Body.String())
	require.Equal(t, "LEGACY", selfBody.Data.Realm)
	twoFA := send("GET", "/api/user/2fa/status", logged.Data.AccessToken, "", nil, "")
	var twoBody result
	require.NoError(t, common.Unmarshal(twoFA.Body.Bytes(), &twoBody))
	require.True(t, twoBody.Success, twoFA.Body.String())
	require.False(t, twoBody.Data.Enabled)
	var cookie *http.Cookie
	for _, c := range login.Result().Cookies() {
		if c.Name == service.RefreshCookieName {
			cookie = c
		}
	}
	require.NotNil(t, cookie)
	options := send("GET", "/api/user/auth/session-options", "", "", cookie, "")
	require.Equal(t, http.StatusOK, options.Code, options.Body.String())
	var optionsBody struct {
		Success bool `json:"success"`
		Data    struct {
			Sessions []struct {
				SID         string `json:"sid"`
				Realm       string `json:"realm"`
				DisplayName string `json:"display_name"`
			} `json:"sessions"`
			Unavailable bool `json:"unavailable"`
		} `json:"data"`
	}
	require.NoError(t, common.Unmarshal(options.Body.Bytes(), &optionsBody))
	require.True(t, optionsBody.Success)
	require.Len(t, optionsBody.Data.Sessions, 1)
	require.Equal(t, identity.SessionID, optionsBody.Data.Sessions[0].SID)
	require.Equal(t, "legacy", optionsBody.Data.Sessions[0].Realm)
	require.NotContains(t, options.Body.String(), cookie.Value)
	badOptions := send("GET", "/api/user/auth/session-options", "", "", &http.Cookie{Name: cookie.Name, Value: identity.SessionID + ".invalid"}, "")
	require.NoError(t, common.Unmarshal(badOptions.Body.Bytes(), &optionsBody))
	require.Empty(t, optionsBody.Data.Sessions)
	runtimeUnavailable = true
	unavailableOptions := send("GET", "/api/user/auth/session-options", "", "", cookie, "")
	require.NoError(t, common.Unmarshal(unavailableOptions.Body.Bytes(), &optionsBody))
	require.Empty(t, optionsBody.Data.Sessions)
	require.True(t, optionsBody.Data.Unavailable)
	runtimeUnavailable = false
	wrongSlot := send("GET", "/api/user/auth/session-options", "", "", &http.Cookie{Name: service.CentralRefreshCookieName, Value: cookie.Value}, "")
	require.NoError(t, common.Unmarshal(wrongSlot.Body.Bytes(), &optionsBody))
	require.Empty(t, optionsBody.Data.Sessions)
	refresh := send("POST", "/api/user/auth/refresh", "", "", cookie, identity.SessionID)
	var refreshed result
	require.NoError(t, common.Unmarshal(refresh.Body.Bytes(), &refreshed))
	require.True(t, refreshed.Success, refresh.Body.String())
	require.Equal(t, "LEGACY", refreshed.Data.Session.AuthenticationRealm)
	require.Equal(t, "LEGACY", refreshed.Data.User.Realm)

	// A disabled central authority for a distinct shared owner does not
	// revoke that owner's independently enrolled local session.
	nativeHash, err := common.Password2Hash("native-test-password")
	require.NoError(t, err)
	native := model.User{Username: "native-shared", Password: nativeHash, Role: 1, Status: 1, AuthVersion: 1, AffCode: "native-shared", FundingRealm: model.NativeFundingLocalOnly, FundingRealmEvidence: "fixture", NativePassword: true}
	require.NoError(t, f.db.Create(&native).Error)
	_, err = model.BindUniHubIdentity(platformaccount.Identity{Issuer: hub.URL, User: platformaccount.User{ID: "disabled-central-subject"}}, native.Id)
	require.NoError(t, err)
	require.NoError(t, f.db.Create(&model.CentralAuthState{UserID: native.Id, AuthVersion: 7, AccessVersion: 1, Active: true, BindingID: "disabled-binding", Issuer: hub.URL, Subject: "disabled-central-subject", ProductID: "llmrouter", InstanceID: "default"}).Error)
	central, err := service.CreateVerifiedCentralLoginSession(native.Id, 7, "product_broker", time.Now().Unix()+600, "", "")
	require.NoError(t, err)
	nativeLogin := send("POST", "/api/user/login", "", `{"username":"native-shared","password":"native-test-password"}`, nil, "")
	var nativeBody result
	require.NoError(t, common.Unmarshal(nativeLogin.Body.Bytes(), &nativeBody))
	require.True(t, nativeBody.Success, nativeLogin.Body.String())
	require.Equal(t, "LOCAL_ONLY", nativeBody.Data.Session.AuthenticationRealm)
	require.Equal(t, "LOCAL_ONLY", nativeBody.Data.User.Realm)
	require.Empty(t, nativeBody.Data.User.IdentitySource)
	require.NoError(t, f.db.Model(&model.CentralAuthState{}).Where("user_id = ?", native.Id).Update("active", false).Error)
	centralSelf := send("GET", "/api/user/self", central.AccessToken, "", nil, "")
	require.Equal(t, http.StatusUnauthorized, centralSelf.Code, centralSelf.Body.String())
	nativeSelf := send("GET", "/api/user/self", nativeBody.Data.AccessToken, "", nil, "")
	var nativeSelfBody result
	require.NoError(t, common.Unmarshal(nativeSelf.Body.Bytes(), &nativeSelfBody))
	require.True(t, nativeSelfBody.Success, nativeSelf.Body.String())
	require.Equal(t, "LOCAL_ONLY", nativeSelfBody.Data.Realm)
	native2FA := send("GET", "/api/user/2fa/status", nativeBody.Data.AccessToken, "", nil, "")
	var native2FABody result
	require.NoError(t, common.Unmarshal(native2FA.Body.Bytes(), &native2FABody))
	require.True(t, native2FABody.Success, native2FA.Body.String())
	var nativeCookie *http.Cookie
	for _, c := range nativeLogin.Result().Cookies() {
		if c.Name == service.RefreshCookieName {
			nativeCookie = c
		}
	}
	require.NotNil(t, nativeCookie)
	nativeRefresh := send("POST", "/api/user/auth/refresh", "", "", nativeCookie, nativeBody.Data.Session.SID)
	var nativeRefreshed result
	require.NoError(t, common.Unmarshal(nativeRefresh.Body.Bytes(), &nativeRefreshed))
	require.True(t, nativeRefreshed.Success, nativeRefresh.Body.String())
	require.Equal(t, "LOCAL_ONLY", nativeRefreshed.Data.Session.AuthenticationRealm)
}
