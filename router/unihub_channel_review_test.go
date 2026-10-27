package router

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/bytedance/gopkg/util/gopool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUniHubChannelReadSuppressesConfigurationAndPreservesUpdates(t *testing.T) {
	f := newUniHubRegressionFixture(t)
	// Initialize the same reserved-column quoting as normal startup before actual
	// search handlers execute; the shared fixture otherwise bypasses InitDB.
	oldPath, oldMaster := common.SQLitePath, common.IsMasterNode
	oldMainType, oldLogType := common.MainDatabaseType(), common.LogDatabaseType()
	common.SQLitePath, common.IsMasterNode = t.TempDir()+"/column-setup.db", false
	t.Setenv("SQL_DSN", "")
	t.Setenv("LOG_SQL_DSN", "")
	require.NoError(t, model.InitDB())
	sqlDB, err := model.DB.DB()
	require.NoError(t, err)
	require.NoError(t, sqlDB.Close())
	model.DB = f.db
	common.SQLitePath, common.IsMasterNode = oldPath, oldMaster
	t.Cleanup(func() { common.SetDatabaseTypes(oldMainType, oldLogType) })
	require.NoError(t, f.db.AutoMigrate(&model.Channel{}, &model.Ability{}, &model.Log{}))
	oldLog := model.LOG_DB
	model.LOG_DB = f.db
	t.Cleanup(func() {
		require.Eventually(t, func() bool { return gopool.WorkerCount() == 0 }, 5*time.Second, 10*time.Millisecond)
		model.LOG_DB = oldLog
	})
	ch := model.Channel{Type: 1, Name: "sentinel-channel", Key: "SECRET-key", Status: 1, Models: "model-a", Group: "default", Tag: common.GetPointer("tag-a"), BaseURL: common.GetPointer("https://SECRET-user:SECRET-pass@supplier.test"), Setting: common.GetPointer(`{"proxy":"http://SECRET-proxy:SECRET-password@proxy.test"}`), HeaderOverride: common.GetPointer(`{"Authorization":"Bearer SECRET-header"}`), ParamOverride: common.GetPointer(`{"api_key":"SECRET-param"}`), OtherSettings: `{"credential":"SECRET-settings"}`, Other: "SECRET-other", OtherInfo: `{"error":"SECRET-info"}`, Remark: common.GetPointer("SECRET-remark")}
	require.NoError(t, f.db.Create(&ch).Error)
	b, _ := decodeUniHubBundle(t, f.request("POST", "/api/unihub/session", "admin", nil))
	for _, path := range []string{"/api/channel/", "/api/channel/?tag_mode=true", "/api/channel/search?keyword=sentinel", "/api/channel/search?keyword=sentinel&tag_mode=true", fmt.Sprintf("/api/channel/%d", ch.Id)} {
		w := f.request("GET", path, b.Data.AccessToken, nil)
		require.Equal(t, 200, w.Code, path)
		require.Contains(t, w.Body.String(), `"success":true`)
		assert.Contains(t, w.Body.String(), "sentinel-channel")
		assert.NotContains(t, w.Body.String(), "SECRET", path)
		var data map[string]any
		require.NoError(t, common.Unmarshal(w.Body.Bytes(), &data))
	}
	// The normal routing editor submits only visible routing/name fields.
	body := fmt.Sprintf(`{"id":%d,"type":1,"name":"renamed","models":"model-a,model-b","group":"default"}`, ch.Id)
	req := httptest.NewRequest("PUT", "https://dashboard.test/api/channel/", strings.NewReader(body))
	req.Header.Set("Origin", "https://dashboard.test")
	req.Header.Set("Authorization", "Bearer "+b.Data.AccessToken)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	f.router.ServeHTTP(w, req)
	require.Equal(t, 200, w.Code)
	require.Contains(t, w.Body.String(), `"success":true`)
	assert.NotContains(t, w.Body.String(), "SECRET")
	var persisted model.Channel
	require.NoError(t, f.db.First(&persisted, ch.Id).Error)
	assert.Equal(t, "renamed", persisted.Name)
	assert.Equal(t, "model-a,model-b", persisted.Models)
	assert.Equal(t, ch.Key, persisted.Key)
	assert.Equal(t, ch.BaseURL, persisted.BaseURL)
	assert.Equal(t, ch.Setting, persisted.Setting)
	assert.Equal(t, ch.HeaderOverride, persisted.HeaderOverride)
	assert.Equal(t, ch.ParamOverride, persisted.ParamOverride)
	assert.Equal(t, ch.OtherSettings, persisted.OtherSettings)
	// Even a sensitive editor supplies a partial opaque-config patch. Its normal
	// name/routing edits must never replace the unseen saved credential defaults.
	sensitive, _ := decodeUniHubBundle(t, f.request("POST", "/api/unihub/session", "sensitive", nil))
	patch := fmt.Sprintf(`{"id":%d,"setting":"{\"force_format\":true}","settings":"{\"disable_store\":true}"}`, ch.Id)
	req = httptest.NewRequest("PUT", "https://dashboard.test/api/channel/", strings.NewReader(patch))
	req.Header.Set("Origin", "https://dashboard.test")
	req.Header.Set("Authorization", "Bearer "+sensitive.Data.AccessToken)
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	f.router.ServeHTTP(w, req)
	require.Contains(t, w.Body.String(), `"success":true`)
	require.NoError(t, f.db.First(&persisted, ch.Id).Error)
	assert.Contains(t, *persisted.Setting, "SECRET-proxy")
	assert.Contains(t, persisted.OtherSettings, "SECRET-settings")
	// Native root uses the established full native configuration contract.
	t.Setenv("UNIHUB_PRODUCT_ENABLED", "false")
	t.Setenv("UNIHUB_PLATFORM_ENABLED", "false")
	t.Setenv("NATIVE_PUBLIC_ACCESS", "true")
	native := model.User{Username: "native-full-config", Role: common.RoleRootUser, Status: 1, AuthVersion: 1, AffCode: "native-full-config"}
	require.NoError(t, f.db.Create(&native).Error)
	bundle, err := service.CreateLoginSession(native.Id, "password", "", "")
	require.NoError(t, err)
	nativeRead := f.request("GET", fmt.Sprintf("/api/channel/%d", ch.Id), bundle.AccessToken, nil)
	require.Equal(t, 200, nativeRead.Code)
	assert.Contains(t, nativeRead.Body.String(), "SECRET-header")
	assert.Contains(t, nativeRead.Body.String(), "SECRET-password")
}

