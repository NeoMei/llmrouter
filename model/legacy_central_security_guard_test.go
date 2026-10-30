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
	"github.com/pquerna/otp/totp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestMixedLegacySecurityChangeCannotInvalidateCentralSession(t *testing.T) {
	for _, operation := range []string{"password", "passkey-register", "passkey-delete", "twofa"} {
		t.Run(operation, func(t *testing.T) {
			oldDB, oldRedis := DB, common.RedisEnabled
			db, err := gorm.Open(sqlite.Open(t.TempDir()+"/mixed.db"), &gorm.Config{})
			require.NoError(t, err)
			require.NoError(t, db.AutoMigrate(&User{}, &UserSession{}, &CentralAuthState{}, &CentralLoginCredential{}, &CentralSessionCredential{}, &NativeLoginCredential{}, &ProductAuthLifecycle{}, &PasskeyCredential{}, &TwoFA{}, &TwoFABackupCode{}))
			DB, common.RedisEnabled = db, false
			t.Cleanup(func() { DB, common.RedisEnabled = oldDB, oldRedis })
			var calls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				_, _ = w.Write([]byte(`{"auth_version":2}`))
			}))
			t.Cleanup(srv.Close)
			t.Setenv("UNIHUB_PRODUCT_RUNTIME_ENABLED", "true")
			t.Setenv("UNIHUB_INDEPENDENT_CENTRAL_REALM_ENABLED", "true")
			t.Setenv("UNIHUB_ORIGIN", srv.URL)
			t.Setenv("UNIHUB_PRODUCT_RUNTIME_TOKEN", strings.Repeat("s", 32))
			hash, err := common.Password2Hash("old-password")
			require.NoError(t, err)
			user := User{Username: "mixed", AffCode: "mixed", Password: hash, Status: common.UserStatusEnabled, AuthVersion: 1, IdentitySource: UniHubIdentitySource, UniHubUserID: "central"}
			require.NoError(t, db.Create(&user).Error)
			legacy := newTestUserSession("mixed-legacy", user.Id, time.Now().Unix())
			central := newTestUserSession("mixed-central", user.Id, time.Now().Unix())
			central.AuthRealm = CentralAuthRealm
			central.UserAuthVersion = 7
			require.NoError(t, db.Create(legacy).Error)
			require.NoError(t, db.Create(central).Error)
			require.NoError(t, db.Create(&CentralAuthState{UserID: user.Id, AuthVersion: 7, Active: true, BindingID: "central-binding"}).Error)
			key, err := common.GenerateTOTPSecret("fixture")
			require.NoError(t, err)
			code, err := totp.GenerateCode(key.Secret(), time.Now())
			require.NoError(t, err)
			require.NoError(t, db.Create(&TwoFA{UserId: user.Id, Secret: key.Secret(), IsEnabled: true}).Error)
			if operation == "passkey-delete" {
				require.NoError(t, db.Create(&PasskeyCredential{UserID: user.Id, CredentialID: "legacy-key", PublicKey: "legacy-public"}).Error)
			}
			switch operation {
			case "password":
				_, err = ChangePasswordForSession(&User{Id: user.Id, Password: "new-password"}, "old-password", legacy.SID, 1, 1, "")
			case "passkey-register":
				_, err = ChangePasskeyForSession(user.Id, legacy.SID, 1, 1, "", &PasskeyCredential{UserID: user.Id, CredentialID: "new-key", PublicKey: "new-public"})
			case "passkey-delete":
				_, err = ChangePasskeyForSession(user.Id, legacy.SID, 1, 1, "", nil)
			case "twofa":
				_, err = ChangeTwoFAForSession(user.Id, legacy.SID, 1, 1, "backup", code, []string{"WXYZ-1234"})
			}
			require.Error(t, err, "mixed legacy mutation must be denied")
			var stored User
			require.NoError(t, db.First(&stored, user.Id).Error)
			assert.EqualValues(t, 1, stored.AuthVersion)
			assert.True(t, common.ValidatePasswordAndHash("old-password", stored.Password))
			var n int64
			require.NoError(t, db.Model(&ProductAuthLifecycle{}).Where("user_id = ?", user.Id).Count(&n).Error)
			assert.Zero(t, n)
			assert.Zero(t, calls.Load())
			var saved UserSession
			require.NoError(t, db.First(&saved, "sid = ?", central.SID).Error)
			assert.Equal(t, UserSessionStatusActive, saved.Status)
			assert.EqualValues(t, 7, saved.UserAuthVersion)
			var authority CentralAuthState
			require.NoError(t, db.First(&authority, "user_id = ?", user.Id).Error)
			assert.True(t, authority.Active)
			assert.EqualValues(t, 7, authority.AuthVersion)
		})
	}
}

