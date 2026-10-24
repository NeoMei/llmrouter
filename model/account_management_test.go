package model

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestAccountManagementProjection(t *testing.T) {
	db, _, refs := accountPoolFixture(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 25, 0, 0, 0, 0, time.UTC)
	scope := UpstreamAccountScope{Operation: "audio.speech.create", Model: "Voice"}
	require.NoError(t, SetUpstreamAccountQuotaGroup(ctx, db, refs[0], "shared", now))
	refs[0].QuotaGroup = "shared"
	require.NoError(t, RecordUpstreamAccountFailure(ctx, db, UpstreamAccountSelection{Ref: refs[0]}, scope, UpstreamAccountFailure{Kind: UpstreamAccountCooldown, Reason: "rate_limit", RetryAt: now.Add(time.Minute)}, now))
	require.NoError(t, RecordUpstreamAccountFailure(ctx, db, UpstreamAccountSelection{Ref: refs[0]}, UpstreamAccountScope{QuotaGroup: "shared"}, UpstreamAccountFailure{Kind: UpstreamAccountCooldown, Reason: "quota_exhausted", RetryAt: now.Add(2 * time.Minute)}, now))
	refs[0].QuotaGroup = "" // untrusted/stale refs must not suppress stored membership
	views, err := ProjectUpstreamAccounts(ctx, db, refs, scope, now)
	require.NoError(t, err)
	require.Len(t, views, 3)
	assert.Equal(t, "shared", views[0].QuotaGroup)
	assert.Equal(t, now.Add(2*time.Minute), *views[0].RetryAt)
	assert.Len(t, views[0].Blockers, 2)
	require.NoError(t, SetUpstreamAccountPaused(ctx, db, refs[0], true, now))
	views, err = ProjectUpstreamAccounts(ctx, db, refs, scope, now.Add(time.Hour))
	require.NoError(t, err)
	assert.True(t, views[0].Paused)
	assert.Nil(t, views[0].RetryAt)
	selected, err := SelectUpstreamAccount(ctx, db, refs[1:], scope, nil, now)
	require.NoError(t, err)
	assert.Equal(t, refs[1].MemberID, selected.Ref.MemberID)
	views, err = ProjectUpstreamAccounts(ctx, db, refs[1:], scope, now)
	require.NoError(t, err)
	selected, err = SelectUpstreamAccount(ctx, db, refs[1:], scope, nil, now)
	require.NoError(t, err)
	assert.Equal(t, refs[2].MemberID, selected.Ref.MemberID)
}

func TestAccountManagementNativeDistinctAndLegacy(t *testing.T) {
	db, _, _ := accountPoolFixture(t)
	ch := &Channel{Id: 19, Key: "A\nA\nB\n", ChannelInfo: ChannelInfo{IsMultiKey: true, MultiKeyStatusList: map[int]int{0: common.ChannelStatusManuallyDisabled, 2: common.ChannelStatusManuallyDisabled}}}
	rows, err := ChannelAccountManagement(context.Background(), db, ch)
	require.NoError(t, err)
	require.Len(t, rows, 2)
	assert.Equal(t, []int{0, 1}, rows[0].Indices)
	assert.Equal(t, []int{0}, rows[0].LegacyDisabledIndices)
	assert.Empty(t, rows[0].Barriers)
	assert.Equal(t, "legacy_disabled", rows[1].Barriers[0].Reason)
	ch.ChannelInfo.IsMultiKey = false
	_, err = ChannelAccountManagement(context.Background(), db, ch)
	require.ErrorIs(t, err, ErrAccountNotPool)
}

func TestAccountManagementExactScopeAndMembership(t *testing.T) {
	db, _, refs := accountPoolFixture(t)
	accountManagementConformance(t, db, refs)
}

