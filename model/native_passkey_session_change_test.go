package model

import (
	"errors"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/glebarez/sqlite"
	"github.com/pquerna/otp/totp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestStalePasskeyChangesAfterTwoFAHaveNoEffect(t *testing.T) {
	for _, operation := range []string{"register", "delete"} {
		t.Run(operation, func(t *testing.T) {
			oldDB, oldRedis := DB, common.RedisEnabled
			db, err := gorm.Open(sqlite.Open(t.TempDir()+"/passkey-race.db?_pragma=busy_timeout(5000)"), &gorm.Config{})
			require.NoError(t, err)
			require.NoError(t, db.AutoMigrate(&User{}, &UserSession{}, &TwoFA{}, &TwoFABackupCode{}, &PasskeyCredential{}, &NativeLoginCredential{}, &CentralLoginCredential{}, &ProductAuthLifecycle{}))
			DB, common.RedisEnabled = db, false
			t.Cleanup(func() { DB, common.RedisEnabled = oldDB, oldRedis })
			t.Setenv("UNIHUB_PRODUCT_RUNTIME_ENABLED", "false")
			user := User{Username: "passkey-race", AffCode: "passkey-race", Status: common.UserStatusEnabled, AuthVersion: 1, FundingRealm: NativeFundingLocalOnly, FundingRealmEvidence: "reviewed", NativePassword: true}
			require.NoError(t, db.Create(&user).Error)
			session := newTestUserSession("passkey-race-session", user.Id, time.Now().Unix())
			session.AuthRealm = NativeFundingLocalOnly
			require.NoError(t, db.Create(session).Error)
			key, err := common.GenerateTOTPSecret("fixture")
			require.NoError(t, err)
			code, err := totp.GenerateCode(key.Secret(), time.Now())
			require.NoError(t, err)
			require.NoError(t, db.Create(&TwoFA{UserId: user.Id, Secret: key.Secret(), IsEnabled: true}).Error)
			oldCredential := &PasskeyCredential{UserID: user.Id, CredentialID: "old-key", PublicKey: "old-public"}
			if operation == "delete" {
				require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
					if err := SetNativeLoginCredentialTx(tx, user.Id, "passkey", oldCredential.CredentialID, NativeFundingLocalOnly); err != nil {
						return err
					}
					return tx.Create(oldCredential).Error
				}))
			}
			// Both requests were authenticated as v1 before this 2FA change commits.
			advanced, err := ChangeTwoFAForSession(user.Id, session.SID, 1, 1, "backup", code, []string{"WXYZ-1234"})
			require.NoError(t, err)
			var credential *PasskeyCredential
			if operation == "register" {
				credential = &PasskeyCredential{UserID: user.Id, CredentialID: "new-key", PublicKey: "new-public"}
			}
			_, err = ChangePasskeyForSession(user.Id, session.SID, 1, 1, NativeFundingLocalOnly, credential)
			require.ErrorIs(t, err, ErrUserSessionInactive)
			var persisted User
			require.NoError(t, db.First(&persisted, user.Id).Error)
			assert.EqualValues(t, 2, persisted.AuthVersion)
			var current UserSession
			require.NoError(t, db.First(&current, "sid = ?", session.SID).Error)
			assert.Equal(t, advanced.Version, current.Version)
			assert.Equal(t, advanced.UserAuthVersion, current.UserAuthVersion)
			var count int64
			require.NoError(t, db.Model(&PasskeyCredential{}).Where("user_id = ?", user.Id).Count(&count).Error)
			if operation == "register" {
				assert.Zero(t, count)
			} else {
				assert.EqualValues(t, 1, count)
			}
		})
	}
}

func TestPasskeySessionChangeUpdatesCacheAndPreservesOtherRealms(t *testing.T) {
	setupUserSessionTest(t)
	require.NoError(t, DB.AutoMigrate(&PasskeyCredential{}, &NativeLoginCredential{}, &CentralLoginCredential{}, &ProductAuthLifecycle{}))
	useUserCacheMiniRedis(t)
	t.Setenv("UNIHUB_PRODUCT_RUNTIME_ENABLED", "false")
	createUserSessionTestUser(t, 741, 1)
	require.NoError(t, DB.Model(&User{}).Where("id = ?", 741).Updates(map[string]interface{}{"funding_realm": NativeFundingLocalOnly, "funding_realm_evidence": "reviewed", "native_password": true}).Error)
	now := time.Now().Unix()
	current := newTestUserSession("passkey-current", 741, now)
	current.AuthRealm = NativeFundingLocalOnly
	other := newTestUserSession("passkey-other", 741, now)
	other.AuthRealm = NativeFundingLocalOnly
	central := newTestUserSession("passkey-central", 741, now)
	central.AuthRealm = CentralAuthRealm
	for _, item := range []*UserSession{current, other, central} {
		require.NoError(t, DB.Create(item).Error)
		_, err := GetUserSessionCached(item.SID)
		require.NoError(t, err)
	}
	_, err := GetUserCache(741)
	require.NoError(t, err)
	credential := &PasskeyCredential{UserID: 741, CredentialID: "fixture-key", PublicKey: "fixture-public"}
	advanced, err := ChangePasskeyForSession(741, current.SID, 1, 1, NativeFundingLocalOnly, credential)
	require.NoError(t, err)
	assert.EqualValues(t, 2, advanced.Version)
	assert.EqualValues(t, 2, advanced.UserAuthVersion)
	cached, err := GetUserSessionCached(current.SID)
	require.NoError(t, err)
	assert.EqualValues(t, 2, cached.UserAuthVersion)
	_, err = GetUserSessionCached(other.SID)
	assert.ErrorIs(t, err, ErrUserSessionInactive)
	centralCached, err := GetUserSessionCached(central.SID)
	require.NoError(t, err)
	assert.Equal(t, UserSessionStatusActive, centralCached.Status)
	valid, err := IsNativeLoginCredential(t.Context(), 741, "passkey", "fixture-key")
	require.NoError(t, err)
	assert.True(t, valid)
	advanced, err = ChangePasskeyForSession(741, current.SID, 2, 2, NativeFundingLocalOnly, nil)
	require.NoError(t, err)
	assert.EqualValues(t, 3, advanced.UserAuthVersion)
	_, err = GetPasskeyByUserID(741)
	assert.ErrorIs(t, err, ErrPasskeyNotFound)
}

