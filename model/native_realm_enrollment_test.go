package model

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"github.com/QuantumNous/new-api/internal/platformaccount"
	"gorm.io/gorm"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func nativeEnrollmentDigest(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func TestNativeRealmEnrollmentIsAtomicAndPreservesKeys(t *testing.T) {
	_, db, _ := productFundingFixture(t)
	require.NoError(t, db.AutoMigrate(&Token{}, &ProductAuthLifecycle{}))
	require.NoError(t, db.Model(&User{}).Where("id = ?", 42).Updates(map[string]any{"password": "synthetic-password-hash", "role": 100, "status": 1, "auth_version": 1}).Error)
	token := Token{Id: 71, UserId: 42, Key: "synthetic-key", Status: 1, ExpiredTime: -1, RemainQuota: 50, ModelLimitsEnabled: true, ModelLimits: "model-a"}
	require.NoError(t, db.Create(&token).Error)
	request := NativeRealmManifest{Version: 1, Users: []NativeRealmEnrollment{{UserID: 42, Role: 100, Status: 1, AuthVersion: 1, PasswordSHA256: nativeEnrollmentDigest("synthetic-password-hash"), Keys: []NativeKeyEnrollment{{ID: 71, SecretSHA256: nativeEnrollmentDigest("synthetic-key")}}}}}
	raw, err := common.Marshal(request)
	require.NoError(t, err)
	require.Error(t, EnrollNativeRealm(context.Background(), db, raw, nativeEnrollmentDigest("wrong")))
	// A mismatching key must roll back even the prior user policy write.
	request.Users[0].Keys[0].SecretSHA256 = nativeEnrollmentDigest("wrong")
	bad, err := common.Marshal(request)
	require.NoError(t, err)
	require.Error(t, EnrollNativeRealm(context.Background(), db, bad, nativeEnrollmentDigest(string(bad))))
	var user User
	require.NoError(t, db.First(&user, 42).Error)
	assert.Empty(t, user.FundingRealm)
	require.NoError(t, EnrollNativeRealm(context.Background(), db, raw, nativeEnrollmentDigest(string(raw))))
	require.NoError(t, db.First(&user, 42).Error)
	assert.True(t, user.NativePassword)
	assert.Equal(t, NativeFundingLocalOnly, user.FundingRealm)
	assert.Equal(t, 500, user.Quota)
	require.NoError(t, db.First(&token, 71).Error)
	assert.Equal(t, NativeFundingLocalOnly, token.AuthRealm)
	assert.Equal(t, 50, token.RemainQuota)
	assert.Equal(t, "model-a", token.ModelLimits)
	require.NoError(t, EnrollNativeRealm(context.Background(), db, raw, nativeEnrollmentDigest(string(raw))))
}

func TestNativeRealmProjectionDoesNotOverwriteNativeAuthority(t *testing.T) {
	_, db, _ := productFundingFixture(t)
	old := DB
	DB = db
	t.Cleanup(func() { DB = old })
	require.NoError(t, db.AutoMigrate(&UniHubIdentityMapping{}, &ProductAuthLifecycle{}))
	require.NoError(t, db.Model(&User{}).Where("id = ?", 42).Updates(map[string]any{"role": 100, "status": 1, "auth_version": 1, "funding_realm": NativeFundingLocalOnly, "funding_realm_evidence": "reviewed", "native_password": true}).Error)
	_, err := BindUniHubIdentity(platformaccount.Identity{Issuer: "https://hub.test", User: platformaccount.User{ID: "mapped"}}, 42)
	require.NoError(t, err)
	t.Setenv("UNIHUB_PRODUCT_RUNTIME_ENABLED", "true")
	t.Setenv("UNIHUB_ORIGIN", "http://127.0.0.1:1")
	t.Setenv("UNIHUB_PRODUCT_RUNTIME_TOKEN", strings.Repeat("s", 32))
	// Stale input does not grant a central reconciler permission to write locally.
	stale := User{Id: 42, IdentitySource: UniHubIdentitySource, UniHubUserID: "mapped", AuthVersion: 1, Role: 1, Status: common.UserStatusDisabled}
	require.NoError(t, ReconcileProductIdentity(context.Background(), &stale))
	assert.Equal(t, 100, stale.Role)
	assert.Equal(t, common.UserStatusEnabled, stale.Status)
	var user User
	require.NoError(t, db.First(&user, 42).Error)
	assert.Equal(t, 100, user.Role)
	assert.Equal(t, common.UserStatusEnabled, user.Status)
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error { return queueProductAuthVersion(tx, 42, 1, 2) }))
	var count int64
	require.NoError(t, db.Model(&ProductAuthLifecycle{}).Count(&count).Error)
	assert.Zero(t, count)
}