func TestLegacySecurityWithoutCentralEvidenceKeepsLifecycleSync(t *testing.T) {
	oldDB, oldRedis := DB, common.RedisEnabled
	db, err := gorm.Open(sqlite.Open(t.TempDir()+"/legacy.db"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&User{}, &UserSession{}, &CentralAuthState{}, &CentralLoginCredential{}, &CentralSessionCredential{}, &ProductAuthLifecycle{}))
	DB, common.RedisEnabled = db, false
	t.Cleanup(func() { DB, common.RedisEnabled = oldDB, oldRedis })
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = w.Write([]byte(`{"auth_version":2}`))
	}))
	t.Cleanup(srv.Close)
	t.Setenv("UNIHUB_PRODUCT_RUNTIME_ENABLED", "true")
	t.Setenv("UNIHUB_INDEPENDENT_CENTRAL_REALM_ENABLED", "true")
	t.Setenv("UNIHUB_ORIGIN", srv.URL)
	t.Setenv("UNIHUB_PRODUCT_RUNTIME_TOKEN", strings.Repeat("s", 32))
	hash, err := common.Password2Hash("old-password")
	require.NoError(t, err)
	user := User{Username: "legacy", AffCode: "legacy", Password: hash, Status: common.UserStatusEnabled, AuthVersion: 1, IdentitySource: UniHubIdentitySource, UniHubUserID: "legacy-projection"}
	require.NoError(t, db.Create(&user).Error)
	session := newTestUserSession("legacy-session", user.Id, time.Now().Unix())
	require.NoError(t, db.Create(session).Error)
	advanced, err := ChangePasswordForSession(&User{Id: user.Id, Password: "new-password"}, "old-password", session.SID, 1, 1, "")
	require.NoError(t, err)
	assert.EqualValues(t, 2, advanced.UserAuthVersion)
	var event ProductAuthLifecycle
	require.NoError(t, db.Where("user_id = ?", user.Id).First(&event).Error)
	assert.True(t, event.Delivered)
	assert.EqualValues(t, 1, event.PreviousVersion)
	assert.EqualValues(t, 2, event.NextVersion)
	assert.EqualValues(t, 1, calls.Load())
}

func TestLegacySecurityFailsClosedWhenCentralEvidenceUnavailable(t *testing.T) {
	oldDB, oldRedis := DB, common.RedisEnabled
	db, err := gorm.Open(sqlite.Open(t.TempDir()+"/missing.db"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&User{}, &UserSession{}, &CentralAuthState{}, &CentralSessionCredential{}, &ProductAuthLifecycle{}))
	DB, common.RedisEnabled = db, false
	t.Cleanup(func() { DB, common.RedisEnabled = oldDB, oldRedis })
	t.Setenv("UNIHUB_INDEPENDENT_CENTRAL_REALM_ENABLED", "true")
	t.Setenv("UNIHUB_PRODUCT_RUNTIME_ENABLED", "false")
	hash, err := common.Password2Hash("old-password")
	require.NoError(t, err)
	user := User{Username: "missing", AffCode: "missing", Password: hash, Status: common.UserStatusEnabled, AuthVersion: 1}
	require.NoError(t, db.Create(&user).Error)
	session := newTestUserSession("missing-session", user.Id, time.Now().Unix())
	require.NoError(t, db.Create(session).Error)
	_, err = ChangePasswordForSession(&User{Id: user.Id, Password: "new-password"}, "old-password", session.SID, 1, 1, "")
	require.Error(t, err)
	var saved User
	require.NoError(t, db.First(&saved, user.Id).Error)
	assert.EqualValues(t, 1, saved.AuthVersion)
	assert.True(t, common.ValidatePasswordAndHash("old-password", saved.Password))
}
