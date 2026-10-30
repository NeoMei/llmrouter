package model

import (
	"context"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNativeRealmAuthenticationKeepsUnprojectedRefundSpendable(t *testing.T) {
	truncateTables(t)
	resetBatchUpdateTestState(t)
	useUserCacheMiniRedis(t)
	t.Setenv("UNIHUB_PRODUCT_RUNTIME_ENABLED", "false")
	common.BatchUpdateEnabled = true
	owner := createReserveTestUser(t, 1000)
	require.NoError(t, DB.Model(&owner).Updates(map[string]any{"funding_realm": NativeFundingLocalOnly, "funding_realm_evidence": "reviewed", "native_password": true}).Error)
	token := createReserveTestToken(t, 100)
	require.NoError(t, DB.Model(&token).Updates(map[string]any{"user_id": owner.Id, "auth_realm": NativeFundingLocalOnly}).Error)
	enrollNativeQuotaFixture(t, token.Id)
	accepted, err := TryReserveTokenQuota(token.Id, token.Key, 80, false)
	require.NoError(t, err)
	require.True(t, accepted)
	require.NoError(t, FlushNativeTokenQuotaProjections())
	require.Equal(t, 20, getTokenFromDB(t, token.Id).RemainQuota)
	require.NoError(t, IncreaseTokenQuota(token.Id, token.Key, 80))
	require.Equal(t, 20, getTokenFromDB(t, token.Id).RemainQuota, "the token-table projection still precedes the completed refund")

	authenticated, err := GetTokenByKey(token.Key, false)
	require.NoError(t, err)
	require.Equal(t, 100, authenticated.RemainQuota)
	native, err := NativeRealmToken(context.Background(), authenticated)
	require.NoError(t, err)
	require.True(t, native)
	assert.Equal(t, 100, authenticated.RemainQuota, "authentication must retain the durable refund, not clamp it to a stale projection")
	assert.Zero(t, authenticated.UsedQuota)
	accepted, err = TryReserveTokenQuota(authenticated.Id, authenticated.Key, 60, authenticated.UnlimitedQuota)
	require.NoError(t, err)
	require.True(t, accepted)
	accepted, err = TryReserveTokenQuota(authenticated.Id, authenticated.Key, 60, authenticated.UnlimitedQuota)
	require.NoError(t, err)
	assert.False(t, accepted, "the durable balance still bounds subsequent admissions")
}

func TestNativeRealmUnenrolledHistoryCannotGrantAdmission(t *testing.T) {
	_, db, _ := productFundingFixture(t)
	oldDB := DB
	DB = db
	t.Cleanup(func() { DB = oldDB })
	t.Setenv("UNIHUB_PRODUCT_RUNTIME_ENABLED", "false")
	require.NoError(t, RegisterNativeTokenQuotaModels(db))
	require.NoError(t, db.Model(&User{}).Where("id = ?", 42).Updates(map[string]any{"funding_realm": NativeFundingLocalOnly, "funding_realm_evidence": "reviewed", "native_password": true}).Error)
	require.NoError(t, db.Model(&Token{}).Where("id = ?", 7).Update("auth_realm", NativeFundingLocalOnly).Error)
	var authenticated Token
	require.NoError(t, db.First(&authenticated, 7).Error)
	authenticated.RemainQuota = 1000 // stale credential-cache snapshot
	native, err := NativeRealmTokenReadOnly(context.Background(), &authenticated)
	require.NoError(t, err)
	require.True(t, native)
	assert.Equal(t, 200, authenticated.RemainQuota, "unenrolled history shows only the current SQL projection")
	_, err = NativeRealmToken(context.Background(), &authenticated)
	require.ErrorIs(t, err, ErrNativeQuotaNotEnrolled, "a read-only token DTO must not become admission authority")
}