func TestNativeRealmKeysRemainPartitionedWithinOwner(t *testing.T) {
	_, db, _ := productFundingFixture(t)
	old := DB
	DB = db
	t.Cleanup(func() { DB = old })
	native := Token{Id: 82, UserId: 42, Key: "synthetic-native-secret", AuthRealm: NativeFundingLocalOnly, Status: 1, ExpiredTime: -1, RemainQuota: 20}
	require.NoError(t, db.Create(&native).Error)
	_, err := GetTokenByIds(native.Id, 42, "")
	require.Error(t, err)
	require.Error(t, DeleteTokenById(native.Id, 42, ""))
	require.NoError(t, db.First(&native, native.Id).Error)
	keys, err := GetTokenKeysByIds([]int{7, 82}, 42, "")
	require.NoError(t, err)
	require.Len(t, keys, 1)
	assert.Equal(t, 7, keys[0].Id)
	_, err = GetTokenByIds(7, 42, NativeFundingLocalOnly)
	require.Error(t, err)
	rows, err := GetAllUserTokens(42, 0, 100, NativeFundingLocalOnly)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	count, err := BatchDeleteTokens([]int{7, 82}, 42, "")
	require.NoError(t, err)
	assert.Equal(t, 1, count)
	require.NoError(t, db.First(&native, native.Id).Error)
	require.NoError(t, DeleteTokenById(native.Id, 42, NativeFundingLocalOnly))
}

func TestNativeRealmUnknownKeyCannotSpendLocalFunding(t *testing.T) {
	_, db, _ := productFundingFixture(t)
	old := DB
	DB = db
	t.Cleanup(func() { DB = old })
	require.NoError(t, db.Model(&User{}).Where("id = ?", 42).Updates(map[string]any{"funding_realm": NativeFundingLocalOnly, "funding_realm_evidence": "reviewed", "native_password": true}).Error)
	require.Error(t, ValidateLocalFundingCredential(context.Background(), 42, 7))
	require.NoError(t, db.Model(&Token{}).Where("id = ?", 7).Update("auth_realm", NativeFundingLocalOnly).Error)
	require.NoError(t, ValidateLocalFundingCredential(context.Background(), 42, 7))
	var token Token
	require.NoError(t, db.First(&token, 7).Error)
	token.UnlimitedQuota = true // stale cache before a server-side restriction
	require.NoError(t, db.Model(&Token{}).Where("id = ?", 7).Updates(map[string]any{"unlimited_quota": false, "remain_quota": 0}).Error)
	enrollNativeQuotaFixture(t, 7)
	ok, err := TryReserveTokenQuota(7, token.Key, 1, true)
	require.NoError(t, err)
	assert.False(t, ok)
	native, err := NativeRealmToken(context.Background(), &token)
	require.NoError(t, err)
	assert.True(t, native)
	assert.False(t, token.UnlimitedQuota)
	assert.Zero(t, token.RemainQuota)
	require.NoError(t, db.Model(&Token{}).Where("id = ?", 7).Update("status", common.TokenStatusExhausted).Error)
	_, err = NativeRealmToken(context.Background(), &token)
	require.Error(t, err)
	native, err = NativeRealmToken(context.Background(), &token, true)
	require.NoError(t, err)
	assert.True(t, native)
	var user User
	require.NoError(t, db.First(&user, 42).Error)
	assert.Equal(t, 500, user.Quota)
}

func TestNativeRealmReadOnlyPreservesHistoricalKeyPolicy(t *testing.T) {
	_, db, _ := productFundingFixture(t)
	old := DB
	DB = db
	t.Cleanup(func() { DB = old })
	require.NoError(t, db.Model(&User{}).Where("id = ?", 42).Updates(map[string]any{"funding_realm": NativeFundingLocalOnly, "funding_realm_evidence": "reviewed", "native_password": true}).Error)
	for _, state := range []int{common.TokenStatusEnabled, common.TokenStatusExhausted, common.TokenStatusExpired, common.TokenStatusDisabled} {
		require.NoError(t, db.Model(&Token{}).Where("id = ?", 7).Updates(map[string]any{"auth_realm": NativeFundingLocalOnly, "status": state, "expired_time": 1}).Error)
		var token Token
		require.NoError(t, db.First(&token, 7).Error)
		ok, err := NativeRealmTokenReadOnly(context.Background(), &token)
		if state == common.TokenStatusDisabled {
			require.Error(t, err)
			assert.False(t, ok)
		} else {
			require.NoError(t, err)
			assert.True(t, ok)
		}
		_, err = NativeRealmToken(context.Background(), &token, true)
		require.Error(t, err, "paid-job reads still require unexpired credentials")
	}
}

func TestNativeRealmRejectsPendingIdentityEvenWhenFlagsAreClear(t *testing.T) {
	_, db, _ := productFundingFixture(t)
	old := DB
	DB = db
	t.Cleanup(func() { DB = old })
	require.NoError(t, db.Model(&User{}).Where("id = ?", 42).Updates(map[string]any{"funding_realm": NativeFundingLocalOnly, "funding_realm_evidence": "reviewed", "native_password": true, "identity_source": ProductIdentityPending, "product_registration_pending": false, "product_jit_pending": false}).Error)
	_, err := NativeRealmUser(context.Background(), 42)
	require.Error(t, err)
}
