package router

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/internal/platformaccount"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type uniHubRegressionFixture struct {
	revoked        *atomic.Bool
	forwarded      *atomic.Int32
	unavailable    *atomic.Bool
	accessCalls    *atomic.Int32
	failAccessCall *atomic.Int32
	db             *gorm.DB
	router         *gin.Engine
}
type uniHubRegressionBundle struct {
	Success bool `json:"success"`
	Data    struct {
		AccessToken     string                   `json:"access_token"`
		TokenType       string                   `json:"token_type"`
		AccessExpiresAt int64                    `json:"access_expires_at"`
		User            model.User               `json:"user"`
		Session         service.LoginSessionView `json:"session"`
	} `json:"data"`
}

func newUniHubRegressionFixture(t *testing.T) *uniHubRegressionFixture {
	enableUniHubBoundary(t)
	t.Helper()
	oldDB, oldRedis, oldSecret := model.DB, common.RedisEnabled, common.SessionSecret
	oldSecure, oldTrusted := common.SessionCookieSecure, common.SessionCookieTrustedURLs
	oldCritical, oldGlobal, oldMode := common.CriticalRateLimitEnable, common.GlobalApiRateLimitEnable, gin.Mode()
	db := nativeRealmFixtureDB(t, t.TempDir()+"/matrix.db")
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.UserSession{}, &model.UniHubIdentityMapping{}, &model.UniHubSessionBinding{}))
	sqlDB, err := db.DB()
	require.NoError(t, err)
	model.DB, common.RedisEnabled, common.SessionSecret = db, false, "unihub-regression-test-secret"
	nativeRealmFixtureCache(t)
	common.SessionCookieSecure, common.SessionCookieTrustedURLs = true, nil
	common.CriticalRateLimitEnable, common.GlobalApiRateLimitEnable = false, false
	gin.SetMode(gin.TestMode)
	t.Cleanup(func() {
		model.DB, common.RedisEnabled, common.SessionSecret = oldDB, oldRedis, oldSecret
		common.SessionCookieSecure, common.SessionCookieTrustedURLs = oldSecure, oldTrusted
		common.CriticalRateLimitEnable, common.GlobalApiRateLimitEnable = oldCritical, oldGlobal
		gin.SetMode(oldMode)
		require.NoError(t, sqlDB.Close())
	})
	revoked := &atomic.Bool{}
	forwarded := &atomic.Int32{}
	unavailable := &atomic.Bool{}
	accessCalls := &atomic.Int32{}
	failAccessCall := &atomic.Int32{}
	var upstream *httptest.Server
	upstream = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Origin") != "" && r.Header.Get("Origin") != "https://dashboard.test" {
			w.WriteHeader(403)
			return
		}
		subject := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if unavailable.Load() {
			w.WriteHeader(503)
			return
		}
		switch r.URL.Path {
		case "/v1/capabilities":
			client := "client"
			if subject == "wrong-client" {
				client = "other"
			}
			_ = json.NewEncoder(w).Encode(gin.H{"kind": "product", "product_id": "llmrouter", "client_id": client, "instance_id": "default", "scopes": []string{"access:read"}})
		case "/v1/me/product-access":
			call := accessCalls.Add(1)
			if failAccessCall.Load() == call {
				w.WriteHeader(503)
				return
			}
			if revoked.Load() {
				w.WriteHeader(403)
				return
			}
			permissions := []string{"product:use"}
			if subject == "product-viewer" || subject == "product-admin" {
				permissions = append(permissions, "channel:read", "catalog:read", "log:read", "routing:read", "settings:read")
			}
			if subject == "product-admin" {
				permissions = append(permissions, "channel:operate", "channel:write", "channel:sensitive_write", "catalog:write", "routing:write", "settings:write")
			}
			if subject == "sensitive" {
				permissions = append(permissions, "channel:sensitive_write")
			}
			if subject == "viewer" || subject == "operator" || subject == "admin" || subject == "sensitive" {
				permissions = append(permissions, "channel:read")
			}
			if subject == "operator" || subject == "admin" || subject == "sensitive" {
				permissions = append(permissions, "channel:operate")
			}
			if subject == "admin" || subject == "sensitive" {
				permissions = append(permissions, "channel:write")
			}
			_ = json.NewEncoder(w).Encode(gin.H{"issuer": upstream.URL, "subject": subject, "product_id": "llmrouter", "instance_id": "default", "user": gin.H{"id": subject, "name": "Same", "email": "same@test"}, "roles": []string{subject}, "permissions": permissions, "active": true, "version": 1, "expires_at": time.Now().Add(time.Hour).Unix()})
		case "/v1/me":
			_ = json.NewEncoder(w).Encode(gin.H{"id": subject, "name": "Same", "email": "same@test", "role": "root"})
		case "/v1/accounts":
			if r.URL.Query().Has("redirect") {
				w.Header().Set("Location", "https://not-followed.invalid")
				w.WriteHeader(302)
				return
			}
			if r.URL.Query().Has("oversized") {
				_, _ = w.Write([]byte(strings.Repeat("x", (4<<20)+1)))
				return
			}

			if r.Method == "POST" {
				if r.Header.Get("Idempotency-Key") != "same-original-key" {
					w.WriteHeader(400)
					return
				}
				_, _ = w.Write([]byte(`{"id":"created"}`))
				return
			}
			_, _ = w.Write([]byte(`[]`))
		default:
			if strings.HasSuffix(r.URL.Path, "/recharges") && r.Method == "POST" {
				forwarded.Add(1)
				w.WriteHeader(503)
				_, _ = w.Write([]byte(`{"error":"response_unknown"}`))
				return
			}
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(upstream.Close)
	r := gin.New()
	SetUniHubRouter(r, platformaccount.Config{Enabled: true, APIOrigin: upstream.URL, PublicOrigin: upstream.URL, ClientID: "client", RedirectURI: "https://dashboard.test/unihub"})
	SetApiRouter(r)
	// Sentinels verify that authentication terminates before a protected handler runs.
	reached := func(c *gin.Context) { c.Status(http.StatusNoContent) }
	r.POST("/v1/chat/completions", middleware.TokenOrUserAuth(), reached)
	r.GET("/optional-native", middleware.TryUserAuth(), reached)
	r.GET("/native-control", middleware.UserAuth(), reached)
	r.GET("/admin-control", middleware.AdminAuth(), reached)
	return &uniHubRegressionFixture{db: db, router: r, revoked: revoked, forwarded: forwarded, unavailable: unavailable, accessCalls: accessCalls, failAccessCall: failAccessCall}
}

