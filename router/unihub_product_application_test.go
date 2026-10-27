package router

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/bytedance/gopkg/util/gopool"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUniHubProductMemberOwnApplicationData(t *testing.T) {
	f := newUniHubRegressionFixture(t)
	oldLog := model.LOG_DB
	model.LOG_DB = f.db
	t.Cleanup(func() {
		require.Eventually(t, func() bool { return gopool.WorkerCount() == 0 }, 5*time.Second, 10*time.Millisecond)
		model.LOG_DB = oldLog
	})
	require.NoError(t, f.db.AutoMigrate(&model.QuotaData{}, &model.Log{}, &model.Channel{}, &model.Ability{}, &model.Model{}, &model.Vendor{}, &model.Task{}, &model.Midjourney{}))
	member, _ := decodeUniHubBundle(t, f.request("POST", "/api/unihub/session", "subject", nil))
	other, _ := decodeUniHubBundle(t, f.request("POST", "/api/unihub/session", "other", nil))
	now := time.Now().Unix()
	for _, row := range []model.QuotaData{{UserID: member.Data.User.Id, Username: member.Data.User.Username, ModelName: "own-model", CreatedAt: now, Count: 1}, {UserID: other.Data.User.Id, Username: other.Data.User.Username, ModelName: "other-model", CreatedAt: now, Count: 9}} {
		require.NoError(t, f.db.Create(&row).Error)
	}
	for _, row := range []model.Log{{UserId: member.Data.User.Id, Username: member.Data.User.Username, ModelName: "own-model", CreatedAt: now, Type: model.LogTypeConsume, Content: "provider-secret-should-not-leak", Other: `{"admin_info":{"secret":"private"},"provider_error":"private"}`}, {UserId: other.Data.User.Id, Username: other.Data.User.Username, ModelName: "other-model", CreatedAt: now, Type: model.LogTypeConsume}} {
		require.NoError(t, f.db.Create(&row).Error)
	}
	for _, row := range []model.Task{{UserId: member.Data.User.Id, TaskID: "own-task", Status: model.TaskStatusSuccess, Data: []byte(`{"credential":"task-secret"}`), FailReason: "task-secret"}, {UserId: other.Data.User.Id, TaskID: "other-task", Status: model.TaskStatusSuccess}} {
		require.NoError(t, f.db.Create(&row).Error)
	}
	for _, row := range []model.Midjourney{{UserId: member.Data.User.Id, MjId: "own-drawing", Prompt: "user drawing", Properties: "drawing-secret", Status: "SUCCESS"}, {UserId: other.Data.User.Id, MjId: "other-drawing", Status: "SUCCESS"}} {
		require.NoError(t, f.db.Create(&row).Error)
	}
	for _, tc := range []struct{ path, own, other, secret string }{{"/api/task/self", "own-task", "other-task", "task-secret"}, {"/api/mj/self", "own-drawing", "other-drawing", "drawing-secret"}} {
		w := f.request("GET", tc.path, member.Data.AccessToken, nil)
		require.Equal(t, 200, w.Code, w.Body.String())
		assert.Contains(t, w.Body.String(), tc.own)
		assert.NotContains(t, w.Body.String(), tc.other)
		assert.NotContains(t, w.Body.String(), tc.secret)
	}
	query := fmt.Sprintf("?start_timestamp=%d&end_timestamp=%d&user_id=%d&username=%s", now-100, now+100, other.Data.User.Id, other.Data.User.Username)
	for _, path := range []string{"/api/data/self", "/api/log/self"} {
		t.Run(path, func(t *testing.T) {
			w := f.request("GET", path+query, member.Data.AccessToken, nil)
			require.Equal(t, 200, w.Code, w.Body.String())
			assert.Contains(t, w.Body.String(), "own-model")
			assert.NotContains(t, w.Body.String(), "other-model")
			assert.NotContains(t, w.Body.String(), "provider-secret")
			assert.NotContains(t, w.Body.String(), "private")
		})
	}
	for _, path := range []string{"/api/models", "/api/capabilities", "/api/user/models", "/api/user/self/groups", "/api/pricing"} {
		w := f.request("GET", path, member.Data.AccessToken, nil)
		require.Equal(t, 200, w.Code, w.Body.String())
	}
	for _, path := range []string{"/api/data/", "/api/log/", "/api/client_model_routes/", "/api/option/", "/api/user/", "/api/token/", "/api/user/topup/info"} {
		w := f.request("GET", path, member.Data.AccessToken, nil)
		assert.Equal(t, 403, w.Code, path+" "+w.Body.String())
	}
	f.revoked.Store(true)
	for _, path := range []string{"/api/data/self", "/api/log/self", "/api/models"} {
		w := f.request("GET", path+query, member.Data.AccessToken, nil)
		assert.Equal(t, 401, w.Code, path+" "+w.Body.String())
	}
}

