package model

import (
	"context"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/go-redis/redis/v8"
	"github.com/pquerna/otp/totp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type beforeSessionCachePublish struct {
	sid    string
	armed  *bool
	fired  bool
	mutate func()
}

func (h *beforeSessionCachePublish) BeforeProcess(ctx context.Context, cmd redis.Cmder) (context.Context, error) {
	args := cmd.Args()
	if !h.fired && *h.armed && cmd.Name() == "eval" && len(args) > 6 && args[3] == userSessionCacheKey(h.sid) && args[6] == int64(1) {
		h.fired = true
		h.mutate()
	}
	return ctx, nil
}
func (*beforeSessionCachePublish) AfterProcess(context.Context, redis.Cmder) error { return nil }
func (*beforeSessionCachePublish) BeforeProcessPipeline(ctx context.Context, _ []redis.Cmder) (context.Context, error) {
	return ctx, nil
}
func (*beforeSessionCachePublish) AfterProcessPipeline(context.Context, []redis.Cmder) error {
	return nil
}

func TestRefreshRotationPreservesRetainedSessionAcrossSecurityChange(t *testing.T) {
	for _, operation := range []string{"password", "twofa", "passkey", "revoke"} {
		for _, phase := range []string{"before_update", "before_cache_publish"} {
			t.Run(operation+"/"+phase, func(t *testing.T) {
				setupUserSessionTest(t)
				require.NoError(t, DB.AutoMigrate(&ProductAuthLifecycle{}, &TwoFA{}, &TwoFABackupCode{}, &PasskeyCredential{}, &NativeLoginCredential{}, &CentralLoginCredential{}))
				useUserCacheMiniRedis(t)
				t.Setenv("UNIHUB_PRODUCT_RUNTIME_ENABLED", "false")
				user := User{Username: "refresh-security-" + operation + "-" + phase, AffCode: operation + "-" + phase, Status: common.UserStatusEnabled, AuthVersion: 1, FundingRealm: NativeFundingLocalOnly, FundingRealmEvidence: "reviewed", NativePassword: true}
				hash, err := common.Password2Hash("old-password")
				require.NoError(t, err)
				user.Password = hash
				require.NoError(t, DB.Create(&user).Error)
				now := time.Now().Unix()
				session := newTestUserSession("refresh-security-"+operation+"-"+phase, user.Id, now)
				session.AuthRealm = NativeFundingLocalOnly
				require.NoError(t, CreateUserSession(session))
				var twoFACode string
				if operation == "twofa" {
					key, err := common.GenerateTOTPSecret("refresh-security")
					require.NoError(t, err)
					twoFACode, err = totp.GenerateCode(key.Secret(), time.Now())
					require.NoError(t, err)
					require.NoError(t, DB.Create(&TwoFA{UserId: user.Id, Secret: key.Secret(), IsEnabled: true}).Error)
				}
				var mutationErr error
				var advanced *UserSession
				mutate := func() {
					switch operation {
					case "password":
						advanced, mutationErr = ChangePasswordForSession(&User{Id: user.Id, Password: "new-password"}, "old-password", session.SID, 1, 1, NativeFundingLocalOnly)
					case "twofa":
						advanced, mutationErr = ChangeTwoFAForSession(user.Id, session.SID, 1, 1, "backup", twoFACode, []string{"WXYZ-1234"})
					case "passkey":
						advanced, mutationErr = ChangePasskeyForSession(user.Id, session.SID, 1, 1, NativeFundingLocalOnly, &PasskeyCredential{UserID: user.Id, CredentialID: "synthetic-key-" + phase, PublicKey: "synthetic-public-key"})
					case "revoke":
						_, mutationErr = RevokeUserSession(user.Id, session.SID, "explicit_revoke", NativeFundingLocalOnly)
					}
				}
				armed, fired := false, false
				callback := "test:refresh_security_interleave"
				require.NoError(t, DB.Callback().Update().Before("gorm:begin_transaction").Register(callback, func(tx *gorm.DB) {
					changes, ok := tx.Statement.Dest.(map[string]interface{})
					if !fired && tx.Statement.Table == "user_sessions" && ok && changes["refresh_hash"] != nil {
						fired = true
						if phase == "before_update" {
							mutate()
						} else {
							armed = true
						}
					}
				}))
				t.Cleanup(func() { _ = DB.Callback().Update().Remove(callback) })
				hook := &beforeSessionCachePublish{sid: session.SID, armed: &armed, mutate: mutate}
				common.RDB.AddHook(hook)
				rotated, rotateErr := RotateUserSessionRefresh(user.Id, session.SID, session.RefreshHash, "returned-next-hash", now, 5*time.Second)
				require.True(t, fired)
				if phase == "before_cache_publish" {
					require.True(t, hook.fired, "the security change must occur after the SQL rotation and immediately before its old cache snapshot publishes")
				}
				require.NoError(t, mutationErr)
				stored, err := GetUserSessionBySID(session.SID)
				require.NoError(t, err)
				if operation == "revoke" {
					require.ErrorIs(t, rotateErr, ErrUserSessionInactive)
					require.Nil(t, rotated)
					assert.Equal(t, UserSessionStatusRevoked, stored.Status)
					_, err = GetUserSessionCached(session.SID)
					assert.ErrorIs(t, err, ErrUserSessionInactive)
					return
				}
				require.NoError(t, rotateErr, "a security change retains this exact SID and its browser refresh secret")
				require.NotNil(t, rotated)
				require.EqualValues(t, 2, advanced.Version)
				assert.Equal(t, advanced.Version, rotated.Version)
				assert.Equal(t, advanced.UserAuthVersion, rotated.UserAuthVersion)
				assert.Equal(t, "returned-next-hash", stored.RefreshHash)
				assert.Equal(t, UserSessionStatusActive, stored.Status)
				cached, err := GetUserSessionCached(session.SID)
				require.NoError(t, err)
				assert.Equal(t, advanced.Version, cached.Version)
				// The returned winner must remain usable beyond the old token's
				// grace period, rather than becoming a false family-reuse revoke.
				next, err := RotateUserSessionRefresh(user.Id, session.SID, "returned-next-hash", "following-hash", now+6, 5*time.Second)
				require.NoError(t, err)
				assert.Equal(t, advanced.Version, next.Version)
			})
		}
	}
}