// Shared by actual dialect fixtures; assertions protect observable eligibility,
// historical sent attribution, exact case-sensitive scope and no rotation.
func accountManagementConformance(t *testing.T, db *gorm.DB, refs []UpstreamAccountRef) {
	t.Helper()
	ctx := context.Background()
	now := time.Date(2026, 10, 25, 1, 2, 3, 0, time.UTC)
	scope := UpstreamAccountScope{Operation: "audio.speech.create", Model: "Voice"}
	oldGroup := "old:" + refs[0].MemberID
	newGroup := "new:" + refs[0].MemberID
	require.NoError(t, SetUpstreamAccountQuotaGroup(ctx, db, refs[0], oldGroup, now))
	sent, err := SelectUpstreamAccount(ctx, db, refs[:1], scope, nil, now)
	require.NoError(t, err)
	require.NoError(t, RecordUpstreamAccountFailure(ctx, db, sent, scope, UpstreamAccountFailure{Kind: UpstreamAccountCooldown, Reason: "rate_limit", RetryAt: now.Add(time.Minute)}, now))
	views, err := ProjectUpstreamAccounts(ctx, db, refs, UpstreamAccountScope{Operation: scope.Operation, Model: "voice"}, now)
	require.NoError(t, err)
	assert.Empty(t, views[0].Blockers)
	views, err = ProjectUpstreamAccounts(ctx, db, refs, UpstreamAccountScope{Operation: "audio.speech.create ", Model: "Voice"}, now)
	require.NoError(t, err)
	assert.Empty(t, views[0].Blockers)
	require.NoError(t, MutateManagedAccount(ctx, db, refs[0], refs[0], "quota-group", UpstreamAccountScope{}, &newGroup, &oldGroup, now.Add(time.Second)))
	// Delayed evidence still belongs to the group of the actual sent attempt.
	require.NoError(t, RecordUpstreamAccountFailure(ctx, db, sent, UpstreamAccountScope{QuotaGroup: oldGroup}, UpstreamAccountFailure{Kind: UpstreamAccountCooldown, Reason: "quota_exhausted", RetryAt: now.Add(time.Hour)}, now.Add(time.Second)))
	views, err = ProjectUpstreamAccounts(ctx, db, refs[:1], UpstreamAccountScope{Operation: "other", Model: "Voice"}, now)
	require.NoError(t, err)
	assert.Empty(t, views[0].Blockers)
	assert.Equal(t, newGroup, views[0].QuotaGroup)
	require.NoError(t, SetUpstreamAccountQuotaGroup(ctx, db, refs[1], oldGroup, now))
	views, err = ProjectUpstreamAccounts(ctx, db, refs[1:2], scope, now)
	require.NoError(t, err)
	require.Len(t, views[0].Blockers, 1)
	assert.Equal(t, "quota_group_global", views[0].Blockers[0].Scope)
	err = MutateManagedAccount(ctx, db, sent.Ref, refs[0], "recover-group", UpstreamAccountScope{}, &oldGroup, nil, now)
	require.EqualError(t, err, "stale_quota_group")
	views, err = ProjectUpstreamAccounts(ctx, db, refs[1:2], scope, now)
	require.NoError(t, err)
	assert.Len(t, views[0].Blockers, 1)
	require.NoError(t, MutateManagedAccount(ctx, db, refs[1], refs[1], "recover-group", UpstreamAccountScope{}, &oldGroup, nil, now))
	views, err = ProjectUpstreamAccounts(ctx, db, refs[1:2], scope, now)
	require.NoError(t, err)
	assert.Empty(t, views[0].Blockers)
	// Selected-scope recovery leaves a distinct model cooldown and manual pause.
	otherScope := UpstreamAccountScope{Operation: scope.Operation, Model: "Other"}
	require.NoError(t, RecordUpstreamAccountFailure(ctx, db, sent, otherScope, UpstreamAccountFailure{Kind: UpstreamAccountCooldown, Reason: "rate_limit", RetryAt: now.Add(2 * time.Minute)}, now))
	require.NoError(t, SetUpstreamAccountPaused(ctx, db, refs[0], true, now))
	require.NoError(t, MutateManagedAccount(ctx, db, refs[0], refs[0], "recover", scope, nil, nil, now))
	views, err = ProjectUpstreamAccounts(ctx, db, refs[:1], otherScope, now)
	require.NoError(t, err)
	assert.True(t, views[0].Paused)
	assert.Len(t, views[0].Blockers, 2)
	assert.Nil(t, views[0].RetryAt)
	require.NoError(t, SetUpstreamAccountPaused(ctx, db, refs[0], false, now))
	views, err = ProjectUpstreamAccounts(ctx, db, refs[:1], otherScope, now)
	require.NoError(t, err)
	assert.Equal(t, now.Add(2*time.Minute), *views[0].RetryAt)
	views, err = ProjectUpstreamAccounts(ctx, db, refs[:1], otherScope, now.Add(2*time.Minute))
	require.NoError(t, err)
	assert.Empty(t, views[0].Blockers)
	forged := refs[0]
	forged.MemberID = "00000000-0000-0000-0000-000000000000"
	_, err = ProjectUpstreamAccounts(ctx, db, []UpstreamAccountRef{forged}, scope, now)
	require.Error(t, err)
}

