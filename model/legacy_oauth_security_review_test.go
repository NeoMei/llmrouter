package model

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestMixedLegacyOAuthBindingsCannotMutateCentralAuthority(t *testing.T) {
	oldDB, oldRedis := DB, common.RedisEnabled
	db, err := gorm.Open(sqlite.Open(t.TempDir()+"/oauth.db"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&User{}, &UserSession{}, &CentralAuthState{}, &CentralLoginCredential{}, &CentralSessionCredential{}, &NativeLoginCredential{}, &ProductAuthLifecycle{}, &UserOAuthBinding{}))
	DB, common.RedisEnabled = db, false
	t.Cleanup(func() { DB, common.RedisEnabled = oldDB, oldRedis })
	t.Setenv("UNIHUB_PRODUCT_RUNTIME_ENABLED", "false")
	user := User{Username: "mixed-oauth", AffCode: "mixed-oauth", Status: common.UserStatusEnabled, AuthVersion: 1, GitHubId: "old-id"}
	require.NoError(t, db.Create(&user).Error)
	require.NoError(t, db.Create(&CentralAuthState{UserID: user.Id, AuthVersion: 7, BindingID: "central", Active: true}).Error)
	central := newTestUserSession("central-oauth", user.Id, time.Now().Unix())
	central.AuthRealm = CentralAuthRealm
	central.UserAuthVersion = 7
	require.NoError(t, db.Create(central).Error)
	require.NoError(t, db.Create(&UserOAuthBinding{UserId: user.Id, ProviderId: 3, ProviderUserId: "original"}).Error)
	require.ErrorIs(t, UpdateUserBindColumn(user.Id, "github_id", "new-id", ""), ErrLegacyCentralSecurityConflict)
	require.ErrorIs(t, UpdateUserOAuthBinding(user.Id, 3, "replacement", ""), ErrLegacyCentralSecurityConflict)
	require.ErrorIs(t, DeleteUserOAuthBinding(user.Id, 3, ""), ErrLegacyCentralSecurityConflict)
	var stored User
	require.NoError(t, db.First(&stored, user.Id).Error)
	assert.Equal(t, "old-id", stored.GitHubId)
	assert.EqualValues(t, 1, stored.AuthVersion)
	var binding UserOAuthBinding
	require.NoError(t, db.Where("user_id = ? AND provider_id = ?", user.Id, 3).First(&binding).Error)
	assert.Equal(t, "original", binding.ProviderUserId)
	var state UserSession
	require.NoError(t, db.First(&state, "sid = ?", central.SID).Error)
	assert.Equal(t, UserSessionStatusActive, state.Status)
}

func TestPureLegacyOAuthBindingsRemainEditable(t *testing.T) {
	oldDB, oldRedis := DB, common.RedisEnabled
	db, err := gorm.Open(sqlite.Open(t.TempDir()+"/oauth-legacy.db"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&User{}, &UserSession{}, &CentralAuthState{}, &CentralLoginCredential{}, &CentralSessionCredential{}, &NativeLoginCredential{}, &ProductAuthLifecycle{}, &UserOAuthBinding{}))
	DB, common.RedisEnabled = db, false
	t.Cleanup(func() { DB, common.RedisEnabled = oldDB, oldRedis })
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var request struct {
			Next int64 `json:"next_auth_version"`
		}
		if err := common.DecodeJson(r.Body, &request); err != nil {
			w.WriteHeader(400)
			return
		}
		data, err := common.Marshal(map[string]int64{"auth_version": request.Next})
		if err != nil {
			w.WriteHeader(500)
			return
		}
		_, _ = w.Write(data)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("UNIHUB_PRODUCT_RUNTIME_ENABLED", "true")
	t.Setenv("UNIHUB_ORIGIN", srv.URL)
	t.Setenv("UNIHUB_PRODUCT_RUNTIME_TOKEN", strings.Repeat("s", 32))
	user := User{Username: "pure-oauth", AffCode: "pure-oauth", Status: common.UserStatusEnabled, AuthVersion: 1, IdentitySource: UniHubIdentitySource}
	require.NoError(t, db.Create(&user).Error)
	require.NoError(t, UpdateUserBindColumn(user.Id, "github_id", "fixture-id", ""))
	require.NoError(t, UpdateUserOAuthBinding(user.Id, 3, "fixture-subject", ""))
	require.NoError(t, DeleteUserOAuthBinding(user.Id, 3, ""))
	var stored User
	require.NoError(t, db.First(&stored, user.Id).Error)
	assert.Equal(t, "fixture-id", stored.GitHubId)
	assert.EqualValues(t, 3, calls.Load())
	var delivered int64
	require.NoError(t, db.Model(&ProductAuthLifecycle{}).Where("user_id = ? AND delivered = ?", user.Id, true).Count(&delivered).Error)
	assert.EqualValues(t, 3, delivered)
}
