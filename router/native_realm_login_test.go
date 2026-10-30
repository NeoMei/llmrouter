package router

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"github.com/bytedance/gopkg/util/gopool"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/service/authz"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNativeRealmPasswordLoginAndKeyManagement(t *testing.T) {
	f := newUniHubRegressionFixture(t)
	t.Setenv("UNIHUB_PRODUCT_RUNTIME_ENABLED", "true")
	t.Setenv("UNIHUB_ORIGIN", "http://127.0.0.1:1")
	t.Setenv("UNIHUB_PRODUCT_RUNTIME_TOKEN", strings.Repeat("s", 32))
	t.Setenv("NATIVE_PUBLIC_ACCESS", "true")
	require.NoError(t, f.db.AutoMigrate(&model.Token{}, &model.TwoFA{}, &model.Log{}, &model.ProductAuthLifecycle{}, &model.CasbinRule{}, &model.AuthzRole{}))
	require.NoError(t, model.RegisterProductFundingModels(f.db))
	require.NoError(t, authz.Init(f.db))
	oldLog := model.LOG_DB
	model.LOG_DB = f.db
	t.Cleanup(func() {
		require.Eventually(t, func() bool { return gopool.WorkerCount() == 0 }, time.Second, time.Millisecond)
		model.LOG_DB = oldLog
	})
	hash, err := common.Password2Hash("synthetic-native-pass")
	require.NoError(t, err)
	user := model.User{Username: "native-realm", Password: hash, Role: 100, Status: 1, AuthVersion: 1, AffCode: "native-realm", Quota: 1000, Group: "default"}
	require.NoError(t, f.db.Create(&user).Error)
	digest := sha256.Sum256([]byte(hash))
	raw, err := common.Marshal(model.NativeRealmManifest{Version: 1, Users: []model.NativeRealmEnrollment{{UserID: user.Id, Role: 100, Status: 1, AuthVersion: 1, PasswordSHA256: hex.EncodeToString(digest[:])}}})
	require.NoError(t, err)
	approved := sha256.Sum256(raw)
	require.NoError(t, model.EnrollNativeRealm(context.Background(), f.db, raw, hex.EncodeToString(approved[:])))
	quotaManifest, err := common.Marshal(model.NativeTokenQuotaManifest{Version: 1, CutoverEvidence: strings.Repeat("a", 64)})
	require.NoError(t, err)
	quotaApproved := sha256.Sum256(quotaManifest)
	require.NoError(t, model.ImportNativeTokenQuota(context.Background(), f.db, quotaManifest, hex.EncodeToString(quotaApproved[:])))
	send := func(method, path, bearer, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "https://dashboard.test"+path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", "https://dashboard.test")
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		out := httptest.NewRecorder()
		f.router.ServeHTTP(out, req)
		return out
	}
	login := send("POST", "/api/user/login", "", `{"username":"native-realm","password":"synthetic-native-pass","auth_realm":"unihub"}`)
	require.Equal(t, 200, login.Code)
	var response struct {
		Success bool `json:"success"`
		Data    struct {
			AccessToken string `json:"access_token"`
		} `json:"data"`
	}
	require.NoError(t, common.Unmarshal(login.Body.Bytes(), &response))
	require.True(t, response.Success, login.Body.String())
	require.NotEmpty(t, response.Data.AccessToken)
	identity, err := service.ParseAccessToken(response.Data.AccessToken)
	require.NoError(t, err)
	native, err := service.IsNativeLoginSession(context.Background(), identity)
	require.NoError(t, err)
	require.True(t, native)
	added := send("POST", "/api/token/", response.Data.AccessToken, `{"name":"native-created","remain_quota":100,"expired_time":-1,"unlimited_quota":false}`)
	require.Equal(t, 200, added.Code)
	assert.Contains(t, added.Body.String(), `"success":true`)
	var key model.Token
	require.NoError(t, f.db.Where("user_id = ?", user.Id).First(&key).Error)
	assert.Equal(t, model.NativeFundingLocalOnly, key.AuthRealm)
	created := send("POST", "/api/user/", response.Data.AccessToken, `{"username":"native-new","password":"native-new-pass","role":1,"funding_realm":"central"}`)
	require.Equal(t, 200, created.Code, created.Body.String())
	require.Contains(t, created.Body.String(), `"success":true`)
	var newUser model.User
	require.NoError(t, f.db.Where("username = ?", "native-new").First(&newUser).Error)
	assert.Equal(t, model.NativeFundingLocalOnly, newUser.FundingRealm)
	assert.True(t, newUser.NativePassword)
	assert.False(t, newUser.ProductRegistrationPending)
	assert.Empty(t, newUser.IdentitySource)
	assert.Contains(t, newUser.FundingRealmEvidence, "native-admin:")
	newLogin := send("POST", "/api/user/login", "", `{"username":"native-new","password":"native-new-pass"}`)
	require.Equal(t, 200, newLogin.Code)
	require.Contains(t, newLogin.Body.String(), `"success":true`)
	var newBundle struct {
		Data struct {
			AccessToken string `json:"access_token"`
		} `json:"data"`
	}
	require.NoError(t, common.Unmarshal(newLogin.Body.Bytes(), &newBundle))
	createdIdentity, err := service.ParseAccessToken(newBundle.Data.AccessToken)
	require.NoError(t, err)
	isNative, err := service.IsNativeLoginSession(context.Background(), createdIdentity)
	require.NoError(t, err)
	assert.True(t, isNative)
	denied := send("POST", "/api/user/", newBundle.Data.AccessToken, `{"username":"cannot-create","password":"native-new-pass","role":1}`)
	assert.NotContains(t, denied.Body.String(), `"success":true`)
	var prohibited int64
	require.NoError(t, f.db.Model(&model.User{}).Where("username = ?", "cannot-create").Count(&prohibited).Error)
	assert.Zero(t, prohibited)
	target := model.User{Username: "native-target", Password: hash, Role: 1, Status: 1, AuthVersion: 1, AffCode: "native-target", Group: "default"}
	require.NoError(t, f.db.Create(&target).Error)
	targetRaw, err := common.Marshal(model.NativeRealmManifest{Version: 1, Users: []model.NativeRealmEnrollment{{UserID: target.Id, Role: 1, Status: 1, AuthVersion: 1, PasswordSHA256: hex.EncodeToString(digest[:])}}})
	require.NoError(t, err)
	targetApproved := sha256.Sum256(targetRaw)
	require.NoError(t, model.EnrollNativeRealm(context.Background(), f.db, targetRaw, hex.EncodeToString(targetApproved[:])))
	changed := send("POST", "/api/user/manage", response.Data.AccessToken, fmt.Sprintf(`{"id":%d,"action":"disable"}`, target.Id))
	require.Equal(t, 200, changed.Code, changed.Body.String())
	require.Contains(t, changed.Body.String(), `"success":true`)
	require.NoError(t, f.db.First(&target, target.Id).Error)
	assert.Equal(t, common.UserStatusDisabled, target.Status)
	self := send("GET", "/api/user/self", response.Data.AccessToken, "")
	require.Equal(t, 200, self.Code)
	assert.Contains(t, self.Body.String(), `"role":100`)
}
