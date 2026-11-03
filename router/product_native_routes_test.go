package router

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	userkit "github.com/NeoMei/UniHub/packages/userkit-go"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/controller"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/internal/platformaccount"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The real browser session, identity mapping and runtime authority remain in
// the fixture. Only the external identity service is replaced.
func newCentralProductRouteFixture(t *testing.T) (*uniHubRegressionFixture, *model.User, *service.AuthBundle) {
	t.Helper()
	f := newUniHubRegressionFixture(t)
	model.InitDatabaseColumns()
	require.NoError(t, i18n.Init())
	require.NoError(t, f.db.AutoMigrate(&model.Token{}, &model.ProductAuthLifecycle{}, &model.ProductBrokerSessionProof{}, &model.Ability{}, &model.Channel{}))
	require.NoError(t, model.RegisterProductFundingModels(f.db))
	config, err := platformaccount.Current()
	require.NoError(t, err)
	user := &model.User{Username: "central-product", AffCode: "central-product", AuthVersion: 1, Status: common.UserStatusEnabled, Role: common.RoleCommonUser, Group: "default"}
	require.NoError(t, f.db.Create(user).Error)
	_, err = model.BindUniHubIdentity(platformaccount.Identity{Issuer: config.PublicOrigin, User: platformaccount.User{ID: "central-product"}}, user.Id)
	require.NoError(t, err)
	access := userkit.Access{Issuer: config.PublicOrigin, Subject: "central-product", User: userkit.User{ID: "central-product"}, ProductID: "llmrouter", InstanceID: "default", ProductRole: 1, Permissions: []string{"product:use"}, Active: true, Version: 1, ExpiresAt: time.Now().Add(time.Hour).Unix()}
	hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if f.unavailable.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		if r.URL.Path == "/internal/v1/product-runtime/identity/browser/source/verify" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.URL.Path != "/internal/v1/product-runtime/identity/"+strconv.Itoa(user.Id) {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		current := access
		if f.revoked.Load() {
			current.Active, current.Version = false, 2
		}
		identity := userkit.ProductIdentity{Binding: userkit.RuntimeBinding{ID: "product-binding", LocalUserID: strconv.Itoa(user.Id), UserID: access.Subject, PersonalAccountID: "central-payer", ProductID: "llmrouter", InstanceID: "default"}, Access: current, ProductRole: 1, AuthVersion: 1}
		raw, e := common.Marshal(identity)
		if e != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, _ = w.Write(raw)
	}))
	t.Cleanup(hub.Close)
	t.Setenv("UNIHUB_PRODUCT_RUNTIME_ENABLED", "true")
	t.Setenv("UNIHUB_PRODUCT_API_ORIGIN", hub.URL)
	t.Setenv("UNIHUB_PRODUCT_RUNTIME_TOKEN", strings.Repeat("s", 32))
	t.Setenv("UNIHUB_INDEPENDENT_CENTRAL_REALM_ENABLED", "true")
	t.Setenv("NATIVE_PUBLIC_ACCESS", "false")
	bundle, err := service.CreateCentralSessionFromVerifiedSource(context.Background(), user.Id, access, "product_broker", access.ExpiresAt, "", "")
	require.NoError(t, err)
	require.NoError(t, model.BindBrokerSourceProof(bundle.Session.SID, user.Id, access, config, "ubsp_"+strings.Repeat("a", 64), access.ExpiresAt))
	scope, err := model.ProductRuntimeScopeDigest()
	require.NoError(t, err)
	require.NoError(t, f.db.Create(&model.ProductFundingAccount{UserID: user.Id, AccountID: "central-payer", ScopeDigest: scope, State: "central", Asset: userkit.RuntimeAsset, UnitVersion: userkit.RuntimeUnitVersion}).Error)
	return f, user, bundle
}

func productRouteRequest(f *uniHubRegressionFixture, method, path, bearer, origin, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, "https://dashboard.test"+path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+bearer)
	req.Header.Set("Content-Type", "application/json")
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	w := httptest.NewRecorder()
	f.router.ServeHTTP(w, req)
	return w
}

