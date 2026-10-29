package router

import (
	"encoding/json"
	userkit "github.com/NeoMei/UniHub/packages/userkit-go"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/internal/platformaccount"
	"github.com/QuantumNous/new-api/internal/platformbilling"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestBrowserUsesPublicOriginWithoutChangingPrivatePlatformAndRuntime(t *testing.T) {
	old := model.DB
	db, e := gorm.Open(sqlite.Open(t.TempDir()+"/split-origins.db"), &gorm.Config{})
	require.NoError(t, e)
	require.NoError(t, db.AutoMigrate(&model.AuthFlow{}))
	model.DB = db
	t.Cleanup(func() { model.DB = old })
	for key, value := range map[string]string{
		"UNIHUB_PRODUCT_RUNTIME_ENABLED": "true", "UNIHUB_PRODUCT_ENABLED": "true", "UNIHUB_PLATFORM_ENABLED": "true",
		"UNIHUB_ORIGIN": "https://hub.test:18488", "UNIHUB_PRODUCT_API_ORIGIN": "https://hub.test:18488", "UNIHUB_PRODUCT_PUBLIC_ORIGIN": "https://hub.test",
		"UNIHUB_PRODUCT_RUNTIME_TOKEN": strings.Repeat("s", 32), "UNIHUB_PRODUCT_ID": "llmrouter", "UNIHUB_PRODUCT_INSTANCE_ID": "default",
		"UNIHUB_PRODUCT_CLIENT_ID": "router", "UNIHUB_PRODUCT_REDIRECT_URI": "https://router.test/api/unihub/callback",
		"UNIHUB_MODEL_SERVICE_TOKEN": strings.Repeat("m", 32), "UNIHUB_ROUTING_GROUP": "default", "UNIHUB_ROUTING_PRINCIPAL_ID": "14",
		"UNIHUB_MAX_INPUT_TOKENS": "1000", "UNIHUB_MAX_OUTPUT_TOKENS": "1000", "UNIHUB_MAX_BODY_BYTES": "1048576",
	} {
		t.Setenv(key, value)
	}
	config, e := platformaccount.FromEnv()
	require.NoError(t, e)
	r := gin.New()
	registerUniHubSSO(r, config)
	response := httptest.NewRecorder()
	r.ServeHTTP(response, httptest.NewRequest("GET", "https://router.test/api/unihub/login?prompt=none&return_to=%2Fprofile", nil))
	require.Equal(t, 303, response.Code)
	target, e := url.Parse(response.Header().Get("Location"))
	require.NoError(t, e)
	require.Equal(t, "https://hub.test/auth/product/authorize", target.Scheme+"://"+target.Host+target.Path, "a browser must not follow the private model-service port")
	runtime, e := platformaccount.RuntimeClient()
	require.NoError(t, e)
	require.Equal(t, "https://hub.test:18488", runtime.Scope().Origin)
	legacy, e := platformbilling.ConfigFromEnv(os.Getenv)
	require.NoError(t, e)
	require.Equal(t, "https://hub.test:18488", legacy.Origin)
}

func TestNativeBrowserHandoffRequiresOwnCookieAndLiveSourceSession(t *testing.T) {
	old, redis, secret := model.DB, common.RedisEnabled, common.SessionSecret
	db, e := gorm.Open(sqlite.Open(t.TempDir()+"/handoff.db"), &gorm.Config{})
	require.NoError(t, e)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.UserSession{}, &model.AuthFlow{}, &model.UniHubIdentityMapping{}, &model.ProductAuthLifecycle{}))
	model.DB = db
	common.RedisEnabled = false
	common.SessionSecret = "browser-handoff-test-secret"
	t.Cleanup(func() { model.DB = old; common.RedisEnabled = redis; common.SessionSecret = secret })
	u := model.User{Username: "native", AffCode: "native", AuthVersion: 1, Status: 1}
	require.NoError(t, db.Create(&u).Error)
	_, e = model.BindUniHubIdentity(platformaccount.Identity{Issuer: "https://hub.test", User: platformaccount.User{ID: "central"}}, u.Id)
	require.NoError(t, e)
	native, e := service.CreateLoginSession(u.Id, "password", "127.0.0.1", "test")
	require.NoError(t, e)
	approved := 0
	unavailable := true
	var hub *httptest.Server
	hub = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "Bearer "+strings.Repeat("s", 32), r.Header.Get("Authorization"))
		switch r.URL.Path {
		case "/internal/v1/product-runtime/identity/authenticate":
			if unavailable {
				w.WriteHeader(503)
				return
			}
			json.NewEncoder(w).Encode(userkit.ProductIdentity{Binding: userkit.RuntimeBinding{LocalUserID: strconv.Itoa(u.Id), UserID: "central", ProductID: "llmrouter", InstanceID: "default"}, Access: userkit.Access{Issuer: "https://hub.test", Subject: "central", User: userkit.User{ID: "central"}, ProductID: "llmrouter", InstanceID: "default", Version: 1, ProductRole: 1, Active: true, ExpiresAt: time.Now().Add(time.Minute).Unix(), Permissions: []string{"product:use"}}, ProductRole: 1, AuthVersion: 1, ActorToken: "upa_source"})
		case "/internal/v1/product-runtime/identity/browser/approve":
			require.Equal(t, "Bearer upa_source", r.Header.Get("X-UniHub-Actor-Authorization"))
			var v userkit.ProductBrowserApproval
			require.NoError(t, json.NewDecoder(r.Body).Decode(&v))
			approved++
			json.NewEncoder(w).Encode(userkit.ProductBrowserApprovalReceipt{FinishURL: hub.URL + "/auth/product/finish?" + url.Values{"handoff": {v.HandoffID}, "state": {v.State}}.Encode()})
		default:
			w.WriteHeader(404)
		}
	}))
	defer hub.Close()
	t.Setenv("UNIHUB_PRODUCT_RUNTIME_ENABLED", "true")
	t.Setenv("UNIHUB_ORIGIN", hub.URL)
	t.Setenv("UNIHUB_PRODUCT_RUNTIME_TOKEN", strings.Repeat("s", 32))
	r := gin.New()
	registerUniHubSSO(r, platformaccount.Config{Enabled: true, ClientID: "router", PublicOrigin: hub.URL, RedirectURI: "https://router.test/api/unihub/callback"})
	start := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "https://router.test/api/user/auth/unihub-handoff?return_to=%2Fusers%3Fpage%3D2", nil)
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	req.AddCookie(&http.Cookie{Name: service.RefreshCookieName, Value: native.RefreshToken})
	failed := httptest.NewRecorder()
	r.ServeHTTP(failed, req)
	require.Equal(t, 401, failed.Code)
	var newest *http.Cookie
	for _, v := range failed.Result().Cookies() {
		if v.Name == service.RefreshCookieName {
			newest = v
		}
	}
	require.NotNil(t, newest, "handoff must return refresh rotation before central failure")
	require.NoError(t, db.Model(&model.UserSession{}).Where("sid = ?", native.Session.SID).Update("previous_valid_until", time.Now().Add(-time.Minute).Unix()).Error)
	req = httptest.NewRequest("GET", "https://router.test/api/user/auth/unihub-handoff?return_to=%2Fusers%3Fpage%3D2", nil)
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	req.AddCookie(newest)
	unavailable = false
	r.ServeHTTP(start, req)
	require.Equal(t, 303, start.Code, start.Body.String())
	target, e := url.Parse(start.Header().Get("Location"))
	require.NoError(t, e)
	require.Equal(t, hub.URL+"/auth/product/start", target.Scheme+"://"+target.Host+target.Path)
	state := target.Query().Get("state")
	require.NotEmpty(t, state)
	var cookie *http.Cookie
	for _, c := range start.Result().Cookies() {
		if c.Name == "llmrouter_unihub_handoff" {
			cookie = c
		}
	}
	require.NotNil(t, cookie)
	require.True(t, cookie.HttpOnly)
	require.Equal(t, http.SameSiteLaxMode, cookie.SameSite)
	require.Equal(t, "/api/unihub/callback", cookie.Path)

	// Strict cookies alone do not isolate same-site sibling products.
	foreignStart := httptest.NewRecorder()
	foreignRequest := httptest.NewRequest("GET", "https://router.test/api/user/auth/unihub-handoff?return_to=%2Fusers", nil)
	foreignRequest.Header.Set("Sec-Fetch-Site", "same-site")
	foreignRequest.AddCookie(&http.Cookie{Name: service.RefreshCookieName, Value: native.RefreshToken})
	r.ServeHTTP(foreignStart, foreignRequest)
	require.Equal(t, 403, foreignStart.Code)
	callback := "https://router.test/api/unihub/callback?" + url.Values{"handoff": {"central-one-use"}, "state": {state}}.Encode()
	invalid := httptest.NewRecorder()
	r.ServeHTTP(invalid, httptest.NewRequest("GET", callback, nil))
	require.Equal(t, 401, invalid.Code)
	require.Zero(t, approved)
	success := httptest.NewRecorder()
	request := httptest.NewRequest("GET", callback, nil)
	request.AddCookie(cookie)
	r.ServeHTTP(success, request)
	require.Equal(t, 303, success.Code, success.Body.String())
	require.Equal(t, 1, approved)
	done := httptest.NewRecorder()
	request = httptest.NewRequest("GET", "https://router.test/api/unihub/callback?"+url.Values{"handoff_done": {"1"}, "state": {state}}.Encode(), nil)
	request.AddCookie(cookie)
	r.ServeHTTP(done, request)
	require.Equal(t, 303, done.Code)
	require.Equal(t, "/users?page=2", done.Header().Get("Location"))
	replay := httptest.NewRecorder()
	r.ServeHTTP(replay, request)
	require.Equal(t, 401, replay.Code)
	var renewedCookie *http.Cookie
	for _, value := range start.Result().Cookies() {
		if value.Name == service.RefreshCookieName {
			renewedCookie = value
		}
	}
	require.NotNil(t, renewedCookie)
	secondStart := httptest.NewRecorder()
	secondRequest := httptest.NewRequest("GET", "https://router.test/api/user/auth/unihub-handoff?return_to=%2Fprofile", nil)
	secondRequest.Header.Set("Sec-Fetch-Site", "same-origin")
	secondRequest.AddCookie(renewedCookie)
	r.ServeHTTP(secondStart, secondRequest)
	require.Equal(t, 303, secondStart.Code)
	secondURL, e := url.Parse(secondStart.Header().Get("Location"))
	require.NoError(t, e)
	var secondCookie *http.Cookie
	for _, value := range secondStart.Result().Cookies() {
		if value.Name == productHandoffCookie {
			secondCookie = value
		}
	}
	require.NotNil(t, secondCookie)
	_, e = model.RevokeUserSession(u.Id, native.Session.SID, "test_revocation")
	require.NoError(t, e)
	revokedRequest := httptest.NewRequest("GET", "https://router.test/api/unihub/callback?"+url.Values{"handoff": {"another-one-use"}, "state": {secondURL.Query().Get("state")}}.Encode(), nil)
	revokedRequest.AddCookie(secondCookie)
	revoked := httptest.NewRecorder()
	r.ServeHTTP(revoked, revokedRequest)
	require.Equal(t, 401, revoked.Code)
	require.Equal(t, 1, approved)

}