func (f *uniHubRegressionFixture) request(method, path, token string, cookie *http.Cookie) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, "https://dashboard.test"+path, nil)
	req.Header.Set("Origin", "https://dashboard.test")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if cookie != nil {
		req.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	f.router.ServeHTTP(w, req)
	return w
}
func decodeUniHubBundle(t *testing.T, w *httptest.ResponseRecorder) (uniHubRegressionBundle, *http.Cookie) {
	t.Helper()
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var b uniHubRegressionBundle
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &b))
	require.True(t, b.Success)
	require.NotEmpty(t, b.Data.AccessToken)
	require.Equal(t, "Bearer", b.Data.TokenType)
	require.Greater(t, b.Data.AccessExpiresAt, time.Now().Unix())
	require.NotEmpty(t, b.Data.Session.SID)
	require.Equal(t, model.UniHubIdentitySource, b.Data.User.IdentitySource)
	require.Contains(t, w.Header().Get("Cache-Control"), "no-store")
	for _, cookie := range w.Result().Cookies() {
		if cookie.Name == service.RefreshCookieName {
			require.True(t, cookie.HttpOnly)
			require.True(t, cookie.Secure)
			return b, cookie
		}
	}
	t.Fatal("refresh cookie missing")
	return b, nil
}

func TestUniHubSessionProofRegressionMatrix(t *testing.T) {
	f := newUniHubRegressionFixture(t)
	for _, tc := range []struct {
		name, token, origin, query, body string
		duplicate                        bool
		want                             int
	}{
		{name: "wrong_client", token: "wrong-client", origin: "https://dashboard.test", want: 401},
		{name: "wrong_origin", token: "subject", origin: "https://attacker.test", want: 403},
		{name: "same_host_wrong_scheme", token: "subject", origin: "http://dashboard.test", want: 403},
		{name: "missing_origin", token: "subject", want: 403},
		{name: "identity_body", token: "subject", origin: "https://dashboard.test", body: `{"user_id":"override"}`, want: 401},
		{name: "identity_query", token: "subject", origin: "https://dashboard.test", query: "?user_id=override", want: 401},
		{name: "duplicate_bearer", token: "subject", origin: "https://dashboard.test", duplicate: true, want: 401},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("POST", "https://dashboard.test/api/unihub/session"+tc.query, strings.NewReader(tc.body))
			if tc.origin != "" {
				req.Header.Set("Origin", tc.origin)
			}
			req.Header.Set("Authorization", "Bearer "+tc.token)
			if tc.duplicate {
				req.Header.Add("Authorization", "Bearer "+tc.token)
			}
			w := httptest.NewRecorder()
			f.router.ServeHTTP(w, req)
			require.Equal(t, tc.want, w.Code)
			require.Empty(t, w.Result().Cookies())
		})
	}
	for _, table := range []any{&model.User{}, &model.UserSession{}, &model.UniHubIdentityMapping{}} {
		var count int64
		require.NoError(t, f.db.Model(table).Count(&count).Error)
		require.Zero(t, count)
	}
}

