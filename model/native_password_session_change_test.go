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

func TestStalePasswordChangeAfterTwoFADoesNotCommit(t *testing.T) {
	oldDB, oldRedis := DB, common.RedisEnabled
	db, err := gorm.Open(sqlite.Open(t.TempDir()+"/password-race.db?_pragma=busy_timeout(5000)"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&User{}, &UserSession{}, &TwoFA{}, &TwoFABackupCode{}, &ProductAuthLifecycle{}))
	DB, common.RedisEnabled = db, false
	t.Cleanup(func() { DB, common.RedisEnabled = oldDB, oldRedis })
	t.Setenv("UNIHUB_PRODUCT_RUNTIME_ENABLED", "false")
	hash, err := common.Password2Hash("old-password")
	require.NoError(t, err)
	user := User{Username: "password-race", AffCode: "password-race", Password: hash, Status: common.UserStatusEnabled, AuthVersion: 1, FundingRealm: NativeFundingLocalOnly, FundingRealmEvidence: "reviewed", NativePassword: true}
	require.NoError(t, db.Create(&user).Error)
	session := newTestUserSession("password-race-session", user.Id, time.Now().Unix())
	session.AuthRealm = NativeFundingLocalOnly
	require.NoError(t, db.Create(session).Error)
	key, err := common.GenerateTOTPSecret("fixture")
	require.NoError(t, err)
	code, err := totp.GenerateCode(key.Secret(), time.Now())
	require.NoError(t, err)
	require.NoError(t, db.Create(&TwoFA{UserId: user.Id, Secret: key.Secret(), IsEnabled: true}).Error)
	advanced, err := ChangeTwoFAForSession(user.Id, session.SID, 1, 1, "backup", code, []string{"WXYZ-1234"})
	require.NoError(t, err)
	// This request had already authenticated and validated the old password.
	update := User{Id: user.Id, Password: "new-password"}
	_, err = ChangePasswordForSession(&update, "old-password", session.SID, 1, 1, NativeFundingLocalOnly)
	require.ErrorIs(t, err, ErrUserSessionInactive)
	var stored User
	require.NoError(t, db.First(&stored, user.Id).Error)
	assert.True(t, common.ValidatePasswordAndHash("old-password", stored.Password))
	assert.EqualValues(t, 2, stored.AuthVersion)
	var current UserSession
	require.NoError(t, db.First(&current, "sid = ?", session.SID).Error)
	assert.Equal(t, advanced.Version, current.Version)
	assert.Equal(t, advanced.UserAuthVersion, current.UserAuthVersion)
}

