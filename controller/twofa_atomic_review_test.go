package controller

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/alicebob/miniredis/v2"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/go-redis/redis/v8"
	"github.com/pquerna/otp/totp"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type failRevokedSessionCache struct {
	key   string
	calls *int
}

func (h failRevokedSessionCache) BeforeProcess(ctx context.Context, cmd redis.Cmder) (context.Context, error) {
	args := cmd.Args()
	if cmd.Name() == "eval" && len(args) > 8 && fmt.Sprint(args[4]) == h.key && fmt.Sprint(args[8]) == model.UserSessionStatusRevoked {
		*h.calls++
		return ctx, errors.New("revocation cache write unavailable")
	}
	return ctx, nil
}
func (failRevokedSessionCache) AfterProcess(context.Context, redis.Cmder) error { return nil }
func (failRevokedSessionCache) BeforeProcessPipeline(ctx context.Context, _ []redis.Cmder) (context.Context, error) {
	return ctx, nil
}
func (failRevokedSessionCache) AfterProcessPipeline(context.Context, []redis.Cmder) error { return nil }

func TestTwoFARevokedCacheFailureStillDeniesOldSession(t *testing.T) {
	oldDB, oldRedis, oldRDB, oldSecret := model.DB, common.RedisEnabled, common.RDB, common.SessionSecret
	db, err := gorm.Open(sqlite.Open(t.TempDir()+"/twofa-cache.db"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.UserSession{}, &model.TwoFA{}, &model.TwoFABackupCode{}, &model.UniHubIdentityMapping{}))
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	model.DB, common.RedisEnabled, common.RDB, common.SessionSecret = db, true, client, "twofa-cache-test-secret"
	t.Cleanup(func() {
		_ = client.Close()
		model.DB, common.RedisEnabled, common.RDB, common.SessionSecret = oldDB, oldRedis, oldRDB, oldSecret
	})
	t.Setenv("UNIHUB_PRODUCT_RUNTIME_ENABLED", "false")
	user := model.User{Username: "twofa-cache", AffCode: "twofa-cache", Role: common.RoleCommonUser, Status: common.UserStatusEnabled, AuthVersion: 1}
	require.NoError(t, db.Create(&user).Error)
	key, err := common.GenerateTOTPSecret("twofa-cache")
	require.NoError(t, err)
	code, err := totp.GenerateCode(key.Secret(), time.Now())
	require.NoError(t, err)
	require.NoError(t, db.Create(&model.TwoFA{UserId: user.Id, Secret: key.Secret()}).Error)
	for _, sid := range []string{"current-cache", "other-cache"} {
		session := model.UserSession{SID: sid, UserID: user.Id, AuthRealm: model.NativeFundingLocalOnly, Version: 1, UserAuthVersion: 1, Status: model.UserSessionStatusActive, RefreshHash: "fixture", ExpiresAt: time.Now().Unix() + 600}
		require.NoError(t, db.Create(&session).Error)
		_, err = model.GetUserSessionCached(sid)
		require.NoError(t, err)
	}
	_, err = model.GetUserCache(user.Id)
	require.NoError(t, err)
	failedWrites := 0
	client.AddHook(failRevokedSessionCache{key: "other-cache", calls: &failedWrites})
	identity := service.AuthIdentity{UserID: user.Id, SessionID: "current-cache", SessionVersion: 1, UserAuthVersion: 1}
	bundle, err := service.ChangeTwoFAForCurrentSession(identity, "enable", code, nil)
	require.NoError(t, err)
	require.Equal(t, 1, failedWrites)
	newIdentity, err := service.ParseAccessToken(bundle.AccessToken)
	require.NoError(t, err)
	_, _, err = service.ValidateLoginSession(newIdentity)
	require.NoError(t, err)
	_, _, err = service.ValidateLoginSession(service.AuthIdentity{UserID: user.Id, SessionID: "other-cache", SessionVersion: 1, UserAuthVersion: 1})
	require.ErrorIs(t, err, service.ErrLoginSessionRevoked)
}