func TestUniHubSessionStableSubjectAndRefreshLogout(t *testing.T) {
	f := newUniHubRegressionFixture(t)
	local := model.User{Username: "Same", DisplayName: "Same", Email: "same@test", Password: "local-password", AffCode: "local", Quota: 42}
	require.NoError(t, f.db.Create(&local).Error)
	first, _ := decodeUniHubBundle(t, f.request("POST", "/api/unihub/session", "subject-one", nil))
	again, cookie := decodeUniHubBundle(t, f.request("POST", "/api/unihub/session", "subject-one", nil))
	other, _ := decodeUniHubBundle(t, f.request("POST", "/api/unihub/session", "subject-two", nil))
	require.Equal(t, first.Data.User.Id, again.Data.User.Id)
	require.NotEqual(t, first.Data.Session.SID, again.Data.Session.SID)
	require.NotEqual(t, first.Data.User.Id, other.Data.User.Id)
	require.NotEqual(t, local.Id, first.Data.User.Id)
	require.NotEqual(t, local.Id, other.Data.User.Id)
	for _, b := range []uniHubRegressionBundle{first, again, other} {
		var user model.User
		require.NoError(t, f.db.First(&user, b.Data.User.Id).Error)
		require.Equal(t, common.RoleCommonUser, user.Role)
		require.Zero(t, user.Quota)
		require.Zero(t, user.UsedQuota)
		require.Zero(t, user.RequestCount)
		require.Zero(t, user.AffCount)
		require.Zero(t, user.AffQuota)
		require.Zero(t, user.AffHistoryQuota)
		require.Zero(t, user.InviterId)
		require.Empty(t, user.Password)
		require.Nil(t, user.AccessToken)
		require.Empty(t, user.StripeCustomer)
		require.Equal(t, user.AffCode, b.Data.User.AffCode)
	}
	refreshed, rotatedCookie := decodeUniHubBundle(t, f.request("POST", "/api/user/auth/refresh", "", cookie))
	require.Equal(t, again.Data.User.Id, refreshed.Data.User.Id)
	require.Equal(t, "subject-one", refreshed.Data.User.UniHubUserID)
	require.Equal(t, again.Data.Session.SID, refreshed.Data.Session.SID)
	self := f.request("GET", "/api/user/self", refreshed.Data.AccessToken, nil)
	require.Equal(t, 200, self.Code)
	require.Contains(t, self.Body.String(), `"unihub_user_id":"subject-one"`)
	require.Equal(t, 200, f.request("POST", "/api/user/auth/logout", refreshed.Data.AccessToken, rotatedCookie).Code)
	require.Equal(t, 401, f.request("GET", "/api/user/self", refreshed.Data.AccessToken, nil).Code)
	require.Equal(t, 401, f.request("POST", "/api/user/auth/refresh", "", rotatedCookie).Code)
	var count int64
	require.NoError(t, f.db.Model(&model.UniHubIdentityMapping{}).Count(&count).Error)
	require.EqualValues(t, 2, count)
	require.NoError(t, f.db.First(&local, local.Id).Error)
	require.Equal(t, 42, local.Quota)
}

