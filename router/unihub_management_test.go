package router

import (
	"fmt"
	userkit "github.com/NeoMei/UniHub/packages/userkit-go"
	"github.com/QuantumNous/new-api/internal/platformaccount"
	"github.com/QuantumNous/new-api/service/authz"
	"github.com/bytedance/gopkg/util/gopool"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUniHubLiveChannelManagement(t *testing.T) {
	f := newUniHubRegressionFixture(t)
	require.NoError(t, f.db.AutoMigrate(&model.Channel{}, &model.Ability{}, &model.UniHubSessionBinding{}))
	channel := model.Channel{Type: 1, Name: "controlled-local", Key: "upstream-secret", Status: 1, Models: "test-model", Group: "default"}
	require.NoError(t, f.db.Create(&channel).Error)
	for _, tc := range []struct {
		subject     string
		read, write int
	}{{"ordinary", 403, 403}, {"viewer", 200, 403}, {"operator", 200, 403}, {"admin", 200, 200}} {
		t.Run(tc.subject, func(t *testing.T) {
			b, _ := decodeUniHubBundle(t, f.request("POST", "/api/unihub/session", tc.subject, nil))
			got := f.request("GET", "/api/channel/", b.Data.AccessToken, nil)
			assert.Equal(t, tc.read, got.Code, got.Body.String())
			assert.NotContains(t, got.Body.String(), "upstream-secret")
			req := f.request("GET", "/api/channel/test/999999", b.Data.AccessToken, nil)
			if tc.subject == "ordinary" || tc.subject == "viewer" {
				assert.Equal(t, 403, req.Code)
			} else {
				assert.NotEqual(t, 403, req.Code)
			}
			for _, path := range []string{"/api/token/", "/api/user/", "/api/option/", "/api/topup/"} {
				assert.NotEqual(t, 200, f.request("GET", path, b.Data.AccessToken, nil).Code, path)
			}
			assert.Equal(t, 403, f.request("POST", "/api/channel/1/key", b.Data.AccessToken, nil).Code)
			var local model.User
			require.NoError(t, f.db.First(&local, b.Data.User.Id).Error)
			assert.Equal(t, common.RoleCommonUser, local.Role)
			assert.Zero(t, local.Quota)
			var binding model.UniHubSessionBinding
			require.NoError(t, f.db.First(&binding, "sid = ?", b.Data.Session.SID).Error)
			assert.NotContains(t, binding.Ciphertext, tc.subject)
			var native model.UserSession
			require.NoError(t, f.db.First(&native, "sid = ?", b.Data.Session.SID).Error)
			assert.LessOrEqual(t, native.ExpiresAt, time.Now().Add(time.Hour).Unix())
			f.revoked.Store(true)
			assert.Equal(t, 401, f.request("GET", "/api/channel/", b.Data.AccessToken, nil).Code)
			assert.Equal(t, 401, f.request("GET", "/api/unihub/access", b.Data.AccessToken, nil).Code)
			assert.Equal(t, 401, f.request("GET", "/api/unihub/bff/v1/accounts", b.Data.AccessToken, nil).Code)
			f.revoked.Store(false)
			require.NoError(t, f.db.Model(&model.UniHubSessionBinding{}).Where("sid = ?", b.Data.Session.SID).Update("ciphertext", strings.Repeat("a", 80)).Error)
			assert.Equal(t, 401, f.request("GET", "/api/channel/", b.Data.AccessToken, nil).Code)
		})
	}
	native := model.User{Username: "native-admin", Role: common.RoleAdminUser, Status: 1, AuthVersion: 1, AffCode: "native-admin"}
	require.NoError(t, f.db.Create(&native).Error)
	bundle, e := service.CreateLoginSession(native.Id, "password", "", "")
	require.NoError(t, e)
	assert.NotEqual(t, 200, f.request("GET", "/api/channel/", bundle.AccessToken, nil).Code)
}

func TestUniHubSessionUpstreamExpiryCapsAccessAndRefresh(t *testing.T) {
	f := newUniHubRegressionFixture(t)
	a := userkit.Access{Issuer: "https://hub.test", Subject: "central", ProductID: "llmrouter", InstanceID: "default", User: userkit.User{ID: "central"}, Active: true, Version: 1, ExpiresAt: time.Now().Add(45 * time.Second).Unix()}
	user, e := model.ResolveUniHubProjection(platformaccount.Identity{Issuer: a.Issuer, User: platformaccount.User{ID: a.Subject}})
	require.NoError(t, e)
	bundle, e := service.CreateUniHubLoginSession(user.Id, a.ExpiresAt, "", "")
	require.NoError(t, e)
	assert.LessOrEqual(t, bundle.AccessExpiresAt, a.ExpiresAt)
	assert.LessOrEqual(t, bundle.Session.ExpiresAt, a.ExpiresAt)
	_ = f
}

func TestUniHubChannelWriteUsesCurrentPrincipalAndAuditIdentity(t *testing.T) {
	f := newUniHubRegressionFixture(t)
	require.NoError(t, f.db.AutoMigrate(&model.Channel{}, &model.Ability{}, &model.Log{}))
	oldLog := model.LOG_DB
	model.LOG_DB = f.db
	t.Cleanup(func() {
		require.Eventually(t, func() bool { return gopool.WorkerCount() == 0 }, 5*time.Second, 10*time.Millisecond)
		model.LOG_DB = oldLog
	})
	channel := model.Channel{Type: 1, Name: "before", Key: "kept-secret", Status: 1, Models: "test-model", Group: "default"}
	require.NoError(t, f.db.Create(&channel).Error)
	send := func(token string, payload string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("PUT", "https://dashboard.test/api/channel/", strings.NewReader(payload))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		f.router.ServeHTTP(w, req)
		return w
	}
	viewer, _ := decodeUniHubBundle(t, f.request("POST", "/api/unihub/session", "viewer", nil))
	admin, _ := decodeUniHubBundle(t, f.request("POST", "/api/unihub/session", "admin", nil))
	payload := fmt.Sprintf(`{"id":%d,"type":1,"name":"after","models":"test-model","group":"default"}`, channel.Id)
	assert.Equal(t, 403, send(viewer.Data.AccessToken, payload).Code)
	w := send(admin.Data.AccessToken, payload)
	require.Equal(t, 200, w.Code, w.Body.String())
	require.Contains(t, w.Body.String(), `"success":true`)
	var changed model.Channel
	require.NoError(t, f.db.First(&changed, channel.Id).Error)
	assert.Equal(t, "after", changed.Name)
	assert.Equal(t, "kept-secret", changed.Key)
	sensitive := fmt.Sprintf(`{"id":%d,"type":1,"name":"after","key":"attempted-change","models":"test-model","group":"default"}`, channel.Id)
	w = send(admin.Data.AccessToken, sensitive)
	require.Contains(t, w.Body.String(), `"success":false`)
	require.NoError(t, f.db.First(&changed, channel.Id).Error)
	assert.Equal(t, "kept-secret", changed.Key)
	require.Eventually(t, func() bool {
		var count int64
		_ = f.db.Model(&model.Log{}).Where("user_id = ? AND type = ?", admin.Data.User.Id, model.LogTypeManage).Count(&count).Error
		return count > 0
	}, 5*time.Second, 10*time.Millisecond)
	var audit model.Log
	require.NoError(t, f.db.Where("user_id = ? AND type = ?", admin.Data.User.Id, model.LogTypeManage).First(&audit).Error)
	assert.Contains(t, audit.Other, fmt.Sprintf(`"admin_id":%d`, admin.Data.User.Id))
	assert.Contains(t, audit.Other, `"admin_role":1`)
	f.revoked.Store(true)
	assert.Equal(t, 401, send(admin.Data.AccessToken, payload).Code)
}

func TestUniHubStandaloneNativeAdminStillManagesChannels(t *testing.T) {
	f := newUniHubRegressionFixture(t)
	t.Setenv("UNIHUB_PRODUCT_ENABLED", "false")
	t.Setenv("UNIHUB_PLATFORM_ENABLED", "false")
	t.Setenv("NATIVE_PUBLIC_ACCESS", "true")
	require.NoError(t, f.db.AutoMigrate(&model.Channel{}, &model.Ability{}, &model.CasbinRule{}, &model.AuthzRole{}))
	oldMaster := common.IsMasterNode
	common.IsMasterNode = true
	t.Cleanup(func() { common.IsMasterNode = oldMaster })
	require.NoError(t, authz.Init(f.db))
	native := model.User{Username: "standalone-admin", Role: common.RoleAdminUser, Status: 1, AuthVersion: 1, AffCode: "standalone-admin"}
	require.NoError(t, f.db.Create(&native).Error)
	bundle, e := service.CreateLoginSession(native.Id, "password", "", "")
	require.NoError(t, e)
	got := f.request("GET", "/api/channel/", bundle.AccessToken, nil)
	assert.Equal(t, 200, got.Code, got.Body.String())
}

func TestUniHubExpiredProofAndLocalRevocationRejectAndPurge(t *testing.T) {
	f := newUniHubRegressionFixture(t)
	b, cookie := decodeUniHubBundle(t, f.request("POST", "/api/unihub/session", "viewer", nil))
	require.NoError(t, f.db.Model(&model.UniHubSessionBinding{}).Where("sid = ?", b.Data.Session.SID).Update("expires_at", time.Now().Add(-time.Second).Unix()).Error)
	assert.Equal(t, 401, f.request("GET", "/api/unihub/access", b.Data.AccessToken, nil).Code)
	assert.Equal(t, 401, f.request("POST", "/api/user/auth/refresh", "", cookie).Code)
	assert.Equal(t, 200, f.request("POST", "/api/user/auth/logout", b.Data.AccessToken, cookie).Code)
	require.NoError(t, model.PurgeUniHubSessionBindings(time.Now().Unix()))
	var count int64
	require.NoError(t, f.db.Model(&model.UniHubSessionBinding{}).Count(&count).Error)
	assert.Zero(t, count)
	other, _ := decodeUniHubBundle(t, f.request("POST", "/api/unihub/session", "viewer", nil))
	_, err := model.RevokeUserSession(other.Data.User.Id, other.Data.Session.SID, "explicit-test-revoke")
	require.NoError(t, err)
	assert.Equal(t, 401, f.request("GET", "/api/channel/", other.Data.AccessToken, nil).Code)
}
