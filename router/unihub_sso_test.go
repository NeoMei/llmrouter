package router

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/internal/platformaccount"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type uniHubSSOFixture struct {
	router           *gin.Engine
	db               *gorm.DB
	issuer           string
	unavailable      atomic.Bool
	discoveryCalls   atomic.Int32
	tokenCalls       atomic.Int32
	discoveryStarted chan struct{}
	codes            sync.Map
}

func newUniHubSSOFixture(t *testing.T, discoveryGate <-chan struct{}) *uniHubSSOFixture {
	t.Helper()
	oldDB, oldRedis, oldSecret, oldMode := model.DB, common.RedisEnabled, common.SessionSecret, gin.Mode()
	db, err := gorm.Open(sqlite.Open(t.TempDir()+"/sso.db"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.UserSession{}, &model.UniHubIdentityMapping{}, &model.UniHubSessionBinding{}))
	sqlDB, err := db.DB()
	require.NoError(t, err)
	model.DB, common.RedisEnabled, common.SessionSecret = db, false, "unihub-sso-test-session-secret"
	gin.SetMode(gin.TestMode)
	t.Cleanup(func() {
		model.DB, common.RedisEnabled, common.SessionSecret = oldDB, oldRedis, oldSecret
		gin.SetMode(oldMode)
		require.NoError(t, sqlDB.Close())
	})
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	f := &uniHubSSOFixture{db: db, discoveryStarted: make(chan struct{}, 1)}
	var issuer *httptest.Server
	issuer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		var payload any
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			f.discoveryCalls.Add(1)
			select {
			case f.discoveryStarted <- struct{}{}:
			default:
			}
			if discoveryGate != nil {
				select {
				case <-discoveryGate:
				case <-r.Context().Done():
					return
				}
			}
			if f.unavailable.Load() {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			payload = gin.H{"issuer": issuer.URL, "authorization_endpoint": issuer.URL + "/authorize", "token_endpoint": issuer.URL + "/token", "jwks_uri": issuer.URL + "/jwks", "id_token_signing_alg_values_supported": []string{"RS256"}}
		case "/jwks":
			payload = gin.H{"keys": []any{gin.H{"kty": "RSA", "kid": "test", "alg": "RS256", "n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()), "e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes())}}}
		case "/token":
			f.tokenCalls.Add(1)
			if r.ParseForm() != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			stored, ok := f.codes.LoadAndDelete(r.Form.Get("code"))
			if !ok {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			auth := stored.(url.Values)
			sum := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
			if base64.RawURLEncoding.EncodeToString(sum[:]) != auth.Get("code_challenge") || r.Form.Get("client_id") != "router-client" || r.Form.Get("redirect_uri") != "http://127.0.0.1:58119/api/unihub/callback" {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			claims := jwt.MapClaims{"iss": issuer.URL, "sub": "idp-subject", "aud": "router-client", "exp": time.Now().Add(time.Hour).Unix(), "nonce": auth.Get("nonce")}
			id := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
			id.Header["kid"] = "test"
			idToken, err := id.SignedString(key)
			if err != nil {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			claims["tokenType"] = "access-token"
			access := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
			access.Header["kid"] = "test"
			accessToken, err := access.SignedString(key)
			if err != nil {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			payload = gin.H{"access_token": accessToken, "id_token": idToken, "token_type": "Bearer", "expires_in": 3600}
		case "/v1/capabilities":
			payload = gin.H{"kind": "product", "product_id": "llmrouter", "client_id": "router-client", "instance_id": "default", "scopes": []string{"access:read"}}
		case "/v1/me/product-access":
			payload = gin.H{"issuer": issuer.URL, "subject": "central-user", "product_id": "llmrouter", "instance_id": "default", "user": gin.H{"id": "central-user", "name": "Ada"}, "roles": []string{"member"}, "permissions": []string{"product:use"}, "version": 1, "active": true, "expires_at": time.Now().Add(time.Hour).Unix()}
		default:
			w.WriteHeader(http.StatusNotFound)
			return
		}
		raw, err := common.Marshal(payload)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, _ = w.Write(raw)
	}))
	t.Cleanup(issuer.Close)
	f.issuer = issuer.URL
	f.router = gin.New()
	registerUniHubSSO(f.router, platformaccount.Config{Enabled: true, ProductID: "llmrouter", APIOrigin: issuer.URL, PublicOrigin: issuer.URL, OIDCIssuer: issuer.URL, ClientID: "router-client", RedirectURI: "http://127.0.0.1:58119/api/unihub/callback"})
	return f
}

func (f *uniHubSSOFixture) login(ctx context.Context) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	f.router.ServeHTTP(w, httptest.NewRequestWithContext(ctx, http.MethodGet, "http://127.0.0.1:58119/api/unihub/login?return_to=/channels", nil))
	return w
}