func TestPasswordSessionChangeRechecksPasswordAndPreservesOtherRealms(t *testing.T) {
	setupUserSessionTest(t)
	require.NoError(t, DB.AutoMigrate(&ProductAuthLifecycle{}))
	useUserCacheMiniRedis(t)
	t.Setenv("UNIHUB_PRODUCT_RUNTIME_ENABLED", "false")
	createUserSessionTestUser(t, 743, 1)
	hash, err := common.Password2Hash("old-password")
	require.NoError(t, err)
	require.NoError(t, DB.Model(&User{}).Where("id = ?", 743).Updates(map[string]interface{}{"password": hash, "funding_realm": NativeFundingLocalOnly, "funding_realm_evidence": "reviewed", "native_password": true}).Error)
	now := time.Now().Unix()
	current := newTestUserSession("password-current", 743, now)
	current.AuthRealm = NativeFundingLocalOnly
	other := newTestUserSession("password-other", 743, now)
	other.AuthRealm = NativeFundingLocalOnly
	central := newTestUserSession("password-central", 743, now)
	central.AuthRealm = CentralAuthRealm
	for _, item := range []*UserSession{current, other, central} {
		require.NoError(t, DB.Create(item).Error)
		_, err = GetUserSessionCached(item.SID)
		require.NoError(t, err)
	}
	_, err = GetUserCache(743)
	require.NoError(t, err)
	change := &User{Id: 743, Password: "new-password"}
	_, err = ChangePasswordForSession(change, "wrong-password", current.SID, 1, 1, NativeFundingLocalOnly)
	assert.ErrorIs(t, err, ErrPasswordSessionMismatch)
	var unchanged User
	require.NoError(t, DB.First(&unchanged, 743).Error)
	assert.True(t, common.ValidatePasswordAndHash("old-password", unchanged.Password))
	assert.EqualValues(t, 1, unchanged.AuthVersion)
	advanced, err := ChangePasswordForSession(change, "old-password", current.SID, 1, 1, NativeFundingLocalOnly)
	require.NoError(t, err)
	assert.EqualValues(t, 2, advanced.Version)
	assert.EqualValues(t, 2, advanced.UserAuthVersion)
	require.NoError(t, DB.First(&unchanged, 743).Error)
	assert.True(t, common.ValidatePasswordAndHash("new-password", unchanged.Password))
	cached, err := GetUserSessionCached(current.SID)
	require.NoError(t, err)
	assert.EqualValues(t, 2, cached.UserAuthVersion)
	_, err = GetUserSessionCached(other.SID)
	assert.ErrorIs(t, err, ErrUserSessionInactive)
	centralCached, err := GetUserSessionCached(central.SID)
	require.NoError(t, err)
	assert.Equal(t, UserSessionStatusActive, centralCached.Status)
}

func TestPasswordChangeRollsBackOnSessionFailureAndKeepsFence(t *testing.T) {
	setupUserSessionTest(t)
	require.NoError(t, DB.AutoMigrate(&ProductAuthLifecycle{}))
	server := useUserCacheMiniRedis(t)
	t.Setenv("UNIHUB_PRODUCT_RUNTIME_ENABLED", "false")
	createUserSessionTestUser(t, 744, 1)
	hash, err := common.Password2Hash("old-password")
	require.NoError(t, err)
	require.NoError(t, DB.Model(&User{}).Where("id = ?", 744).Updates(map[string]interface{}{"password": hash, "funding_realm": NativeFundingLocalOnly, "funding_realm_evidence": "reviewed", "native_password": true}).Error)
	session := newTestUserSession("password-fence", 744, time.Now().Unix())
	session.AuthRealm = NativeFundingLocalOnly
	require.NoError(t, DB.Create(session).Error)
	_, err = GetUserCache(744)
	require.NoError(t, err)
	require.NoError(t, DB.Callback().Update().Before("gorm:update").Register("test:password_session_failure", func(tx *gorm.DB) {
		if tx.Statement.Table == "user_sessions" {
			tx.AddError(errors.New("session unavailable"))
		}
	}))
	_, err = ChangePasswordForSession(&User{Id: 744, Password: "new-password"}, "old-password", session.SID, 1, 1, NativeFundingLocalOnly)
	require.Error(t, err)
	require.NoError(t, DB.Callback().Update().Remove("test:password_session_failure"))
	var user User
	require.NoError(t, DB.First(&user, 744).Error)
	assert.EqualValues(t, 1, user.AuthVersion)
	assert.True(t, common.ValidatePasswordAndHash("old-password", user.Password))
	_, err = GetUserCache(744)
	assert.ErrorIs(t, err, ErrUserAuthCachePending)
	server.FastForward(time.Duration(userAuthFenceTTLSeconds()+1) * time.Second)
	_, err = GetUserCache(744)
	require.NoError(t, err)
	server.SetError("redis unavailable")
	_, err = ChangePasswordForSession(&User{Id: 744, Password: "new-password"}, "old-password", session.SID, 1, 1, NativeFundingLocalOnly)
	require.Error(t, err)
	server.SetError("")
	require.NoError(t, DB.First(&user, 744).Error)
	assert.EqualValues(t, 1, user.AuthVersion)
	assert.True(t, common.ValidatePasswordAndHash("old-password", user.Password))
}