func TestUniHubSessionIdentityLinkAndBusinessStateRegressionMatrix(t *testing.T) {
	for _, tc := range []struct {
		field             string
		value             any
		corrupt, disabled bool
	}{
		{"status", common.UserStatusDisabled, false, true}, {"deleted_at", time.Unix(1, 0), true, true},
		{"identity_source", "", true, false}, {"unihub_user_id", "replaced", true, false},
		{"role", common.RoleRootUser, false, false}, {"stripe_customer", "cus_product", false, false},
		{"aff_code", "original-product-aff", false, false}, {"aff_count", 1, false, false}, {"aff_quota", 1, false, false},
		{"aff_history", 1, false, false}, {"inviter_id", 1, false, false}, {"quota", 123, false, false},
		{"used_quota", 77, false, false}, {"request_count", 8, false, false}, {"password", "product-password", false, false}, {"access_token", "product-pat", false, false},
	} {
		t.Run(tc.field, func(t *testing.T) {
			f := newUniHubRegressionFixture(t)
			bundle, cookie := decodeUniHubBundle(t, f.request("POST", "/api/unihub/session", "subject", nil))
			require.NoError(t, f.db.Model(&model.User{}).Where("id = ?", bundle.Data.User.Id).UpdateColumn(tc.field, tc.value).Error)
			self := f.request("GET", "/api/user/self", bundle.Data.AccessToken, nil)
			if tc.corrupt || tc.disabled {
				require.GreaterOrEqual(t, self.Code, 400)
				require.GreaterOrEqual(t, f.request("POST", "/api/unihub/session", "subject", nil).Code, 400)
			} else {
				require.Equal(t, 200, self.Code, self.Body.String())
				var data struct {
					Data map[string]any `json:"data"`
				}
				require.NoError(t, json.Unmarshal(self.Body.Bytes(), &data))
				require.EqualValues(t, common.RoleCommonUser, data.Data["role"], "local role must not overwrite central authority")
				if tc.field == "quota" || tc.field == "used_quota" || tc.field == "request_count" {
					require.EqualValues(t, tc.value, data.Data[tc.field])
				}
				again, _ := decodeUniHubBundle(t, f.request("POST", "/api/unihub/session", "subject", nil))
				require.Equal(t, bundle.Data.User.Id, again.Data.User.Id)
				f.revoked.Store(true)
				require.Equal(t, 401, f.request("GET", "/api/user/self", bundle.Data.AccessToken, nil).Code)
				f.revoked.Store(false)
			}
			_, e := model.CheckUniHubProjection(bundle.Data.User.Id)
			if tc.corrupt {
				require.ErrorIs(t, e, model.ErrUniHubProjectionInvalid)
			} else {
				require.NoError(t, e)
			}
			require.Equal(t, 200, f.request("POST", "/api/user/auth/logout", bundle.Data.AccessToken, cookie).Code)
			require.GreaterOrEqual(t, f.request("POST", "/api/user/auth/refresh", "", cookie).Code, 400)
			var count int64
			require.NoError(t, f.db.Unscoped().Model(&model.User{}).Count(&count).Error)
			require.EqualValues(t, 1, count)
		})
	}
}

