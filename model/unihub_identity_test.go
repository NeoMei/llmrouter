package model

import (
	"sync"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/internal/platformaccount"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestUniHubProjectionConcurrentFirstLoginAndMutation(t *testing.T) {
	previousDB, previousRedis := DB, common.RedisEnabled
	db, err := gorm.Open(sqlite.Open(t.TempDir()+"/unihub.db"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&User{}, &UniHubIdentityMapping{}))
	DB, common.RedisEnabled = db, false
	t.Cleanup(func() { DB, common.RedisEnabled = previousDB, previousRedis })
	identity := platformaccount.Identity{Issuer: "https://unihub.test", User: platformaccount.User{ID: "fixed-subject", Email: "same@test", Name: "Same"}}
	const callers = 4
	ids := make([]int, callers)
	errs := make([]error, callers)
	var wg sync.WaitGroup
	for i := range ids {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			var u *User
			u, errs[i] = ResolveUniHubProjection(identity)
			if u != nil {
				ids[i] = u.Id
			}
		}(i)
	}
	wg.Wait()
	for i := range ids {
		require.NoError(t, errs[i])
		require.Equal(t, ids[0], ids[i])
	}
	var count int64
	require.NoError(t, db.Model(&User{}).Count(&count).Error)
	require.EqualValues(t, 1, count)
	other := identity
	other.User.ID = "other-subject"
	otherUser, err := ResolveUniHubProjection(other)
	require.NoError(t, err)
	require.NotEqual(t, ids[0], otherUser.Id)
	require.NoError(t, db.Exec("UPDATE users SET role = ? WHERE id = ?", common.RoleAdminUser, ids[0]).Error)
	_, err = CheckUniHubProjection(ids[0])
	require.NoError(t, err)
}

func TestRuntimeSSOReusesDisabledProjectionBeforeCurrentCentralProof(t *testing.T) {
	old, redis := DB, common.RedisEnabled
	db, e := gorm.Open(sqlite.Open(t.TempDir()+"/reenable.db"), &gorm.Config{})
	require.NoError(t, e)
	require.NoError(t, db.AutoMigrate(&User{}, &UniHubIdentityMapping{}))
	DB = db
	common.RedisEnabled = false
	t.Cleanup(func() { DB = old; common.RedisEnabled = redis })
	identity := platformaccount.Identity{Issuer: "https://hub.test", User: platformaccount.User{ID: "canonical"}}
	u, e := ResolveUniHubProjection(identity)
	require.NoError(t, e)
	require.NoError(t, db.Model(&User{}).Where("id = ?", u.Id).Update("status", common.UserStatusDisabled).Error)
	_, e = ResolveUniHubProjection(identity)
	require.Error(t, e, "standalone legacy disabled user remains denied")
	t.Setenv("UNIHUB_PRODUCT_RUNTIME_ENABLED", "true")
	current, e := ResolveUniHubProjection(identity)
	require.NoError(t, e)
	require.Equal(t, u.Id, current.Id)
	require.Equal(t, common.UserStatusDisabled, current.Status, "mapping resolution cannot invent authority or enable locally")
}