func TestUniHubProductApplicationSPARoutes(t *testing.T) {
	enableUniHubBoundary(t)
	r := gin.New()
	r.Use(middleware.NativeWebBoundary())
	r.NoRoute(func(c *gin.Context) { c.String(200, "application shell") })
	for _, path := range []string{"/dashboard/overview", "/dashboard/models", "/dashboard/flow", "/usage-logs/common", "/usage-logs/task", "/models/metadata", "/client-model-routes", "/system-settings/models/global", "/system-settings/models/routing-reliability", "/pricing", "/keys"} {
		t.Run(path, func(t *testing.T) {
			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
			require.Equal(t, 200, w.Code, path)
			assert.Contains(t, w.Body.String(), "application shell")
		})
	}
	for _, path := range []string{"/api/unsupported", "/platform/v1/unsupported", "/system-settings/models/unknown", "/system-settings/auth/oidc", "/keys/secrets"} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		assert.GreaterOrEqual(t, w.Code, 400, path)
		assert.NotContains(t, w.Body.String(), "application shell")
	}
}

func TestUniHubProductRuntimeOptionPermissionAndFiltering(t *testing.T) {
	f := newUniHubRegressionFixture(t)
	require.NoError(t, f.db.AutoMigrate(&model.Option{}, &model.Log{}))
	oldLog := model.LOG_DB
	model.LOG_DB = f.db
	t.Cleanup(func() {
		require.Eventually(t, func() bool { return gopool.WorkerCount() == 0 }, 5*time.Second, 10*time.Millisecond)
		model.LOG_DB = oldLog
	})
	common.OptionMapRWMutex.Lock()
	oldOptions := common.OptionMap
	common.OptionMap = map[string]string{"RetryTimes": "0", "SESSION_SECRET": "do-not-expose", "PaymentAddress": "private-payment", "global.pass_through_request_enabled": "false"}
	common.OptionMapRWMutex.Unlock()
	t.Cleanup(func() {
		common.OptionMapRWMutex.Lock()
		common.OptionMap = oldOptions
		common.OptionMapRWMutex.Unlock()
	})
	viewer, _ := decodeUniHubBundle(t, f.request("POST", "/api/unihub/session", "product-viewer", nil))
	admin, _ := decodeUniHubBundle(t, f.request("POST", "/api/unihub/session", "product-admin", nil))
	get := f.request("GET", "/api/option/", viewer.Data.AccessToken, nil)
	require.Equal(t, 200, get.Code, get.Body.String())
	assert.Contains(t, get.Body.String(), "RetryTimes")
	assert.NotContains(t, get.Body.String(), "SESSION_SECRET")
	assert.NotContains(t, get.Body.String(), "private-payment")
	assert.NotContains(t, get.Body.String(), "CompletionRatioMeta")
	put := func(token, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("PUT", "https://dashboard.test/api/option/", strings.NewReader(body))
		req.Header.Set("Origin", "https://dashboard.test")
		req.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		f.router.ServeHTTP(w, req)
		return w
	}
	require.Equal(t, 403, put(viewer.Data.AccessToken, `{"key":"RetryTimes","value":0}`).Code)
	w := put(admin.Data.AccessToken, `{"key":"RetryTimes","value":0}`)
	require.Equal(t, 200, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), `"success":true`)
	for _, key := range []string{"SESSION_SECRET", "StripeApiKey", "QuotaForNewUser", "PasswordLoginEnabled", "ModelPrice", "model_deployment.ionet.api_key"} {
		w := put(admin.Data.AccessToken, fmt.Sprintf(`{"key":%q,"value":"injected"}`, key))
		assert.Equal(t, 403, w.Code, key+" "+w.Body.String())
	}
	for _, value := range []any{-1, 11, "junk"} {
		body, err := common.Marshal(map[string]any{"key": "RetryTimes", "value": value})
		require.NoError(t, err)
		w := put(admin.Data.AccessToken, string(body))
		assert.Equal(t, 400, w.Code, w.Body.String())
	}
	badOrigin := httptest.NewRequest("PUT", "https://dashboard.test/api/option/", strings.NewReader(`{"key":"RetryTimes","value":0}`))
	badOrigin.Header.Set("Authorization", "Bearer "+admin.Data.AccessToken)
	badOrigin.Header.Set("Origin", "https://attacker.test")
	denied := httptest.NewRecorder()
	f.router.ServeHTTP(denied, badOrigin)
	assert.Equal(t, 403, denied.Code)
	var local model.User
	require.NoError(t, f.db.First(&local, admin.Data.User.Id).Error)
	assert.Equal(t, common.RoleCommonUser, local.Role)
	assert.Zero(t, local.Quota)
}