func TestUniHubSessionNativeAuthRegressionMatrix(t *testing.T) {
	f := newUniHubRegressionFixture(t)
	bundle, _ := decodeUniHubBundle(t, f.request("POST", "/api/unihub/session", "subject", nil))
	for _, tc := range []struct{ method, path string }{
		{"POST", "/api/user/topup"}, {"GET", "/api/user/token"}, {"POST", "/api/token/"},
		{"POST", "/api/user/chat-credential"}, {"PUT", "/api/user/self"},
		{"POST", "/api/user/2fa/setup"}, {"POST", "/api/oauth/email/bind"},
		{"GET", "/api/user/"},
		{"GET", "/api/oauth/github"},
	} {
		t.Run(tc.method+tc.path, func(t *testing.T) {
			require.Equal(t, 403, f.request(tc.method, tc.path, bundle.Data.AccessToken, nil).Code)
		})
	}
	for _, tc := range []struct{ method, path string }{{"GET", "/optional-native"}, {"POST", "/v1/chat/completions"}} {
		require.Equal(t, 204, f.request(tc.method, tc.path, bundle.Data.AccessToken, nil).Code)
	}
	f.revoked.Store(true)
	require.Equal(t, 401, f.request("GET", "/optional-native", bundle.Data.AccessToken, nil).Code)
	f.revoked.Store(false)
	// A corrupt PAT must not bypass the persistent mapping/marker validation.
	require.NoError(t, f.db.Model(&model.User{}).Where("id = ?", bundle.Data.User.Id).UpdateColumn("access_token", "projection-pat").Error)
	for _, path := range []string{"/api/user/self", "/optional-native", "/admin-control"} {
		require.GreaterOrEqual(t, f.request("GET", path, "projection-pat", nil).Code, 400, path)
	}
	pat := "legacy-pat"
	local := model.User{Username: "legacy", Password: "legacy-password", AffCode: "legacy", AccessToken: &pat, Role: common.RoleAdminUser, Quota: 123}
	require.NoError(t, f.db.Create(&local).Error)
	native, err := service.CreateLoginSession(local.Id, "password", "", "")
	require.NoError(t, err)
	for _, token := range []string{native.AccessToken, pat} {
		for _, path := range []string{"/native-control", "/optional-native", "/admin-control"} {
			require.Equal(t, 204, f.request("GET", path, token, nil).Code, path)
		}
	}
	require.Equal(t, 204, f.request("POST", "/v1/chat/completions", native.AccessToken, nil).Code)
}

func TestPublicProjectionRejectsNativeSessionsAndOmitsNativeWallet(t *testing.T) {
	f := newUniHubRegressionFixture(t)
	native := model.User{Username: "native-operator", Status: common.UserStatusEnabled, Role: common.RoleCommonUser, AuthVersion: 1, AffCode: "native-operator", Quota: 12345}
	require.NoError(t, f.db.Create(&native).Error)
	nativeBundle, err := service.CreateLoginSession(native.Id, "password", "", "")
	require.NoError(t, err)
	cookie := &http.Cookie{Name: service.RefreshCookieName, Value: nativeBundle.RefreshToken}
	for _, request := range []struct {
		method, path, token string
		cookie              *http.Cookie
	}{
		{"GET", "/api/user/self", nativeBundle.AccessToken, nil},
		{"POST", "/api/user/auth/refresh", "", cookie},
		{"POST", "/api/user/auth/logout", nativeBundle.AccessToken, cookie},
	} {
		w := f.request(request.method, request.path, request.token, request.cookie)
		require.Equal(t, 401, w.Code, w.Body.String())
		require.NotContains(t, w.Body.String(), "12345")
	}
	var session model.UserSession
	require.NoError(t, f.db.First(&session, "sid = ?", nativeBundle.Session.SID).Error)
	require.Equal(t, model.UserSessionStatusActive, session.Status)
	bundle, projectedCookie := decodeUniHubBundle(t, f.request("POST", "/api/unihub/session", "public-subject", nil))
	for _, w := range []*httptest.ResponseRecorder{
		f.request("GET", "/api/user/self", bundle.Data.AccessToken, nil),
		f.request("POST", "/api/user/auth/refresh", "", projectedCookie),
	} {
		require.Equal(t, 200, w.Code, w.Body.String())
		for _, field := range []string{"quota", "used_quota", "request_count", "aff_quota", "stripe_customer", "github_id"} {
			require.Contains(t, w.Body.String(), `"`+field+`":`)
		}
		require.NotContains(t, w.Body.String(), `"password":`)
	}
}
