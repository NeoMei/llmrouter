package model

import (
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/internal/platformaccount"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"testing"
)

func TestTrustedNativeBindingKeepsOriginalUserAndBusinessState(t *testing.T) {
	previous := DB
	previousRedis := common.RedisEnabled
	db, e := gorm.Open(sqlite.Open(t.TempDir()+"/parity.db"), &gorm.Config{})
	require.NoError(t, e)
	require.NoError(t, db.AutoMigrate(&User{}, &UniHubIdentityMapping{}))
	DB = db
	common.RedisEnabled = false
	t.Cleanup(func() { DB = previous; common.RedisEnabled = previousRedis })
	u := User{Username: "original", AffCode: "first-aff", Password: "original-hash", Role: common.RoleRootUser, Status: common.UserStatusEnabled, Quota: 12345, UsedQuota: 77, RequestCount: 8, AuthVersion: 4}
	require.NoError(t, db.Create(&u).Error)
	identity := platformaccount.Identity{Issuer: "https://hub.test", User: platformaccount.User{ID: "central-17", Name: "Original", Email: "same@test"}}
	bound, e := BindUniHubIdentity(identity, u.Id)
	require.NoError(t, e)
	require.Equal(t, u.Id, bound.Id)
	require.Equal(t, u.Password, bound.Password)
	require.Equal(t, u.Quota, bound.Quota)
	require.Equal(t, u.Role, bound.Role)
	projected, e := CheckUniHubProjection(u.Id)
	require.NoError(t, e)
	require.True(t, projected)
	resolved, e := ResolveUniHubProjection(identity)
	require.NoError(t, e)
	require.Equal(t, u.Id, resolved.Id)
	other := User{Username: "other", AffCode: "other-aff", Email: "same@test", Role: common.RoleCommonUser, Status: common.UserStatusEnabled}
	require.NoError(t, db.Create(&other).Error)
	_, e = BindUniHubIdentity(identity, other.Id)
	require.Error(t, e)
	var count int64
	require.NoError(t, db.Model(&User{}).Count(&count).Error)
	require.EqualValues(t, 2, count)
}