func TestCentralProductOriginalKeyLifecycleWithNativePublicDisabled(t *testing.T) {
	f, user, bundle := newCentralProductRouteFixture(t)
	request := func(method, path, body string) *httptest.ResponseRecorder {
		return productRouteRequest(f, method, path, bundle.AccessToken, "https://dashboard.test", body)
	}
	created := request("POST", "/api/token/", `{"name":"product-original","remain_quota":100,"expired_time":-1,"unlimited_quota":false,"model_limits_enabled":true,"model_limits":"original-model","allow_ips":"127.0.0.1","group":"default","cross_group_retry":true}`)
	require.Equal(t, http.StatusOK, created.Code, created.Body.String())
	require.Contains(t, created.Body.String(), `"success":true`)
	var key model.Token
	require.NoError(t, f.db.Where("user_id = ? AND name = ?", user.Id, "product-original").First(&key).Error)
	assert.Empty(t, key.AuthRealm)
	assert.Equal(t, "original-model", key.ModelLimits)
	assert.True(t, key.CrossGroupRetry)
	for _, path := range []string{"/api/token/", "/api/token/search?keyword=product-original", fmt.Sprintf("/api/token/%d", key.Id)} {
		w := request("GET", path, "")
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		assert.Contains(t, w.Body.String(), "product-original")
	}
	secret := request("POST", fmt.Sprintf("/api/token/%d/key", key.Id), "")
	require.Equal(t, http.StatusOK, secret.Code, secret.Body.String())
	assert.Contains(t, secret.Body.String(), key.Key)
	batch := request("POST", "/api/token/batch/keys", fmt.Sprintf(`{"ids":[%d]}`, key.Id))
	require.Equal(t, http.StatusOK, batch.Code, batch.Body.String())
	assert.Contains(t, batch.Body.String(), key.Key)
	update := request("PUT", "/api/token/", fmt.Sprintf(`{"id":%d,"name":"product-updated","expired_time":-1,"remain_quota":200,"unlimited_quota":false}`, key.Id))
	require.Equal(t, http.StatusOK, update.Code, update.Body.String())
	require.Contains(t, update.Body.String(), `"success":true`)
	paused := request("PUT", "/api/token/?status_only=true", fmt.Sprintf(`{"id":%d,"status":2}`, key.Id))
	require.Equal(t, http.StatusOK, paused.Code, paused.Body.String())
	require.NoError(t, f.db.First(&key, key.Id).Error)
	assert.Equal(t, common.TokenStatusDisabled, key.Status)
	deleted := request("DELETE", fmt.Sprintf("/api/token/%d", key.Id), "")
	require.Equal(t, http.StatusOK, deleted.Code, deleted.Body.String())
	require.NoError(t, f.db.Create(&model.Token{UserId: user.Id, Key: "batch-key", Name: "batch", ExpiredTime: -1}).Error)
	key = model.Token{}
	require.NoError(t, f.db.Where("key = ?", "batch-key").First(&key).Error)
	removed := request("POST", "/api/token/batch", fmt.Sprintf(`{"ids":[%d]}`, key.Id))
	require.Equal(t, http.StatusOK, removed.Code, removed.Body.String())
	assert.Contains(t, removed.Body.String(), `"data":1`)
	chat := request("POST", "/api/user/chat-credential", `{"preset_id":"original","url":"https://chat.example.test/app"}`)
	require.Equal(t, http.StatusOK, chat.Code, chat.Body.String())
	assert.Contains(t, chat.Body.String(), `"credential":"sk-`)
}

func TestCentralProductPreferencesDoNotAuthorizeIdentityOrNativeMoney(t *testing.T) {
	f, user, bundle := newCentralProductRouteFixture(t)
	for _, body := range []string{`{"language":"en"}`, `{"sidebar_modules":"{\"chat\":true}"}`} {
		w := productRouteRequest(f, "PUT", "/api/user/self", bundle.AccessToken, "https://dashboard.test", body)
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		require.Contains(t, w.Body.String(), `"success":true`)
	}
	var current model.User
	require.NoError(t, f.db.First(&current, user.Id).Error)
	assert.Equal(t, "en", current.GetSetting().Language)
	assert.Equal(t, `{"chat":true}`, current.GetSetting().SidebarModules)
	settings := productRouteRequest(f, "PUT", "/api/user/setting", bundle.AccessToken, "https://dashboard.test", `{"notify_type":"email","quota_warning_threshold":1,"record_ip_log":true}`)
	require.Equal(t, http.StatusOK, settings.Code, settings.Body.String())
	assert.Contains(t, settings.Body.String(), `"success":true`)
	for _, body := range []string{`{"username":"hijacked","password":"changed-password"}`, `{"language":"en","password":"changed-password"}`, `{"display_name":"hijacked"}`, `{"language":"en","role":100}`} {
		w := productRouteRequest(f, "PUT", "/api/user/self", bundle.AccessToken, "https://dashboard.test", body)
		assert.Equal(t, http.StatusForbidden, w.Code, w.Body.String())
	}
	for _, entry := range []struct{ method, path string }{{"GET", "/api/user/token"}, {"POST", "/api/user/topup"}, {"POST", "/api/user/2fa/setup"}, {"POST", "/api/oauth/email/bind"}, {"DELETE", "/api/user/self"}} {
		w := productRouteRequest(f, entry.method, entry.path, bundle.AccessToken, "https://dashboard.test", `{}`)
		assert.Equal(t, http.StatusForbidden, w.Code, entry.path+" "+w.Body.String())
	}
	for _, origin := range []string{"", "https://attacker.test"} {
		w := productRouteRequest(f, "POST", "/api/token/", bundle.AccessToken, origin, `{"name":"forbidden"}`)
		assert.Equal(t, http.StatusForbidden, w.Code, w.Body.String())
	}
	f.unavailable.Store(true)
	assert.Equal(t, http.StatusServiceUnavailable, f.request("GET", "/api/token/", bundle.AccessToken, nil).Code)
	f.unavailable.Store(false)
	f.revoked.Store(true)
	assert.Equal(t, http.StatusUnauthorized, f.request("GET", "/api/token/", bundle.AccessToken, nil).Code)
}

