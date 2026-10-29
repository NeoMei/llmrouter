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
	"github.com/QuantumNous/new-api/internal/platformbilling"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPrivateRuntimeBrokerSessionRequiresBoundCurrentAuthority(t *testing.T) {
	f := newUniHubRegressionFixture(t)
	require.NoError(t, f.db.AutoMigrate(&model.ProductAuthLifecycle{}, &model.ProductFundingAccount{}, &platformbilling.Call{}, &platformbilling.Attempt{}))
	t.Setenv("UNIHUB_PRODUCT_RUNTIME_ENABLED", "true")
	t.Setenv("UNIHUB_PRODUCT_RUNTIME_TOKEN", strings.Repeat("s", 32))
	cfg, err := platformaccount.Current()
	require.NoError(t, err)
	user := &model.User{Username: "broker-member", AffCode: "broker-member", AuthVersion: 1, Status: common.UserStatusEnabled, Role: common.RoleCommonUser}
	require.NoError(t, f.db.Create(user).Error)
	_, err = model.BindUniHubIdentity(platformaccount.Identity{Issuer: cfg.PublicOrigin, User: platformaccount.User{ID: "member"}}, user.Id)
	require.NoError(t, err)
	broker, err := service.CreateLoginSession(user.Id, "product_broker", "", "")
	require.NoError(t, err)
	native, err := service.CreateLoginSession(user.Id, "password", "", "")
	require.NoError(t, err)
	enabled := true
	hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !enabled || (r.URL.Path != "/internal/v1/product-runtime/identity/"+strconv.Itoa(user.Id) && r.URL.Path != "/internal/v1/product-runtime/identity/authenticate") {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		identity := userkit.ProductIdentity{Binding: userkit.RuntimeBinding{ID: "binding", LocalUserID: strconv.Itoa(user.Id), UserID: "member", ProductID: "llmrouter", InstanceID: "default"}, Access: userkit.Access{Issuer: cfg.PublicOrigin, Subject: "member", User: userkit.User{ID: "member"}, ProductID: "llmrouter", InstanceID: "default", Active: true, ProductRole: 1, Permissions: []string{"product:use"}, ExpiresAt: time.Now().Add(time.Minute).Unix()}, ProductRole: 1, AuthVersion: user.AuthVersion}
		raw, e := common.Marshal(identity)
		if e != nil {
			w.WriteHeader(500)
			return
		}
		_, _ = w.Write(raw)
	}))
	t.Cleanup(hub.Close)
	t.Setenv("UNIHUB_ORIGIN", hub.URL)
	t.Setenv("NATIVE_PUBLIC_ACCESS", "false")
	response := f.request("GET", "/api/log/self", broker.AccessToken, nil)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	assert.NotContains(t, response.Body.String(), "UNIHUB_SESSION_REQUIRED")
	t.Setenv("UNIHUB_PRODUCT_ENABLED", "false")
	response = f.request("GET", "/api/log/self", broker.AccessToken, nil)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	assert.Equal(t, http.StatusUnauthorized, f.request("GET", "/api/log/self", native.AccessToken, nil).Code)
	refresh := &http.Cookie{Name: service.RefreshCookieName, Value: broker.RefreshToken}
	refreshed := f.request("POST", "/api/user/auth/refresh", "", refresh)
	require.Equal(t, http.StatusOK, refreshed.Code, refreshed.Body.String())
	assert.NotContains(t, refreshed.Body.String(), "UNIHUB_SESSION_REQUIRED")
	nativeRefresh := &http.Cookie{Name: service.RefreshCookieName, Value: native.RefreshToken}
	assert.Equal(t, http.StatusUnauthorized, f.request("POST", "/api/user/auth/refresh", "", nativeRefresh).Code)
	enabled = false
	assert.Equal(t, http.StatusUnauthorized, f.request("GET", "/api/log/self", broker.AccessToken, nil).Code)
}

func TestPrivateRuntimeServesOnlyProductShell(t *testing.T) {
	enableUniHubBoundary(t)
	t.Setenv("UNIHUB_PRODUCT_RUNTIME_ENABLED", "true")
	t.Setenv("UNIHUB_PRODUCT_ENABLED", "false")
	r := gin.New()
	r.Use(middleware.NativeWebBoundary())
	r.NoRoute(func(c *gin.Context) { c.String(http.StatusOK, "shell") })
	for _, path := range []string{"/sign-in", "/dashboard", "/keys"} {
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest(method, path, nil))
			require.Equal(t, http.StatusOK, w.Code, "%s %s", method, path)
		}
	}
	for _, path := range []string{"/api/token/", "/sign-up", "/unknown"} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		assert.Equal(t, http.StatusForbidden, w.Code, path)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/keys", nil))
	assert.Equal(t, http.StatusForbidden, w.Code)
}