func TestUniHubProductCapabilitiesAndCatalogAuthorization(t *testing.T) {
	f := newUniHubRegressionFixture(t)
	require.NoError(t, f.db.AutoMigrate(&model.Vendor{}, &model.Model{}, &model.PrefillGroup{}, &model.Log{}))
	oldLog := model.LOG_DB
	model.LOG_DB = f.db
	t.Cleanup(func() {
		require.Eventually(t, func() bool { return gopool.WorkerCount() == 0 }, 5*time.Second, 10*time.Millisecond)
		model.LOG_DB = oldLog
	})
	member, _ := decodeUniHubBundle(t, f.request("POST", "/api/unihub/session", "subject", nil))
	viewer, _ := decodeUniHubBundle(t, f.request("POST", "/api/unihub/session", "product-viewer", nil))
	admin, _ := decodeUniHubBundle(t, f.request("POST", "/api/unihub/session", "product-admin", nil))
	for _, item := range []struct {
		b     uniHubRegressionBundle
		write bool
	}{{viewer, false}, {admin, true}} {
		access := f.request("GET", "/api/unihub/access", item.b.Data.AccessToken, nil)
		require.Equal(t, 200, access.Code)
		var data struct {
			Permissions struct {
				Admin map[string]map[string]bool `json:"admin_permissions"`
			} `json:"permissions"`
		}
		require.NoError(t, common.Unmarshal(access.Body.Bytes(), &data))
		for _, resource := range []string{"catalog", "log", "routing", "settings"} {
			assert.True(t, data.Permissions.Admin[resource]["read"], resource)
		}
		assert.Equal(t, item.write, data.Permissions.Admin["catalog"]["write"])
	}
	request := func(method, path, token, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "https://dashboard.test"+path, strings.NewReader(body))
		req.Header.Set("Origin", "https://dashboard.test")
		req.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		f.router.ServeHTTP(w, req)
		return w
	}
	for _, token := range []string{member.Data.AccessToken, viewer.Data.AccessToken} {
		w := request("POST", "/api/vendors/", token, `{"name":"vendor"}`)
		assert.Equal(t, 403, w.Code, w.Body.String())
	}
	create := request("POST", "/api/vendors/", admin.Data.AccessToken, `{"name":"vendor","status":1}`)
	require.Equal(t, 200, create.Code, create.Body.String())
	assert.Contains(t, create.Body.String(), `"success":true`)
	var vendor model.Vendor
	require.NoError(t, f.db.Where("name = ?", "vendor").First(&vendor).Error)
	read := request("GET", "/api/vendors/", viewer.Data.AccessToken, "")
	require.Equal(t, 200, read.Code, read.Body.String())
	assert.Contains(t, read.Body.String(), "vendor")
	update := request("PUT", "/api/vendors/", admin.Data.AccessToken, fmt.Sprintf(`{"id":%d,"name":"updated-vendor","status":1}`, vendor.Id))
	require.Equal(t, 200, update.Code, update.Body.String())
	require.NoError(t, f.db.First(&vendor, vendor.Id).Error)
	assert.Equal(t, "updated-vendor", vendor.Name)
	delete := request("DELETE", fmt.Sprintf("/api/vendors/%d", vendor.Id), admin.Data.AccessToken, "")
	require.Equal(t, 200, delete.Code, delete.Body.String())
	for _, path := range []string{"/api/token/", "/api/user/", "/api/native-billing/", "/api/subscription/self", "/api/deployments/", "/api/custom-oauth-provider/", "/api/performance/stats"} {
		assert.Equal(t, 403, request("GET", path, admin.Data.AccessToken, "").Code, path)
	}
	f.revoked.Store(true)
	assert.Equal(t, 401, request("POST", "/api/vendors/", admin.Data.AccessToken, `{"name":"revoked"}`).Code)
	var count int64
	require.NoError(t, f.db.Model(&model.Vendor{}).Where("name = ?", "revoked").Count(&count).Error)
	assert.Zero(t, count)
}