func TestCentralProductKeyUsesOriginalRelayWithNativePublicDisabled(t *testing.T) {
	f, user, _ := newCentralProductRouteFixture(t)
	key := &model.Token{UserId: user.Id, Key: "productrelaykey", Name: "product relay", Status: common.TokenStatusEnabled, ExpiredTime: -1, UnlimitedQuota: true}
	require.NoError(t, f.db.Create(key).Error)
	r := gin.New()
	SetRelayRouter(r)
	f.router = r
	for _, entry := range []struct{ path, header string }{{"/v1/models", "Authorization"}, {"/v1beta/models", "x-goog-api-key"}, {"/v1beta/openai/models", "x-goog-api-key"}} {
		req := httptest.NewRequest("GET", "https://dashboard.test"+entry.path, nil)
		value := "sk-" + key.Key
		if entry.header == "Authorization" {
			value = "Bearer " + value
		}
		req.Header.Set(entry.header, value)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	}
	for _, bearer := range []string{"", "invalid-key"} {
		w := f.request("GET", "/v1/models", bearer, nil)
		assert.GreaterOrEqual(t, w.Code, 400, w.Body.String())
	}
	f.unavailable.Store(true)
	assert.Equal(t, http.StatusServiceUnavailable, f.request("GET", "/v1/models", "sk-"+key.Key, nil).Code)
}

func TestCentralProductKeyOwnershipAndSharedNativeBoundary(t *testing.T) {
	f, user, bundle := newCentralProductRouteFixture(t)
	other := model.User{Username: "another-product", AffCode: "another-product", Status: 1, Role: 1, AuthVersion: 1}
	require.NoError(t, f.db.Create(&other).Error)
	keys := []model.Token{
		{UserId: user.Id, Key: "ownsecret", Name: "own product", ExpiredTime: -1, Status: 1},
		{UserId: user.Id, Key: "nativesecret", Name: "native private", AuthRealm: model.NativeFundingLocalOnly, ExpiredTime: -1, Status: 1},
		{UserId: other.Id, Key: "othersecret", Name: "other private", ExpiredTime: -1, Status: 1},
	}
	for i := range keys {
		require.NoError(t, f.db.Create(&keys[i]).Error)
	}
	listed := f.request("GET", "/api/token/", bundle.AccessToken, nil)
	require.Equal(t, http.StatusOK, listed.Code, listed.Body.String())
	assert.Contains(t, listed.Body.String(), "own product")
	assert.NotContains(t, listed.Body.String(), "native private")
	assert.NotContains(t, listed.Body.String(), "other private")
	batch := productRouteRequest(f, "POST", "/api/token/batch/keys", bundle.AccessToken, "https://dashboard.test", fmt.Sprintf(`{"ids":[%d,%d,%d]}`, keys[0].Id, keys[1].Id, keys[2].Id))
	require.Equal(t, http.StatusOK, batch.Code, batch.Body.String())
	assert.Contains(t, batch.Body.String(), "ownsecret")
	assert.NotContains(t, batch.Body.String(), "nativesecret")
	assert.NotContains(t, batch.Body.String(), "othersecret")
	for _, key := range keys[1:] {
		secret := productRouteRequest(f, "POST", fmt.Sprintf("/api/token/%d/key", key.Id), bundle.AccessToken, "https://dashboard.test", "")
		assert.NotContains(t, secret.Body.String(), key.Key)
		assert.NotContains(t, secret.Body.String(), `"success":true`)
	}
	require.NoError(t, f.db.Model(&model.User{}).Where("id = ?", user.Id).Updates(map[string]any{"funding_realm": model.NativeFundingLocalOnly, "funding_realm_evidence": "reviewed", "native_password": true}).Error)
	for _, entry := range []struct{ method, path, body string }{
		{"GET", "/api/token/", ""}, {"POST", "/api/token/", `{"name":"must-not-create"}`},
		{"POST", "/api/user/chat-credential", `{"url":"https://chat.example.test"}`},
		{"PUT", "/api/user/self", `{"language":"zh"}`}, {"PUT", "/api/user/setting", `{}`},
	} {
		w := productRouteRequest(f, entry.method, entry.path, bundle.AccessToken, "https://dashboard.test", entry.body)
		assert.Equal(t, http.StatusForbidden, w.Code, entry.path+" "+w.Body.String())
	}
	var count int64
	require.NoError(t, f.db.Model(&model.Token{}).Where("name = ?", "must-not-create").Count(&count).Error)
	assert.Zero(t, count)
}