func TestTwoFASelfChangeRollsBackWhenCurrentSessionCannotAdvance(t *testing.T) {
	for _, operation := range []string{"enable", "disable", "disable_backup", "backup"} {
		t.Run(operation, func(t *testing.T) {
			oldDB, oldRedis := model.DB, common.RedisEnabled
			db, err := gorm.Open(sqlite.Open(t.TempDir()+"/twofa.db"), &gorm.Config{})
			require.NoError(t, err)
			require.NoError(t, db.AutoMigrate(&model.User{}, &model.UserSession{}, &model.TwoFA{}, &model.TwoFABackupCode{}, &model.UniHubIdentityMapping{}))
			model.DB, common.RedisEnabled = db, false
			t.Cleanup(func() { model.DB, common.RedisEnabled = oldDB, oldRedis })
			t.Setenv("UNIHUB_PRODUCT_RUNTIME_ENABLED", "false")
			user := model.User{Username: "twofa-test", AffCode: "twofa-test", Status: common.UserStatusEnabled, AuthVersion: 1}
			require.NoError(t, db.Create(&user).Error)
			session := model.UserSession{SID: "sid", UserID: user.Id, AuthRealm: model.NativeFundingLocalOnly, Version: 1, UserAuthVersion: 1, Status: model.UserSessionStatusActive, RefreshHash: "fixture", ExpiresAt: time.Now().Unix() + 600}
			require.NoError(t, db.Create(&session).Error)
			key, err := common.GenerateTOTPSecret("twofa-test")
			require.NoError(t, err)
			factor := model.TwoFA{UserId: user.Id, Secret: key.Secret(), IsEnabled: operation != "enable"}
			require.NoError(t, db.Create(&factor).Error)
			hash, err := common.HashBackupCode("ABCD1234")
			require.NoError(t, err)
			require.NoError(t, db.Create(&model.TwoFABackupCode{UserId: user.Id, CodeHash: hash}).Error)
			code, err := totp.GenerateCode(key.Secret(), time.Now())
			require.NoError(t, err)
			if operation == "disable_backup" {
				code = "ABCD-1234"
			}
			require.NoError(t, db.Callback().Update().Before("gorm:update").Register("test:session_update_fails", func(tx *gorm.DB) {
				if tx.Statement.Table == "user_sessions" {
					tx.AddError(errors.New("session update failed"))
				}
			}))
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest(http.MethodPost, "/api/user/2fa/"+operation, strings.NewReader(`{"code":"`+code+`"}`))
			c.Request.Header.Set("Content-Type", "application/json")
			c.Set("id", user.Id)
			c.Set("session_id", "sid")
			c.Set("session_version", int64(1))
			c.Set("auth_version", int64(1))
			switch operation {
			case "enable":
				Enable2FA(c)
			case "disable", "disable_backup":
				Disable2FA(c)
			case "backup":
				RegenerateBackupCodes(c)
			}
			require.NoError(t, db.Callback().Update().Remove("test:session_update_fails"))
			var after model.User
			require.NoError(t, db.First(&after, user.Id).Error)
			require.Equal(t, int64(1), after.AuthVersion, "a failed session advance must roll back the factor and auth version")
			var saved model.TwoFA
			require.NoError(t, db.Where("user_id = ?", user.Id).First(&saved).Error)
			require.Equal(t, factor.IsEnabled, saved.IsEnabled)
			var codes []model.TwoFABackupCode
			require.NoError(t, db.Where("user_id = ?", user.Id).Find(&codes).Error)
			require.Len(t, codes, 1)
			require.Equal(t, hash, codes[0].CodeHash)
			_, _, err = service.ValidateLoginSession(service.AuthIdentity{UserID: user.Id, SessionID: "sid", SessionVersion: 1, UserAuthVersion: 1})
			require.NoError(t, err)
		})
	}
}

func TestTwoFASetupFailurePreservesPreviousPendingSecretAndCodes(t *testing.T) {
	oldDB, oldRedis := model.DB, common.RedisEnabled
	db, err := gorm.Open(sqlite.Open(t.TempDir()+"/twofa-setup.db"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.TwoFA{}, &model.TwoFABackupCode{}))
	model.DB, common.RedisEnabled = db, false
	t.Cleanup(func() { model.DB, common.RedisEnabled = oldDB, oldRedis })
	user := model.User{Username: "setup", AffCode: "setup", Status: common.UserStatusEnabled, AuthVersion: 1}
	require.NoError(t, db.Create(&user).Error)
	require.NoError(t, db.Create(&model.TwoFA{UserId: user.Id, Secret: "original-secret"}).Error)
	hash, err := common.HashBackupCode("ABCD-1234")
	require.NoError(t, err)
	require.NoError(t, db.Create(&model.TwoFABackupCode{UserId: user.Id, CodeHash: hash}).Error)
	require.NoError(t, db.Callback().Create().Before("gorm:create").Register("test:backup_create_fails", func(tx *gorm.DB) {
		if tx.Statement.Table == "two_fa_backup_codes" {
			tx.AddError(errors.New("backup insert failed"))
		}
	}))
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/user/2fa/setup", nil)
	c.Set("id", user.Id)
	Setup2FA(c)
	require.NoError(t, db.Callback().Create().Remove("test:backup_create_fails"))
	var factor model.TwoFA
	require.NoError(t, db.Where("user_id = ?", user.Id).First(&factor).Error)
	require.Equal(t, "original-secret", factor.Secret)
	var codes []model.TwoFABackupCode
	require.NoError(t, db.Where("user_id = ?", user.Id).Find(&codes).Error)
	require.Len(t, codes, 1)
	require.Equal(t, hash, codes[0].CodeHash)
}