func (f *uniHubSSOFixture) callback(t *testing.T, login *httptest.ResponseRecorder) *httptest.ResponseRecorder {
	t.Helper()
	require.Equal(t, http.StatusSeeOther, login.Code)
	authorize, err := url.Parse(login.Header().Get("Location"))
	require.NoError(t, err)
	require.Equal(t, f.issuer+"/authorize", authorize.Scheme+"://"+authorize.Host+authorize.Path)
	require.Equal(t, "S256", authorize.Query().Get("code_challenge_method"))
	require.NotEmpty(t, authorize.Query().Get("nonce"))
	state := authorize.Query().Get("state")
	require.NotEmpty(t, state)
	f.codes.Store(state, authorize.Query())
	request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:58119/api/unihub/callback?"+url.Values{"code": {state}, "state": {state}}.Encode(), nil)
	cookies := login.Result().Cookies()
	require.Len(t, cookies, 1)
	request.AddCookie(cookies[0])
	w := httptest.NewRecorder()
	f.router.ServeHTTP(w, request)
	return w
}

func TestUniHubSSORetriesDiscoveryAfterOutage(t *testing.T) {
	f := newUniHubSSOFixture(t, nil)
	f.unavailable.Store(true)
	failed := f.login(context.Background())
	assert.Equal(t, http.StatusSeeOther, failed.Code)
	assert.Equal(t, "/unihub?sso_error=1", failed.Header().Get("Location"))
	assert.Empty(t, failed.Result().Cookies())
	assert.EqualValues(t, 1, f.discoveryCalls.Load())
	f.unavailable.Store(false)
	recovered := f.login(context.Background())
	require.Contains(t, recovered.Header().Get("Location"), f.issuer+"/authorize?")
	assert.EqualValues(t, 2, f.discoveryCalls.Load())
	complete := f.callback(t, recovered)
	assert.Equal(t, "/channels", complete.Header().Get("Location"))
	assert.Contains(t, strings.Join(complete.Header().Values("Set-Cookie"), "\n"), service.RefreshCookieName+"=")
}

func TestUniHubSSOConcurrentInitializationPreservesCallbacks(t *testing.T) {
	f := newUniHubSSOFixture(t, nil)
	start := make(chan struct{})
	responses := make(chan *httptest.ResponseRecorder, 4)
	for range 4 {
		go func() {
			<-start
			responses <- f.login(context.Background())
		}()
	}
	close(start)
	logins := make([]*httptest.ResponseRecorder, 0, 4)
	for range 4 {
		logins = append(logins, <-responses)
	}
	assert.EqualValues(t, 1, f.discoveryCalls.Load())
	// A new login while a callback runs must keep the original transaction store.
	go func() { responses <- f.login(context.Background()) }()
	first := f.callback(t, logins[0])
	assert.Equal(t, "/channels", first.Header().Get("Location"))
	logins = append(logins[1:], <-responses)
	for _, login := range logins {
		complete := f.callback(t, login)
		assert.Equal(t, http.StatusSeeOther, complete.Code)
		assert.Equal(t, "/channels", complete.Header().Get("Location"))
		assert.Contains(t, strings.Join(complete.Header().Values("Set-Cookie"), "\n"), service.RefreshCookieName+"=")
	}
	assert.EqualValues(t, 1, f.discoveryCalls.Load())
	assert.EqualValues(t, 5, f.tokenCalls.Load())
	var mappings, sessions int64
	require.NoError(t, f.db.Model(&model.UniHubIdentityMapping{}).Count(&mappings).Error)
	require.NoError(t, f.db.Model(&model.UniHubSessionBinding{}).Count(&sessions).Error)
	assert.EqualValues(t, 1, mappings)
	assert.EqualValues(t, 5, sessions)
	replayed := f.callback(t, logins[0])
	assert.Equal(t, "/unihub?sso_error=1", replayed.Header().Get("Location"))
	assert.EqualValues(t, 5, f.tokenCalls.Load())
}

func TestUniHubSSODiscoveryWaiterCanCancel(t *testing.T) {
	gate := make(chan struct{})
	var release sync.Once
	defer release.Do(func() { close(gate) })
	f := newUniHubSSOFixture(t, gate)
	f.unavailable.Store(true)
	first := make(chan *httptest.ResponseRecorder, 1)
	go func() { first <- f.login(context.Background()) }()
	select {
	case <-f.discoveryStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("discovery did not start")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	waiter := make(chan *httptest.ResponseRecorder, 1)
	go func() { waiter <- f.login(ctx) }()
	select {
	case failed := <-waiter:
		assert.Equal(t, "/unihub?sso_error=1", failed.Header().Get("Location"))
		assert.Empty(t, failed.Result().Cookies())
	case <-time.After(5 * time.Second):
		t.Error("canceled login waited for the in-flight discovery")
	}
	assert.EqualValues(t, 1, f.discoveryCalls.Load())
	release.Do(func() { close(gate) })
	assert.Equal(t, "/unihub?sso_error=1", (<-first).Header().Get("Location"))
	// The canceled waiter did not start a second discovery; the next login can.
	f.unavailable.Store(false)
	recovered := f.login(context.Background())
	require.Contains(t, recovered.Header().Get("Location"), f.issuer+"/authorize?")
	assert.EqualValues(t, 2, f.discoveryCalls.Load())
	assert.Equal(t, "/channels", f.callback(t, recovered).Header().Get("Location"))
}