func TestCentralProductRelayRequiresCentralFundsAndBrowserWriteOrigin(t *testing.T) {
	f, user, bundle := newCentralProductRouteFixture(t)
	key := model.Token{UserId: user.Id, Key: "fundingkey", Name: "funding", Status: 1, ExpiredTime: -1, UnlimitedQuota: true}
	require.NoError(t, f.db.Create(&key).Error)
	r := gin.New()
	SetRelayRouter(r)
	f.router = r
	for _, origin := range []string{"", "https://attacker.test"} {
		w := productRouteRequest(f, "POST", "/pg/chat/completions", bundle.AccessToken, origin, `{"model":"absent","messages":[]}`)
		assert.Equal(t, http.StatusForbidden, w.Code, w.Body.String())
	}
	require.NoError(t, f.db.Where("user_id = ?", user.Id).Delete(&model.ProductFundingAccount{}).Error)
	for _, nativePublic := range []string{"false", "true"} {
		t.Setenv("NATIVE_PUBLIC_ACCESS", nativePublic)
		w := f.request("GET", "/v1/models", "sk-"+key.Key, nil)
		assert.Equal(t, http.StatusServiceUnavailable, w.Code, "native public %s: %s", nativePublic, w.Body.String())
	}
}

func TestProductRelayRetainsKeyPolicyAndRejectsNativeOnlyCredential(t *testing.T) {
	f, user, _ := newCentralProductRouteFixture(t)
	native := model.User{Username: "native-only", AffCode: "native-only", Role: 1, Status: 1, AuthVersion: 1, Group: "default", FundingRealm: model.NativeFundingLocalOnly, FundingRealmEvidence: "reviewed", NativePassword: true}
	require.NoError(t, f.db.Create(&native).Error)
	nativeKey := model.Token{UserId: native.Id, Key: "nativeonlykey", Name: "native-only", Status: 1, ExpiredTime: -1, AuthRealm: model.NativeFundingLocalOnly, UnlimitedQuota: true}
	require.NoError(t, f.db.Create(&nativeKey).Error)
	require.NoError(t, f.db.Create(&model.NativeTokenQuotaState{TokenID: nativeKey.Id, RemainQuota: 100, Revision: 1, ImportEvidence: "reviewed"}).Error)
	key := model.Token{UserId: user.Id, Key: "policykey", Name: "policy", Status: 1, ExpiredTime: -1, UnlimitedQuota: true}
	require.NoError(t, f.db.Create(&key).Error)
	r := gin.New()
	SetRelayRouter(r)
	SetVideoRouter(r)
	SetOCRRouter(r, &controller.OCRHandler{})
	f.router = r
	for _, path := range []string{"/v1/models", "/v1/videos/missing/content", "/api/v2/ocr/jobs/missing"} {
		w := f.request("GET", path, "sk-"+nativeKey.Key, nil)
		assert.Equal(t, http.StatusForbidden, w.Code, path+" "+w.Body.String())
		assert.Contains(t, w.Body.String(), "NATIVE_ACCESS_DISABLED")
	}
	ocr := f.request("POST", "/api/v2/ocr/jobs", "sk-"+key.Key, nil)
	require.Equal(t, http.StatusServiceUnavailable, ocr.Code, ocr.Body.String())
	assert.Contains(t, ocr.Body.String(), "OCR is not configured", "product key reaches the original OCR handler")
	for _, policy := range []struct {
		change map[string]any
		status int
	}{
		{map[string]any{"status": common.TokenStatusDisabled}, http.StatusUnauthorized},
		{map[string]any{"status": common.TokenStatusEnabled, "expired_time": 1}, http.StatusUnauthorized},
		{map[string]any{"status": common.TokenStatusEnabled, "expired_time": -1, "allow_ips": "203.0.113.12"}, http.StatusForbidden},
	} {
		require.NoError(t, f.db.Model(&key).Updates(policy.change).Error)
		w := f.request("GET", "/v1/models", "sk-"+key.Key, nil)
		assert.Equal(t, policy.status, w.Code, w.Body.String())
	}
}