type localCodexRefreshTransport struct {
	base   http.RoundTripper
	target *url.URL
}

func (t localCodexRefreshTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Host == "auth.openai.com" {
		copy := r.Clone(r.Context())
		u := *r.URL
		u.Scheme = t.target.Scheme
		u.Host = t.target.Host
		copy.URL = &u
		copy.Host = t.target.Host
		return t.base.RoundTrip(copy)
	}
	return t.base.RoundTrip(r)
}
func TestUniHubCodexReadDoesNotRefreshStoredCredentials(t *testing.T) {
	f := newUniHubRegressionFixture(t)
	require.NoError(t, f.db.AutoMigrate(&model.Channel{}, &model.Ability{}, &model.Log{}))
	oldLog := model.LOG_DB
	model.LOG_DB = f.db
	t.Cleanup(func() {
		require.Eventually(t, func() bool { return gopool.WorkerCount() == 0 }, 5*time.Second, 10*time.Millisecond)
		model.LOG_DB = oldLog
	})
	var refreshes atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/oauth/token" {
			refreshes.Add(1)
			_, _ = w.Write([]byte(`{"access_token":"rotated-access","refresh_token":"rotated-refresh","expires_in":3600}`))
			return
		}
		if r.Header.Get("Authorization") == "Bearer rotated-access" {
			_, _ = w.Write([]byte(`{"ok":true}`))
			return
		}
		w.WriteHeader(401)
		_, _ = w.Write([]byte(`{"error":"expired"}`))
	}))
	defer upstream.Close()
	target, err := url.Parse(upstream.URL)
	require.NoError(t, err)
	client := service.GetHttpClient()
	if client == nil {
		client = http.DefaultClient
	}
	oldTransport := client.Transport
	base := oldTransport
	if base == nil {
		base = http.DefaultTransport
	}
	client.Transport = localCodexRefreshTransport{base: base, target: target}
	t.Cleanup(func() { client.Transport = oldTransport })
	key := `{"access_token":"expired-access","refresh_token":"kept-refresh","account_id":"account"}`
	ch := model.Channel{Type: constant.ChannelTypeCodex, Name: "codex-local", Key: key, Status: 1, Models: "test", Group: "default", BaseURL: &upstream.URL}
	require.NoError(t, f.db.Create(&ch).Error)
	viewer, _ := decodeUniHubBundle(t, f.request("POST", "/api/unihub/session", "viewer", nil))
	for _, suffix := range []string{"usage", "usage/reset-credits"} {
		w := f.request("GET", fmt.Sprintf("/api/channel/%d/codex/%s", ch.Id, suffix), viewer.Data.AccessToken, nil)
		require.Equal(t, 200, w.Code)
		assert.Contains(t, w.Body.String(), `"success":false`)
	}
	assert.Zero(t, refreshes.Load(), "read-only product user triggered OAuth refresh")
	var persisted model.Channel
	require.NoError(t, f.db.First(&persisted, ch.Id).Error)
	assert.Equal(t, key, persisted.Key)
	assert.Equal(t, 403, f.request("POST", fmt.Sprintf("/api/channel/%d/codex/refresh", ch.Id), viewer.Data.AccessToken, nil).Code)
	sensitive, _ := decodeUniHubBundle(t, f.request("POST", "/api/unihub/session", "sensitive", nil))
	refreshed := f.request("POST", fmt.Sprintf("/api/channel/%d/codex/refresh", ch.Id), sensitive.Data.AccessToken, nil)
	require.Equal(t, 200, refreshed.Code)
	require.Contains(t, refreshed.Body.String(), `"success":true`)
	assert.Equal(t, int32(1), refreshes.Load())
	require.NoError(t, f.db.First(&persisted, ch.Id).Error)
	assert.Contains(t, persisted.Key, "rotated-access")
	// Standalone native readers retain the established automatic refresh behavior.
	t.Setenv("UNIHUB_PRODUCT_ENABLED", "false")
	t.Setenv("UNIHUB_PLATFORM_ENABLED", "false")
	t.Setenv("NATIVE_PUBLIC_ACCESS", "true")
	require.NoError(t, f.db.Model(&model.Channel{}).Where("id = ?", ch.Id).Update("key", key).Error)
	native := model.User{Username: "native-codex", Role: common.RoleRootUser, Status: 1, AuthVersion: 1, AffCode: "native-codex"}
	require.NoError(t, f.db.Create(&native).Error)
	nativeBundle, err := service.CreateLoginSession(native.Id, "password", "", "")
	require.NoError(t, err)
	nativeRead := f.request("GET", fmt.Sprintf("/api/channel/%d/codex/usage", ch.Id), nativeBundle.AccessToken, nil)
	require.Equal(t, 200, nativeRead.Code)
	assert.Contains(t, nativeRead.Body.String(), `"success":true`)
	assert.Equal(t, int32(2), refreshes.Load())

}
