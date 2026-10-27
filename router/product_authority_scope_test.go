package router

import (
	"encoding/json"
	userkit "github.com/NeoMei/UniHub/packages/userkit-go"
	"github.com/QuantumNous/new-api/internal/platformaccount"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/bytedance/gopkg/util/gopool"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestManagedAdminRoutesRequireCurrentResourceGrant(t *testing.T) {
	f := newUniHubRegressionFixture(t)
	t.Setenv("NATIVE_PUBLIC_ACCESS", "true")
	require.NoError(t, f.db.AutoMigrate(&model.ProductAuthLifecycle{}, &model.Redemption{}, &model.SubscriptionPlan{}, &model.Token{}, &model.Log{}))
	oldLog := model.LOG_DB
	model.LOG_DB = f.db
	t.Cleanup(func() {
		require.Eventually(t, func() bool { return gopool.WorkerCount() == 0 }, 5*time.Second, time.Millisecond*10)
		model.LOG_DB = oldLog
	})
	payment := operation_setting.GetPaymentSetting()
	oldPayment := *payment
	payment.ComplianceConfirmed = true
	payment.ComplianceTermsVersion = operation_setting.CurrentComplianceTermsVersion
	t.Cleanup(func() { *payment = oldPayment })
	currentRole := 10
	permissions := []string{"product:use", "channel:read"}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/internal/v1/product-runtime/identity/") {
			w.WriteHeader(404)
			return
		}
		id := strings.TrimPrefix(r.URL.Path, "/internal/v1/product-runtime/identity/")
		json.NewEncoder(w).Encode(userkit.ProductIdentity{Binding: userkit.RuntimeBinding{ID: "binding", LocalUserID: id, UserID: "central", ProductID: "llmrouter", InstanceID: "default"}, Access: userkit.Access{Issuer: "https://authority.test", Subject: "central", User: userkit.User{ID: "central"}, ProductID: "llmrouter", InstanceID: "default", Active: true, ProductRole: currentRole, Permissions: permissions, ExpiresAt: time.Now().Add(time.Minute).Unix()}, ProductRole: currentRole, AuthVersion: 1})
	}))
	defer srv.Close()
	user := model.User{Username: "native-managed", AffCode: "native-managed", AuthVersion: 1, Status: 1, Role: 100}
	require.NoError(t, f.db.Create(&user).Error)
	_, e := model.BindUniHubIdentity(platformaccount.Identity{Issuer: "https://authority.test", User: platformaccount.User{ID: "central"}}, user.Id)
	require.NoError(t, e)
	b, e := service.CreateLoginSession(user.Id, "password", "", "")
	require.NoError(t, e)
	t.Setenv("UNIHUB_PRODUCT_RUNTIME_ENABLED", "true")
	t.Setenv("UNIHUB_ORIGIN", srv.URL)
	t.Setenv("UNIHUB_PRODUCT_RUNTIME_TOKEN", strings.Repeat("s", 32))
	send := func(method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "https://dashboard.test"+path, strings.NewReader(body))
		r.Header.Set("Origin", "https://dashboard.test")
		r.Header.Set("Authorization", "Bearer "+b.AccessToken)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		f.router.ServeHTTP(w, r)
		return w
	}
	f.router.GET("/api/brand-new-admin-operation", middleware.AdminAuth(), func(c *gin.Context) { c.Status(204) })
	for _, path := range []string{"/api/redemption/", "/api/subscription/admin/plans", "/api/deployments/", "/api/user/topup", "/api/option/channel_affinity_cache", "/api/brand-new-admin-operation"} {
		got := send("GET", path, "")
		require.Equal(t, 403, got.Code, path+got.Body.String())
	}
	got := send("POST", "/api/redemption/", `{"name":"scoped","count":1,"quota":5,"expired_time":0}`)
	require.Equal(t, 403, got.Code, got.Body.String())
	var count int64
	require.NoError(t, f.db.Model(&model.Redemption{}).Count(&count).Error)
	require.Zero(t, count)
	permissions = []string{"product:use", "redemption:read", "redemption:write", "subscription:read", "subscription:write", "deployment:read", "user:read", "user:write", "billing:read"}
	for _, path := range []string{"/api/redemption/", "/api/subscription/admin/plans"} {
		got = send("GET", path, "")
		require.Equal(t, 200, got.Code, path+got.Body.String())
	}
	got = send("POST", "/api/redemption/", `{"name":"scoped","count":1,"quota":5,"expired_time":0}`)
	require.Equal(t, 200, got.Code, got.Body.String())
	require.Contains(t, got.Body.String(), `"success":true`)
	require.NoError(t, f.db.Model(&model.Redemption{}).Count(&count).Error)
	require.EqualValues(t, 1, count)
	currentRole = 1
	permissions = []string{"product:use"}
	require.Equal(t, 403, send("POST", "/api/redemption/", `{"name":"stale","count":1,"quota":5}`).Code)
	require.NoError(t, f.db.Model(&model.Redemption{}).Count(&count).Error)
	require.EqualValues(t, 1, count)
	var stored model.User
	require.NoError(t, f.db.First(&stored, strconv.Itoa(user.Id)).Error)
	require.Equal(t, 100, stored.Role, "test must exercise stale SQL authority")
}
