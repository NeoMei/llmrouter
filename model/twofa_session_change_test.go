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

func TestTwoFAConcurrentChangesCommitOnlyOneSession(t *testing.T) {
	oldDB, oldRedis := DB, common.RedisEnabled
	db, err := gorm.Open(sqlite.Open(t.TempDir()+"/race.db?_pragma=busy_timeout(5000)"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&User{}, &UserSession{}, &TwoFA{}, &TwoFABackupCode{}, &ProductAuthLifecycle{}))
	DB, common.RedisEnabled = db, false
	t.Cleanup(func() { DB, common.RedisEnabled = oldDB, oldRedis })
	t.Setenv("UNIHUB_PRODUCT_RUNTIME_ENABLED", "false")
	user := User{Username: "twofa-race", AffCode: "twofa-race", Status: common.UserStatusEnabled, AuthVersion: 1}
	require.NoError(t, db.Create(&user).Error)
	key, err := common.GenerateTOTPSecret("twofa-race")
	require.NoError(t, err)
	code, err := totp.GenerateCode(key.Secret(), time.Now())
	require.NoError(t, err)
	require.NoError(t, db.Create(&TwoFA{UserId: user.Id, Secret: key.Secret(), IsEnabled: true}).Error)
	for _, sid := range []string{"session-a", "session-b"} {
		session := newTestUserSession(sid, user.Id, time.Now().Unix())
		session.AuthRealm = NativeFundingLocalOnly
		require.NoError(t, db.Create(session).Error)
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, sid := range []string{"session-a", "session-b"} {
		go func(sid string) {
			<-start
			_, err := ChangeTwoFAForSession(user.Id, sid, 1, 1, "backup", code, []string{"WXYZ-1234"})
			results <- err
		}(sid)
	}
	close(start)
	errA, errB := <-results, <-results
	require.True(t, (errA == nil) != (errB == nil), "exactly one change must commit: %v, %v", errA, errB)
	var stored User
	require.NoError(t, db.First(&stored, user.Id).Error)
	assert.EqualValues(t, 2, stored.AuthVersion)
	var codes []TwoFABackupCode
	require.NoError(t, db.Where("user_id = ?", user.Id).Find(&codes).Error)
	require.Len(t, codes, 1)
	assert.True(t, common.ValidatePasswordAndHash("WXYZ-1234", codes[0].CodeHash))
}

func TestPendingTwoFAReplacementRejectsStaleCodeWriter(t *testing.T) {
	setupUserSessionTest(t)
	require.NoError(t, DB.AutoMigrate(&TwoFA{}, &TwoFABackupCode{}))
	createUserSessionTestUser(t, 74, 1)
	require.NoError(t, ReplacePendingTwoFASetup(74, "secret-a", []string{"AAAA-1111"}))
	var first TwoFA
	require.NoError(t, DB.Where("user_id = ?", 74).First(&first).Error)
	require.NoError(t, ReplacePendingTwoFASetup(74, "secret-b", []string{"BBBB-2222"}))
	require.Error(t, CreatePendingTwoFASetupBackupCodes(74, first.Id, first.Secret, []string{"AAAA-1111"}))
	var current TwoFA
	require.NoError(t, DB.Where("user_id = ?", 74).First(&current).Error)
	assert.Equal(t, "secret-b", current.Secret)
	var codes []TwoFABackupCode
	require.NoError(t, DB.Where("user_id = ?", 74).Find(&codes).Error)
	require.Len(t, codes, 1)
	assert.True(t, common.ValidatePasswordAndHash("BBBB-2222", codes[0].CodeHash))
	assert.False(t, common.ValidatePasswordAndHash("AAAA-1111", codes[0].CodeHash))
}

func TestConcurrentPendingTwoFASetupsKeepSecretAndCodesPaired(t *testing.T) {
	oldDB, oldRedis := DB, common.RedisEnabled
	db, err := gorm.Open(sqlite.Open(t.TempDir()+"/pending-race.db?_pragma=busy_timeout(5000)"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&User{}, &TwoFA{}, &TwoFABackupCode{}))
	DB, common.RedisEnabled = db, false
	t.Cleanup(func() { DB, common.RedisEnabled = oldDB, oldRedis })
	user := User{Username: "pending-race", AffCode: "pending-race", Status: common.UserStatusEnabled, AuthVersion: 1}
	require.NoError(t, db.Create(&user).Error)
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, item := range []struct{ secret, code string }{{"secret-a", "AAAA-1111"}, {"secret-b", "BBBB-2222"}} {
		go func(secret, code string) {
			<-start
			results <- ReplacePendingTwoFASetup(user.Id, secret, []string{code})
		}(item.secret, item.code)
	}
	close(start)
	require.NoError(t, <-results)
	require.NoError(t, <-results)
	var current TwoFA
	require.NoError(t, db.Where("user_id = ?", user.Id).First(&current).Error)
	var codes []TwoFABackupCode
	require.NoError(t, db.Where("user_id = ?", user.Id).Find(&codes).Error)
	require.Len(t, codes, 1)
	if current.Secret == "secret-a" {
		assert.True(t, common.ValidatePasswordAndHash("AAAA-1111", codes[0].CodeHash))
	} else {
		assert.Equal(t, "secret-b", current.Secret)
		assert.True(t, common.ValidatePasswordAndHash("BBBB-2222", codes[0].CodeHash))
	}
}

func TestTwoFASessionChangePublishesAuthAndSessionState(t *testing.T) {
	setupUserSessionTest(t)
	require.NoError(t, DB.AutoMigrate(&TwoFA{}, &TwoFABackupCode{}, &ProductAuthLifecycle{}))
	useUserCacheMiniRedis(t)
	t.Setenv("UNIHUB_PRODUCT_RUNTIME_ENABLED", "false")
	createUserSessionTestUser(t, 71, 1)
	key, err := common.GenerateTOTPSecret("twofa-atomic")
	require.NoError(t, err)
	code, err := totp.GenerateCode(key.Secret(), time.Now())
	require.NoError(t, err)
	require.NoError(t, DB.Create(&TwoFA{UserId: 71, Secret: key.Secret()}).Error)
	now := time.Now().Unix()
	current := newTestUserSession("twofa-current", 71, now)
	current.AuthRealm = NativeFundingLocalOnly
	other := newTestUserSession("twofa-other", 71, now)
	other.AuthRealm = NativeFundingLocalOnly
	central := newTestUserSession("twofa-central", 71, now)
	central.AuthRealm = CentralAuthRealm
	for _, session := range []*UserSession{current, other, central} {
		require.NoError(t, DB.Create(session).Error)
		_, err = GetUserSessionCached(session.SID)
		require.NoError(t, err)
	}
	_, err = GetUserCache(71)
	require.NoError(t, err)
	advanced, err := ChangeTwoFAForSession(71, current.SID, 1, 1, "enable", code, nil)
	require.NoError(t, err)
	assert.EqualValues(t, 2, advanced.Version)
	assert.EqualValues(t, 2, advanced.UserAuthVersion)
	cachedUser, err := GetUserCache(71)
	require.NoError(t, err)
	assert.EqualValues(t, 2, cachedUser.AuthVersion)
	cachedSession, err := GetUserSessionCached(current.SID)
	require.NoError(t, err)
	assert.EqualValues(t, 2, cachedSession.Version)
	_, err = GetUserSessionCached(other.SID)
	assert.ErrorIs(t, err, ErrUserSessionInactive)
	centralCached, err := GetUserSessionCached(central.SID)
	require.NoError(t, err)
	assert.Equal(t, UserSessionStatusActive, centralCached.Status)
	_, err = ChangeTwoFAForSession(71, current.SID, 1, 1, "disable", code, nil)
	assert.ErrorIs(t, err, ErrUserSessionInactive)
	var factor TwoFA
	require.NoError(t, DB.Where("user_id = ?", 71).First(&factor).Error)
	assert.True(t, factor.IsEnabled)
	_, err = ChangeTwoFAForSession(71, current.SID, 2, 2, "backup", "ABCD-1234", []string{"WXYZ-1234"})
	assert.ErrorIs(t, err, ErrTwoFAInvalidCode, "regeneration requires TOTP")
	advanced, err = ChangeTwoFAForSession(71, current.SID, 2, 2, "backup", code, []string{"WXYZ-1234"})
	require.NoError(t, err)
	assert.EqualValues(t, 3, advanced.UserAuthVersion)
	_, err = ChangeTwoFAForSession(71, current.SID, 3, 3, "disable", "WXYZ-1234", nil)
	require.NoError(t, err)
	var count int64
	require.NoError(t, DB.Model(&TwoFA{}).Where("user_id = ?", 71).Count(&count).Error)
	assert.Zero(t, count)
	require.NoError(t, DB.Model(&TwoFABackupCode{}).Where("user_id = ?", 71).Count(&count).Error)
	assert.Zero(t, count)
}

func TestTwoFASessionChangeRejectsRepeatedInvalidVerification(t *testing.T) {
	setupUserSessionTest(t)
	require.NoError(t, DB.AutoMigrate(&TwoFA{}, &TwoFABackupCode{}, &ProductAuthLifecycle{}))
	t.Setenv("UNIHUB_PRODUCT_RUNTIME_ENABLED", "false")
	createUserSessionTestUser(t, 73, 1)
	key, err := common.GenerateTOTPSecret("twofa-lock")
	require.NoError(t, err)
	code, err := totp.GenerateCode(key.Secret(), time.Now())
	require.NoError(t, err)
	require.NoError(t, DB.Create(&TwoFA{UserId: 73, Secret: key.Secret(), IsEnabled: true}).Error)
	session := newTestUserSession("twofa-lock-current", 73, time.Now().Unix())
	session.AuthRealm = NativeFundingLocalOnly
	require.NoError(t, DB.Create(session).Error)
	for range common.MaxFailAttempts {
		_, err = ChangeTwoFAForSession(73, session.SID, 1, 1, "disable", "ZZZZ-ZZZZ", nil)
		assert.ErrorIs(t, err, ErrTwoFAInvalidCode)
	}
	_, err = ChangeTwoFAForSession(73, session.SID, 1, 1, "disable", code, nil)
	require.Error(t, err, "a locked factor must reject even a correct code")
	var factor TwoFA
	require.NoError(t, DB.Where("user_id = ?", 73).First(&factor).Error)
	assert.True(t, factor.IsEnabled)
	assert.NotNil(t, factor.LockedUntil)
}

func TestTwoFAAtomicRollbackKeepsRedisFenceAndRecovers(t *testing.T) {
	setupUserSessionTest(t)
	require.NoError(t, DB.AutoMigrate(&TwoFA{}, &TwoFABackupCode{}, &ProductAuthLifecycle{}))
	server := useUserCacheMiniRedis(t)
	t.Setenv("UNIHUB_PRODUCT_RUNTIME_ENABLED", "false")
	createUserSessionTestUser(t, 72, 1)
	key, err := common.GenerateTOTPSecret("twofa-fence")
	require.NoError(t, err)
	code, err := totp.GenerateCode(key.Secret(), time.Now())
	require.NoError(t, err)
	require.NoError(t, DB.Create(&TwoFA{UserId: 72, Secret: key.Secret()}).Error)
	session := newTestUserSession("twofa-fence-current", 72, time.Now().Unix())
	session.AuthRealm = NativeFundingLocalOnly
	require.NoError(t, DB.Create(session).Error)
	_, err = GetUserCache(72)
	require.NoError(t, err)
	require.NoError(t, DB.Callback().Update().Before("gorm:update").Register("test:twofa_session_failure", func(tx *gorm.DB) {
		if tx.Statement.Table == "user_sessions" {
			tx.AddError(errors.New("session unavailable"))
		}
	}))
	_, err = ChangeTwoFAForSession(72, session.SID, 1, 1, "enable", code, nil)
	require.Error(t, err)
	require.NoError(t, DB.Callback().Update().Remove("test:twofa_session_failure"))
	_, err = GetUserCache(72)
	assert.ErrorIs(t, err, ErrUserAuthCachePending)
	var factor TwoFA
	require.NoError(t, DB.Where("user_id = ?", 72).First(&factor).Error)
	assert.False(t, factor.IsEnabled)
	server.FastForward(time.Duration(userAuthFenceTTLSeconds()+1) * time.Second)
	user, err := GetUserCache(72)
	require.NoError(t, err)
	assert.EqualValues(t, 1, user.AuthVersion)
	server.SetError("redis unavailable")
	_, err = ChangeTwoFAForSession(72, session.SID, 1, 1, "enable", code, nil)
	require.Error(t, err, "the change must not commit without its Redis deny fence")
	server.SetError("")
	var after User
	require.NoError(t, DB.First(&after, 72).Error)
	assert.EqualValues(t, 1, after.AuthVersion)
	require.NoError(t, DB.Where("user_id = ?", 72).First(&factor).Error)
	assert.False(t, factor.IsEnabled)
}
