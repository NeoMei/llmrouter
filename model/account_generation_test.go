package model

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestAccountGenerationCurrentOwnerInheritance(t *testing.T) {
	for _, dialect := range []struct {
		name, env string
		open      func(string) gorm.Dialector
	}{
		{"sqlite", "", sqlite.Open},
		{"mysql57", "LLMROUTER_ACCOUNT_POOL_MYSQL_DSN", mysql.Open},
		{"postgres96", "LLMROUTER_ACCOUNT_POOL_POSTGRES_DSN", func(dsn string) gorm.Dialector {
			return postgres.New(postgres.Config{DSN: dsn, PreferSimpleProtocol: true})
		}},
	} {
		t.Run(dialect.name, func(t *testing.T) {
			dsn := os.Getenv(dialect.env)
			if dialect.name == "sqlite" {
				dsn = filepath.Join(t.TempDir(), "generation.db") + "?_pragma=busy_timeout(5000)"
			} else if dsn == "" {
				t.Skip("dedicated fixture not configured")
			}
			handles := make([]*gorm.DB, 2)
			for i := range handles {
				db, err := gorm.Open(dialect.open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
				require.NoError(t, err)
				handles[i] = db
				sqlDB, err := db.DB()
				require.NoError(t, err)
				t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
			}
			db, adminDB := handles[0], handles[1]
			require.NoError(t, db.AutoMigrate(&UpstreamAccountPool{}, &UpstreamAccountMember{}, &UpstreamAccountState{}))
			ctx := context.Background()
			now := time.Now().UTC()
			pool := "generation-test:" + uuid.NewString()
			group := "group:" + uuid.NewString()
			nextGroup := "next:" + uuid.NewString()
			t.Cleanup(func() {
				namespaces := []string{accountStateID("pool:" + pool), accountStateID("quota:" + group), accountStateID("quota:" + nextGroup)}
				require.NoError(t, db.Where("pool_id IN ?", namespaces).Delete(&UpstreamAccountState{}).Error)
				require.NoError(t, db.Where("pool_id IN ?", namespaces).Delete(&UpstreamAccountMember{}).Error)
				require.NoError(t, db.Where("pool_id IN ?", namespaces).Delete(&UpstreamAccountPool{}).Error)
			})
			owner, err := ResolveUpstreamAccountRef(ctx, db, pool, 59, 1, "provider-row", "")
			require.NoError(t, err)
			generation, err := ResolveUpstreamAccountRef(ctx, db, pool, 59, 1, "generation-A", "")
			require.NoError(t, err)
			expected := ""
			require.NoError(t, MutateManagedAccount(ctx, adminDB, owner, generation, "quota-group", UpstreamAccountScope{}, &group, &expected, now))
			scope := UpstreamAccountScope{Operation: "audio.speech.create", Model: "fixture", QuotaGroup: group}
			sentRef := generation
			sentRef.QuotaGroup = group
			require.NoError(t, RecordUpstreamAccountFailure(ctx, adminDB, UpstreamAccountSelection{Ref: sentRef}, scope, UpstreamAccountFailure{Kind: UpstreamAccountCooldown, Reason: "quota_exhausted", RetryAt: now.Add(time.Hour)}, now))
			synced, err := SyncUpstreamAccountGeneration(ctx, db, owner, "generation-A", now)
			require.NoError(t, err)
			assert.Equal(t, group, synced.QuotaGroup)
			_, err = SelectUpstreamAccount(ctx, db, []UpstreamAccountRef{synced}, scope, nil, now)
			var unavailable *UpstreamAccountUnavailable
			require.ErrorAs(t, err, &unavailable)
			// Membership synchronization cannot clear generation pause/isolation.
			require.NoError(t, SetUpstreamAccountPaused(ctx, adminDB, generation, true, now))
			require.NoError(t, RecordUpstreamAccountFailure(ctx, adminDB, UpstreamAccountSelection{Ref: generation}, UpstreamAccountScope{}, UpstreamAccountFailure{Kind: UpstreamAccountInvalid, Reason: "invalid_credential"}, now))
			_, err = SyncUpstreamAccountGeneration(ctx, db, owner, "generation-A", now)
			require.NoError(t, err)
			states, err := ProjectUpstreamAccounts(ctx, db, []UpstreamAccountRef{generation}, scope, now)
			require.NoError(t, err)
			require.Len(t, states, 1)
			assert.True(t, states[0].Paused)
			assert.True(t, states[0].Isolated)
			// A new credential generation inherits current membership too.
			fresh, err := SyncUpstreamAccountGeneration(ctx, db, owner, "generation-B", now)
			require.NoError(t, err)
			assert.Equal(t, group, fresh.QuotaGroup)
			assert.NotEqual(t, generation.MemberID, fresh.MemberID)
			// While sync owns the coordinator after its current owner read, start the
			// admin writer at its actual coordinator SQL boundary, then release sync.
			// Admin must serialize after sync and leave both owner/generation in nextGroup.
			ownerRead, releaseSync, adminAttempt := make(chan struct{}), make(chan struct{}), make(chan struct{})
			var readOnce, attemptOnce sync.Once
			require.NoError(t, db.Callback().Query().After("gorm:query").Register("generation_fixture_owner_read", func(tx *gorm.DB) {
				if m, ok := tx.Statement.Dest.(*UpstreamAccountMember); ok && m.MemberID == owner.MemberID {
					readOnce.Do(func() { close(ownerRead); <-releaseSync })
				}
			}))
			require.NoError(t, adminDB.Callback().Create().Before("gorm:create").Register("generation_fixture_admin_attempt", func(tx *gorm.DB) {
				if _, ok := tx.Statement.Dest.(*UpstreamAccountPool); ok {
					attemptOnce.Do(func() { close(adminAttempt) })
				}
			}))
			syncedDone, adminDone := make(chan error, 1), make(chan error, 1)
			go func() {
				_, err := SyncUpstreamAccountGeneration(ctx, db, owner, "generation-B", now)
				syncedDone <- err
			}()
			<-ownerRead
			go func() {
				adminDone <- MutateManagedAccount(ctx, adminDB, owner, fresh, "quota-group", UpstreamAccountScope{}, &nextGroup, &group, now)
			}()
			<-adminAttempt
			close(releaseSync)
			require.NoError(t, <-syncedDone)
			require.NoError(t, <-adminDone)
			require.NoError(t, db.Callback().Query().Remove("generation_fixture_owner_read"))
			require.NoError(t, adminDB.Callback().Create().Remove("generation_fixture_admin_attempt"))
			current, err := CurrentManagedAccountRef(ctx, db, owner)
			require.NoError(t, err)
			assert.Equal(t, nextGroup, current.QuotaGroup)
			current, err = CurrentManagedAccountRef(ctx, db, fresh)
			require.NoError(t, err)
			assert.Equal(t, nextGroup, current.QuotaGroup)
			// Fail closed on an unknown stable owner, never create a generation for it.
			unknown := owner
			unknown.MemberID = uuid.NewString()
			_, err = SyncUpstreamAccountGeneration(ctx, db, unknown, "forged-generation", now)
			require.Error(t, err)
		})
	}
}