func TestProductBrowserAuthorizationBindsPKCEAndRejectsForeignCallback(t *testing.T) {
	old := model.DB
	db, e := gorm.Open(sqlite.Open(t.TempDir()+"/broker.db"), &gorm.Config{})
	require.NoError(t, e)
	require.NoError(t, db.AutoMigrate(&model.AuthFlow{}))
	model.DB = db
	t.Cleanup(func() { model.DB = old })
	t.Setenv("UNIHUB_PRODUCT_RUNTIME_ENABLED", "true")
	t.Setenv("UNIHUB_ORIGIN", "https://hub.test")
	t.Setenv("UNIHUB_PRODUCT_RUNTIME_TOKEN", strings.Repeat("s", 32))
	r := gin.New()
	registerUniHubSSO(r, platformaccount.Config{Enabled: true, ClientID: "router", PublicOrigin: "https://hub.test", RedirectURI: "https://router.test/api/unihub/callback"})
	start := httptest.NewRecorder()
	r.ServeHTTP(start, httptest.NewRequest("GET", "https://router.test/api/unihub/login?prompt=none&return_to=%2Fprofile", nil))
	require.Equal(t, 303, start.Code)
	target, e := url.Parse(start.Header().Get("Location"))
	require.NoError(t, e)
	require.Equal(t, "/auth/product/authorize", target.Path)
	require.Equal(t, "S256", target.Query().Get("code_challenge_method"))
	require.Len(t, target.Query().Get("code_challenge"), 43)
	require.NotEmpty(t, target.Query().Get("nonce"))
	require.Equal(t, "https://router.test/api/unihub/callback", target.Query().Get("redirect_uri"))
	invalid := httptest.NewRecorder()
	r.ServeHTTP(invalid, httptest.NewRequest("GET", "https://router.test/api/unihub/callback?broker=1&state="+url.QueryEscape(target.Query().Get("state"))+"&code=stolen", nil))
	require.Equal(t, 401, invalid.Code)
	var cookie *http.Cookie
	for _, c := range start.Result().Cookies() {
		if c.Name == "llmrouter_unihub_broker" {
			cookie = c
		}
	}
	require.NotNil(t, cookie)
	failed := httptest.NewRecorder()
	request := httptest.NewRequest("GET", "https://router.test/api/unihub/callback?broker=1&error=login_required&state="+url.QueryEscape(target.Query().Get("state")), nil)
	request.AddCookie(cookie)
	r.ServeHTTP(failed, request)
	require.Equal(t, 303, failed.Code)
	fallback, e := url.Parse(failed.Header().Get("Location"))
	require.NoError(t, e)
	require.Equal(t, "/api/unihub/login", fallback.Path)
	require.Equal(t, "1", fallback.Query().Get("broker_fallback"))
	require.Equal(t, "/profile", fallback.Query().Get("return_to"))
	final := httptest.NewRecorder()
	r.ServeHTTP(final, httptest.NewRequest("GET", "https://router.test"+fallback.String(), nil))
	require.Equal(t, 303, final.Code)
	require.Contains(t, final.Header().Get("Location"), "unihub_silent=done")
}