func TestPasskeyChangeRollsBackOnSessionFailureAndKeepsFence(t *testing.T) {
	setupUserSessionTest(t)
	require.NoError(t, DB.AutoMigrate(&PasskeyCredential{}, &NativeLoginCredential{}, &CentralLoginCredential{}, &ProductAuthLifecycle{}))
	server := useUserCacheMiniRedis(t)
	t.Setenv("UNIHUB_PRODUCT_RUNTIME_ENABLED", "false")
	createUserSessionTestUser(t, 742, 1)
	require.NoError(t, DB.Model(&User{}).Where("id = ?", 742).Updates(map[string]interface{}{"funding_realm": NativeFundingLocalOnly, "funding_realm_evidence": "reviewed", "native_password": true}).Error)
	session := newTestUserSession("passkey-fence", 742, time.Now().Unix())
	session.AuthRealm = NativeFundingLocalOnly
	require.NoError(t, DB.Create(session).Error)
	_, err := GetUserCache(742)
	require.NoError(t, err)
	require.NoError(t, DB.Callback().Update().Before("gorm:update").Register("test:passkey_session_failure", func(tx *gorm.DB) {
		if tx.Statement.Table == "user_sessions" {
			tx.AddError(errors.New("session unavailable"))
		}
	}))
	_, err = ChangePasskeyForSession(742, session.SID, 1, 1, NativeFundingLocalOnly, &PasskeyCredential{UserID: 742, CredentialID: "fixture-key", PublicKey: "fixture-public"})
	require.Error(t, err)
	require.NoError(t, DB.Callback().Update().Remove("test:passkey_session_failure"))
	_, err = GetPasskeyByUserID(742)
	assert.ErrorIs(t, err, ErrPasskeyNotFound)
	var user User
	require.NoError(t, DB.First(&user, 742).Error)
	assert.EqualValues(t, 1, user.AuthVersion)
	_, err = GetUserCache(742)
	assert.ErrorIs(t, err, ErrUserAuthCachePending)
	server.FastForward(time.Duration(userAuthFenceTTLSeconds()+1) * time.Second)
	_, err = GetUserCache(742)
	require.NoError(t, err)
	server.SetError("redis unavailable")
	_, err = ChangePasskeyForSession(742, session.SID, 1, 1, NativeFundingLocalOnly, &PasskeyCredential{UserID: 742, CredentialID: "fixture-key", PublicKey: "fixture-public"})
	require.Error(t, err)
	server.SetError("")
	_, err = GetPasskeyByUserID(742)
	assert.ErrorIs(t, err, ErrPasskeyNotFound)
}

func TestLegacyPasskeyChangesCannotReplaceCentralCredential(t *testing.T) {
	oldDB, oldRedis := DB, common.RedisEnabled
	db, err := gorm.Open(sqlite.Open(t.TempDir()+"/legacy-central.db"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&User{}, &UserSession{}, &PasskeyCredential{}, &NativeLoginCredential{}, &CentralLoginCredential{}, &ProductAuthLifecycle{}))
	DB, common.RedisEnabled = db, false
	t.Cleanup(func() { DB, common.RedisEnabled = oldDB, oldRedis })
	t.Setenv("UNIHUB_PRODUCT_RUNTIME_ENABLED", "false")
	user := User{Username: "legacy-central", AffCode: "legacy-central", Status: common.UserStatusEnabled, AuthVersion: 1}
	require.NoError(t, db.Create(&user).Error)
	session := newTestUserSession("legacy-central-session", user.Id, time.Now().Unix())
	require.NoError(t, db.Create(session).Error)
	require.NoError(t, db.Create(&PasskeyCredential{UserID: user.Id, CredentialID: "central-key", PublicKey: "central-public"}).Error)
	require.NoError(t, db.Create(&CentralLoginCredential{UserID: user.Id, Kind: "passkey", Digest: "central-proof", BindingID: "central", AuthVersion: 1}).Error)
	for _, credential := range []*PasskeyCredential{{UserID: user.Id, CredentialID: "legacy-key", PublicKey: "legacy-public"}, nil} {
		_, err = ChangePasskeyForSession(user.Id, session.SID, 1, 1, "", credential)
		require.Error(t, err)
		current, err := GetPasskeyByUserID(user.Id)
		require.NoError(t, err)
		assert.Equal(t, "central-key", current.CredentialID)
		var stored User
		require.NoError(t, db.First(&stored, user.Id).Error)
		assert.EqualValues(t, 1, stored.AuthVersion)
	}
}