func TestUniHubProductSSOApplicationDestinations(t *testing.T) {
	f := newUniHubSSOFixture(t, nil)
	for _, destination := range []string{"/dashboard/overview?range=7", "/usage-logs/common", "/models/metadata", "/system-settings/models/routing-reliability"} {
		w := httptest.NewRecorder()
		f.router.ServeHTTP(w, httptest.NewRequest("GET", "http://127.0.0.1:58119/api/unihub/login?return_to="+url.QueryEscape(destination), nil))
		require.Equal(t, 303, w.Code)
		callback := f.callback(t, w)
		require.Equal(t, 303, callback.Code)
		assert.Equal(t, destination, callback.Header().Get("Location"))
	}
	for _, destination := range []string{"https://attacker.test/", "//attacker.test", "/%2f%2fattacker.test", "/api/user/token", "/unknown", "/system-settings/auth/oidc"} {
		w := httptest.NewRecorder()
		f.router.ServeHTTP(w, httptest.NewRequest("GET", "http://127.0.0.1:58119/api/unihub/login?return_to="+url.QueryEscape(destination), nil))
		assert.Equal(t, 400, w.Code, destination)
	}
}

func TestUniHubProductClientRoutingCRUDAndRevocation(t *testing.T) {
	f := newUniHubRegressionFixture(t)
	require.NoError(t, f.db.AutoMigrate(&model.Channel{}, &model.ClientModelRoute{}, &model.ClientModelTarget{}, &model.Log{}))
	oldLog := model.LOG_DB
	model.LOG_DB = f.db
	t.Cleanup(func() {
		require.Eventually(t, func() bool { return gopool.WorkerCount() == 0 }, 5*time.Second, 10*time.Millisecond)
		model.LOG_DB = oldLog
	})
	oldRatio := ratio_setting.ModelRatio2JSONString()
	require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(`{"test-model":1}`))
	t.Cleanup(func() { require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(oldRatio)) })
	channel := model.Channel{Type: 1, Name: "product-routing-target", Key: "provider-secret", Status: 1, Models: "test-model", Group: "default"}
	require.NoError(t, f.db.Create(&channel).Error)
	viewer, _ := decodeUniHubBundle(t, f.request("POST", "/api/unihub/session", "product-viewer", nil))
	admin, _ := decodeUniHubBundle(t, f.request("POST", "/api/unihub/session", "product-admin", nil))
	request := func(method, path, token, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "https://dashboard.test"+path, strings.NewReader(body))
		req.Header.Set("Origin", "https://dashboard.test")
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		f.router.ServeHTTP(w, req)
		return w
	}
	payload := fmt.Sprintf(`{"public_id":"product-route","display_name":"Product route","enabled":true,"targets":[{"channel_id":%d,"upstream_model":"test-model","billing_model":"test-model","preferred_wires":"[\"openai_chat\"]","weight":1,"enabled":true}]}`, channel.Id)
	assert.Equal(t, 403, request("POST", "/api/client_model_routes/", viewer.Data.AccessToken, payload).Code)
	created := request("POST", "/api/client_model_routes/", admin.Data.AccessToken, payload)
	require.Equal(t, 200, created.Code, created.Body.String())
	assert.Contains(t, created.Body.String(), `"success":true`)
	assert.NotContains(t, created.Body.String(), "provider-secret")
	var route model.ClientModelRoute
	require.NoError(t, f.db.Where("public_id = ?", "product-route").First(&route).Error)
	read := request("GET", "/api/client_model_routes/", viewer.Data.AccessToken, "")
	require.Equal(t, 200, read.Code, read.Body.String())
	assert.Contains(t, read.Body.String(), "product-route")
	assert.NotContains(t, read.Body.String(), "provider-secret")
	update := strings.Replace(payload, `"public_id"`, fmt.Sprintf(`"id":%d,"public_id"`, route.Id), 1)
	update = strings.Replace(update, "Product route", "Updated product route", 1)
	changed := request("PUT", "/api/client_model_routes/", admin.Data.AccessToken, update)
	require.Equal(t, 200, changed.Code, changed.Body.String())
	require.NoError(t, f.db.First(&route, route.Id).Error)
	assert.Equal(t, "Updated product route", route.DisplayName)
	f.revoked.Store(true)
	assert.Equal(t, 401, request("DELETE", fmt.Sprintf("/api/client_model_routes/%d", route.Id), admin.Data.AccessToken, "").Code)
	require.NoError(t, f.db.First(&route, route.Id).Error)
	f.revoked.Store(false)
	deleted := request("DELETE", fmt.Sprintf("/api/client_model_routes/%d", route.Id), admin.Data.AccessToken, "")
	require.Equal(t, 200, deleted.Code, deleted.Body.String())
}