func TestBrokerExchangeCreatesOriginalSessionAndKeepsExistingLocalUserID(t *testing.T) {
	old, redis, secret := model.DB, common.RedisEnabled, common.SessionSecret
	db, e := gorm.Open(sqlite.Open(t.TempDir()+"/jit.db"), &gorm.Config{})
	require.NoError(t, e)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.UserSession{}, &model.AuthFlow{}, &model.UniHubIdentityMapping{}, &model.ProductFundingAccount{}, &model.ProductAuthLifecycle{}, &model.ProductBrokerSessionProof{}))
	model.DB = db
	common.RedisEnabled = false
	common.SessionSecret = "browser-jit-session-test-secret"
	t.Cleanup(func() { model.DB = old; common.RedisEnabled = redis; common.SessionSecret = secret })
	existing := model.User{Username: "native-existing", AffCode: "native-existing", Password: "original-hash", AuthVersion: 7, Status: 1, Quota: 0}
	require.NoError(t, db.Create(&existing).Error)
	_, e = model.BindUniHubIdentity(platformaccount.Identity{Issuer: "https://hub.test", User: platformaccount.User{ID: "central-existing"}}, existing.Id)
	require.NoError(t, e)
	require.NoError(t, db.Model(&model.User{}).Where("id = ?", existing.Id).Update("status", common.UserStatusDisabled).Error)
	exchanges := 0
	binds := 0
	giveSourceProof := true
	var hub *httptest.Server
	access := userkit.Access{Issuer: "https://hub.test", Subject: "central-existing", User: userkit.User{ID: "central-existing", Name: "Original"}, ProductID: "llmrouter", InstanceID: "default", Version: 1, Active: true, ProductRole: 1, Permissions: []string{"product:use"}, ExpiresAt: time.Now().Add(120 * time.Second).Unix()}
	binding := userkit.RuntimeBinding{ID: "binding", LocalUserID: strconv.Itoa(existing.Id), UserID: access.Subject, ProductID: "llmrouter", InstanceID: "default", PersonalAccountID: "personal"}
	var verifier, nonce string
	hub = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "Bearer "+strings.Repeat("s", 32), r.Header.Get("Authorization"))
		switch r.URL.Path {
		case "/internal/v1/product-runtime/identity/browser/exchange":
			exchanges++
			require.Empty(t, r.Header.Get("X-UniHub-Actor-Authorization"))
			var in userkit.ProductBrowserExchange
			require.NoError(t, json.NewDecoder(r.Body).Decode(&in))
			require.Equal(t, "one-use-code", in.Code)
			require.Equal(t, verifier, in.Verifier)
			require.Equal(t, nonce, in.Nonce)
			require.Equal(t, "https://router.test/api/unihub/callback", in.RedirectURI)
			grant := userkit.ProductBrowserGrant{Access: access, ProofToken: "ubp_once", ExpiresAt: access.ExpiresAt}
			if giveSourceProof {
				grant.SourceProofToken = "ubsp_" + strings.Repeat("a", 64)
				grant.SourceProofExpiresAt = time.Now().Add(time.Hour).Unix()
			}
			json.NewEncoder(w).Encode(grant)
		case "/internal/v1/product-runtime/bindings":
			binds++
			require.Equal(t, "Bearer ubp_once", r.Header.Get("X-UniHub-Actor-Authorization"))
			var in userkit.BindRequest
			require.NoError(t, json.NewDecoder(r.Body).Decode(&in))
			require.Equal(t, strconv.Itoa(existing.Id), in.LocalUserID)
			json.NewEncoder(w).Encode(binding)
		case "/internal/v1/product-runtime/identity/" + strconv.Itoa(existing.Id):
			json.NewEncoder(w).Encode(userkit.ProductIdentity{Binding: binding, Access: access, ProductRole: 1, AuthVersion: 7})
		case "/internal/v1/product-runtime/users/" + strconv.Itoa(existing.Id) + "/access":
			json.NewEncoder(w).Encode(userkit.RuntimeAccess{Binding: binding, Access: access})
		default:
			t.Errorf("unexpected broker request %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer hub.Close()
	t.Setenv("UNIHUB_PRODUCT_RUNTIME_ENABLED", "true")
	t.Setenv("UNIHUB_ORIGIN", hub.URL)
	t.Setenv("UNIHUB_PRODUCT_RUNTIME_TOKEN", strings.Repeat("s", 32))
	r := gin.New()
	registerUniHubSSO(r, platformaccount.Config{Enabled: true, ProductID: "llmrouter", InstanceID: "default", ClientID: "router", PublicOrigin: "https://hub.test", RedirectURI: "https://router.test/api/unihub/callback"})
	start := httptest.NewRecorder()
	r.ServeHTTP(start, httptest.NewRequest("GET", "https://router.test/api/unihub/login?prompt=none&return_to=%2Fprofile", nil))
	require.Equal(t, 303, start.Code)
	destination, e := url.Parse(start.Header().Get("Location"))
	require.NoError(t, e)
	state := destination.Query().Get("state")
	flow, e := model.GetAuthFlow(state, model.AuthFlowMatch{Purpose: productBrokerPurpose})
	require.NoError(t, e)
	var saved productBrowserState
	require.NoError(t, common.UnmarshalJsonStr(flow.Payload, &saved))
	verifier = saved.Verifier
	nonce = saved.Nonce
	var cookie *http.Cookie
	for _, c := range start.Result().Cookies() {
		if c.Name == productBrokerCookie {
			cookie = c
		}
	}
	require.NotNil(t, cookie)
	request := httptest.NewRequest("GET", "https://router.test/api/unihub/callback?"+url.Values{"broker": {"1"}, "code": {"one-use-code"}, "state": {state}}.Encode(), nil)
	request.AddCookie(cookie)
	done := httptest.NewRecorder()
	r.ServeHTTP(done, request)
	require.Equal(t, 303, done.Code, done.Body.String())
	require.Equal(t, "/profile", done.Header().Get("Location"))
	require.Equal(t, 1, exchanges)
	require.Equal(t, 1, binds)
	var refresh *http.Cookie
	for _, c := range done.Result().Cookies() {
		if c.Name == service.RefreshCookieName {
			refresh = c
		}
	}
	require.NotNil(t, refresh)
	sid, ok := service.RefreshTokenSID(refresh.Value)
	require.True(t, ok)
	session, e := model.GetUserSessionBySID(sid)
	require.NoError(t, e)
	require.Equal(t, existing.Id, session.UserID)
	require.Equal(t, "product_broker", session.LoginMethod)
	require.Greater(t, session.ExpiresAt, time.Now().Add(time.Hour).Unix())
	renewed, user, e := service.RefreshLoginSession(refresh.Value, sid, "127.0.0.1", "test")
	require.NoError(t, e)
	require.Equal(t, existing.Id, user.Id)
	require.NotEmpty(t, renewed.AccessToken)
	var count int64
	require.NoError(t, db.Model(&model.User{}).Count(&count).Error)
	require.EqualValues(t, 1, count)
	stored, e := model.GetUserById(existing.Id, true)
	require.NoError(t, e)
	require.Equal(t, "original-hash", stored.Password)
	require.EqualValues(t, 7, stored.AuthVersion)
	replay := httptest.NewRecorder()
	r.ServeHTTP(replay, request)
	require.Equal(t, 401, replay.Code)
	require.Equal(t, 1, exchanges)
	// An old central code without a source proof cannot leave a usable new SID.
	giveSourceProof = false
	start = httptest.NewRecorder()
	r.ServeHTTP(start, httptest.NewRequest("GET", "https://router.test/api/unihub/login?prompt=none&return_to=%2Fprofile", nil))
	require.Equal(t, http.StatusSeeOther, start.Code)
	destination, e = url.Parse(start.Header().Get("Location"))
	require.NoError(t, e)
	state = destination.Query().Get("state")
	flow, e = model.GetAuthFlow(state, model.AuthFlowMatch{Purpose: productBrokerPurpose})
	require.NoError(t, e)
	require.NoError(t, common.UnmarshalJsonStr(flow.Payload, &saved))
	verifier, nonce = saved.Verifier, saved.Nonce
	cookie = nil
	for _, c := range start.Result().Cookies() {
		if c.Name == productBrokerCookie {
			cookie = c
		}
	}
	require.NotNil(t, cookie)
	request = httptest.NewRequest("GET", "https://router.test/api/unihub/callback?"+url.Values{"broker": {"1"}, "code": {"one-use-code"}, "state": {state}}.Encode(), nil)
	request.AddCookie(cookie)
	done = httptest.NewRecorder()
	r.ServeHTTP(done, request)
	require.Equal(t, http.StatusSeeOther, done.Code)
	assert.NotEqual(t, "/profile", done.Header().Get("Location"))
	for _, c := range done.Result().Cookies() {
		assert.NotEqual(t, service.RefreshCookieName, c.Name)
	}
	var active int64
	require.NoError(t, db.Model(&model.UserSession{}).Where("user_id = ? AND status = ?", existing.Id, model.UserSessionStatusActive).Count(&active).Error)
	assert.EqualValues(t, 1, active, "partial SID must be revoked without changing the original session")
}