func TestAccountManagementConfiguredDatabases(t *testing.T) {
	for _, dialect := range []struct {
		name, env string
		open      func(string) gorm.Dialector
	}{
		{"mysql57", "LLMROUTER_ACCOUNT_POOL_MYSQL_DSN", mysql.Open},
		{"postgres96", "LLMROUTER_ACCOUNT_POOL_POSTGRES_DSN", func(dsn string) gorm.Dialector {
			return postgres.New(postgres.Config{DSN: dsn, PreferSimpleProtocol: true})
		}},
	} {
		t.Run(dialect.name, func(t *testing.T) {
			dsn := os.Getenv(dialect.env)
			if dsn == "" {
				t.Skip("dedicated fixture not configured")
			}
			db, err := gorm.Open(dialect.open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
			require.NoError(t, err)
			sqlDB, err := db.DB()
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
			require.NoError(t, db.AutoMigrate(&UpstreamAccountPool{}, &UpstreamAccountMember{}, &UpstreamAccountState{}))
			pool := "admin-test:" + uuid.NewString()
			refs := make([]UpstreamAccountRef, 3)
			for i := range refs {
				refs[i], err = ResolveUpstreamAccountRef(context.Background(), db, pool, 27, i, fmt.Sprintf("fixture-%d", i), "")
				require.NoError(t, err)
			}
			t.Cleanup(func() {
				namespaces := []string{accountStateID("pool:" + pool), accountStateID("quota:old:" + refs[0].MemberID), accountStateID("quota:new:" + refs[0].MemberID), accountStateID("quota:snapshot:" + refs[2].MemberID)}
				require.NoError(t, db.Where("pool_id IN ?", namespaces).Delete(&UpstreamAccountState{}).Error)
				require.NoError(t, db.Where("pool_id IN ?", namespaces).Delete(&UpstreamAccountMember{}).Error)
				require.NoError(t, db.Where("pool_id IN ?", namespaces).Delete(&UpstreamAccountPool{}).Error)
			})
			accountManagementConformance(t, db, refs)
			accountManagementOlderSnapshotConformance(t, db, refs[2])
			accountManagementCurrentStoreConformance(t, db)
		})
	}
}

func TestAccountManagementCurrentStoreSQLite(t *testing.T) {
	db, _, _ := accountPoolFixture(t)
	accountManagementCurrentStoreConformance(t, db)
}

func accountManagementCurrentStoreConformance(t *testing.T, db *gorm.DB) {
	t.Helper()
	require.NoError(t, db.AutoMigrate(&Channel{}, &ProviderProfile{}, &ProviderModel{}, &ProviderKeyPool{}))
	name := "admin-" + uuid.NewString()
	profile := ProviderProfile{Name: name, Status: "active"}
	require.NoError(t, db.Create(&profile).Error)
	pm := ProviderModel{ProfileId: profile.Id, ModelId: name, Status: "active"}
	require.NoError(t, db.Create(&pm).Error)
	key := ProviderKeyPool{ProfileId: profile.Id, Status: "inactive", EncryptedKey: []byte("fixture-encrypted")}
	require.NoError(t, db.Create(&key).Error)
	ch := Channel{Key: "fixture-first\nfixture-second", Models: name, ChannelInfo: ChannelInfo{IsMultiKey: true}}
	if db.Dialector.Name() == "postgres" {
		require.NoError(t, db.Omit("ChannelInfo").Create(&ch).Error)
		// Existing ChannelInfo.Value returns []byte; simple-protocol pgx treats
		// it as bytea, so seed PostgreSQL's JSON column explicitly as text.
		info, err := common.Marshal(ch.ChannelInfo)
		require.NoError(t, err)
		require.NoError(t, db.Model(&ch).Update("channel_info", string(info)).Error)
	} else {
		require.NoError(t, db.Create(&ch).Error)
	}

	t.Cleanup(func() {
		require.NoError(t, db.Delete(&key).Error)
		require.NoError(t, db.Delete(&pm).Error)
		require.NoError(t, db.Delete(&profile).Error)
		require.NoError(t, db.Delete(&ch).Error)
		ns := accountStateID(fmt.Sprintf("pool:channel:%d", ch.Id))
		require.NoError(t, db.Where("pool_id = ?", ns).Delete(&UpstreamAccountState{}).Error)
		require.NoError(t, db.Where("pool_id = ?", ns).Delete(&UpstreamAccountMember{}).Error)
		require.NoError(t, db.Where("pool_id = ?", ns).Delete(&UpstreamAccountPool{}).Error)
	})
	ctx := context.Background()
	var memberID string
	require.NoError(t, WithAccountManagementChannel(ctx, db, ch.Id, func(tx *gorm.DB, current *Channel) error {
		modelRow, profileRow, err := AccountManagementProfile(ctx, tx, name)
		require.NoError(t, err)
		assert.Equal(t, pm.Id, modelRow.Id)
		assert.Equal(t, profile.Id, profileRow.Id)
		keys, err := AccountManagementProviderRows(ctx, tx, profile.Id)
		require.NoError(t, err)
		require.Len(t, keys, 1)
		assert.Equal(t, "inactive", keys[0].Status)
		rows, err := ChannelAccountManagement(ctx, tx, current)
		require.NoError(t, err)
		require.Len(t, rows, 2)
		memberID = rows[0].Ref.MemberID
		return MutateManagedAccount(ctx, tx, rows[0].Ref, rows[0].Health, "pause", UpstreamAccountScope{}, nil, nil, time.Now())
	}))
	require.NoError(t, db.Model(&ch).Update("key", "fixture-second\nfixture-first").Error)
	require.NoError(t, WithAccountManagementChannel(ctx, db, ch.Id, func(tx *gorm.DB, current *Channel) error {
		rows, err := ChannelAccountManagement(ctx, tx, current)
		require.NoError(t, err)
		require.Len(t, rows, 2)
		assert.Equal(t, memberID, rows[1].Ref.MemberID)
		views, err := ProjectUpstreamAccounts(ctx, tx, []UpstreamAccountRef{rows[1].Ref}, UpstreamAccountScope{}, time.Now())
		require.NoError(t, err)
		assert.True(t, views[0].Paused)
		return nil
	}))
}

// Establish an older MVCC snapshot before another connection commits a group
// reassignment/refusal. Current locking reads must see both changes under the
// acquired coordinator, with no goroutine scheduling or timing assumptions.
func accountManagementOlderSnapshotConformance(t *testing.T, db *gorm.DB, ref UpstreamAccountRef) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	group := "snapshot:" + ref.MemberID
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		var before UpstreamAccountMember
		require.NoError(t, tx.First(&before, "member_id = ?", ref.MemberID).Error)
		assert.Empty(t, before.QuotaGroup)
		require.NoError(t, SetUpstreamAccountQuotaGroup(ctx, db, ref, group, now))
		sentRef := ref
		sentRef.QuotaGroup = group
		require.NoError(t, RecordUpstreamAccountFailure(ctx, db, UpstreamAccountSelection{Ref: sentRef}, UpstreamAccountScope{QuotaGroup: group}, UpstreamAccountFailure{Kind: UpstreamAccountCooldown, Reason: "quota_exhausted", RetryAt: now.Add(time.Minute)}, now))
		views, err := ProjectUpstreamAccounts(ctx, tx, []UpstreamAccountRef{ref}, UpstreamAccountScope{Operation: "audio.speech.create", Model: "Voice"}, now)
		require.NoError(t, err)
		require.Len(t, views, 1)
		assert.Equal(t, group, views[0].QuotaGroup)
		require.Len(t, views[0].Blockers, 1)
		current, err := CurrentManagedAccountRef(ctx, tx, ref)
		require.NoError(t, err)
		assert.Equal(t, group, current.QuotaGroup)
		return nil
	}))
}
