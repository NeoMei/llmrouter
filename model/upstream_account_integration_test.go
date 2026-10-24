package model

import (
	"context"
	"os"
	"strings"
	"sync"
	"sync/atomic"
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

// These DSNs must name dedicated disposable databases, never application databases.
func TestUpstreamAccountConfiguredDatabases(t *testing.T) {
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
			previousType := common.MainDatabaseType()
			// The explicit DB must remain authoritative even with unrelated globals.
			common.SetMainDatabaseType(common.DatabaseTypeSQLite)
			t.Cleanup(func() { common.SetMainDatabaseType(previousType) })
			dsn := os.Getenv(dialect.env)
			if dsn == "" {
				t.Skip(dialect.env + " is not configured")
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
			db, second := handles[0], handles[1]
			require.NoError(t, db.AutoMigrate(&UpstreamAccountPool{}, &UpstreamAccountMember{}, &UpstreamAccountState{}))
			require.NoError(t, db.AutoMigrate(&UpstreamAccountPool{}, &UpstreamAccountMember{}, &UpstreamAccountState{}))

			if dialect.name == "mysql57" {
				var collation string
				require.NoError(t, db.Raw("SELECT COLLATION_NAME FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ? AND COLUMN_NAME = ?", "upstream_account_states", "operation").Scan(&collation).Error)
				require.Contains(t, collation, "_ci", "the MySQL regression fixture must exercise an ordinary case-insensitive schema")
			}
			ctx := context.Background()
			now := time.Date(2026, 10, 24, 0, 0, 0, 0, time.UTC)
			poolID := "fixture:" + uuid.NewString() + strings.Repeat("p", 52)
			otherPool := "vision:" + uuid.NewString()
			group := "supplier:" + uuid.NewString() + strings.Repeat("g", 51)
			t.Cleanup(func() {
				namespaces := []string{poolID, otherPool, "pool:" + poolID, "pool:" + otherPool, "quota:" + group}
				for _, namespace := range append([]string(nil), namespaces...) {
					namespaces = append(namespaces, accountStateID(namespace))
				}
				require.NoError(t, db.Where("pool_id IN ?", namespaces).Delete(&UpstreamAccountState{}).Error)
				require.NoError(t, db.Where("pool_id IN ?", namespaces).Delete(&UpstreamAccountMember{}).Error)
				require.NoError(t, db.Where("pool_id IN ?", namespaces).Delete(&UpstreamAccountPool{}).Error)
			})
			refs := make([]UpstreamAccountRef, 3)
			for i, key := range []string{"A", "B", "C"} {
				var err error
				refs[i], err = ResolveUpstreamAccountRef(ctx, db, poolID, 17, i, "fixture-secret-"+key, group)
				require.NoError(t, err)
			}
			scope := UpstreamAccountScope{Operation: "speech", Model: "m"}
			// Admission probes use the same live scope checks without consuming rotation.
			require.NoError(t, CheckUpstreamAccounts(ctx, db, refs, scope, now))
			require.NoError(t, CheckUpstreamAccounts(ctx, second, refs, scope, now))
			var first UpstreamAccountSelection
			for i := range refs {
				sent, err := SelectUpstreamAccount(ctx, handles[i%2], refs, scope, nil, now)
				require.NoError(t, err)
				assert.Equal(t, refs[i].MemberID, sent.Ref.MemberID)
				if i == 0 {
					first = sent
				}
			}
			require.NoError(t, RecordUpstreamAccountFailure(ctx, second, first, scope, UpstreamAccountFailure{Kind: UpstreamAccountCooldown, Reason: "rate_limit", RetryAt: now.Add(time.Minute)}, now))
			require.NoError(t, RecordUpstreamAccountSuccess(ctx, db, first, scope, now))
			_, err := SelectUpstreamAccount(ctx, second, refs[:1], scope, nil, now)
			var unavailable *UpstreamAccountUnavailable
			require.ErrorAs(t, err, &unavailable)
			require.ErrorAs(t, CheckUpstreamAccounts(ctx, db, refs[:1], scope, now), &unavailable)
			_, err = SelectUpstreamAccount(ctx, db, refs[:1], UpstreamAccountScope{Operation: "text"}, nil, now)
			require.NoError(t, err)
			_, err = SelectUpstreamAccount(ctx, second, refs[:1], scope, nil, now.Add(time.Minute))
			require.NoError(t, err)
			require.NoError(t, SetUpstreamAccountPaused(ctx, db, refs[0], true, now))
			require.NoError(t, RecoverUpstreamAccount(ctx, second, refs[0], scope, now))
			_, err = SelectUpstreamAccount(ctx, second, refs[:1], scope, nil, now.Add(time.Hour))
			require.ErrorAs(t, err, &unavailable)
			require.NoError(t, SetUpstreamAccountPaused(ctx, db, refs[0], false, now))
			vision, err := ResolveUpstreamAccountRef(ctx, second, otherPool, 18, 0, "fixture-secret-vision", group)
			require.NoError(t, err)
			require.NoError(t, RecordUpstreamAccountFailure(ctx, db, first, UpstreamAccountScope{QuotaGroup: group}, UpstreamAccountFailure{Kind: UpstreamAccountCooldown, Reason: "quota_exhausted", RetryAt: now.Add(time.Hour)}, now))
			_, err = SelectUpstreamAccount(ctx, second, []UpstreamAccountRef{vision}, UpstreamAccountScope{Operation: "vision"}, nil, now)
			require.ErrorAs(t, err, &unavailable)
			require.NoError(t, RecoverUpstreamAccount(ctx, second, vision, UpstreamAccountScope{QuotaGroup: group}, now))

			// Force the exact MVCC ordering: the selector reads pool membership while
			// another pool still holds the quota-group failure transaction open. Its
			// post-lock health read must observe that newly committed failure.
			failureReady := make(chan struct{})
			allowFailure := make(chan struct{})
			selectionWaiting := make(chan struct{})
			var failureOnce, selectionOnce sync.Once
			require.NoError(t, db.Callback().Update().Before("gorm:update").Register("accountpool_fixture_failure", func(tx *gorm.DB) {
				if tx.Statement.Table == "upstream_account_states" {
					failureOnce.Do(func() { close(failureReady); <-allowFailure })
				}
			}))
			require.NoError(t, second.Callback().Create().Before("gorm:create").Register("accountpool_fixture_wait", func(tx *gorm.DB) {
				if pool, ok := tx.Statement.Dest.(*UpstreamAccountPool); ok && pool.PoolID == accountStateID("quota:"+group) {
					selectionOnce.Do(func() { close(selectionWaiting) })
				}
			}))
			failureDone := make(chan error, 1)
			go func() {
				failureDone <- RecordUpstreamAccountFailure(ctx, db, first, UpstreamAccountScope{QuotaGroup: group}, UpstreamAccountFailure{Kind: UpstreamAccountCooldown, Reason: "quota_exhausted", RetryAt: now.Add(time.Hour)}, now)
			}()
			<-failureReady
			selectionDone := make(chan error, 1)
			go func() {
				_, err := SelectUpstreamAccount(ctx, second, []UpstreamAccountRef{vision}, UpstreamAccountScope{Operation: "vision"}, nil, now)
				selectionDone <- err
			}()
			<-selectionWaiting
			close(allowFailure)
			require.NoError(t, <-failureDone)
			require.ErrorAs(t, <-selectionDone, &unavailable)
			require.NoError(t, db.Callback().Update().Remove("accountpool_fixture_failure"))
			require.NoError(t, second.Callback().Create().Remove("accountpool_fixture_wait"))
			require.NoError(t, RecoverUpstreamAccount(ctx, second, vision, UpstreamAccountScope{QuotaGroup: group}, now))
			testAccountExactScopes(t, db, second, refs[1], now)
			testAccountCaseDistinctPoolsAndGroups(t, db, second, now)

			start := make(chan struct{})
			results := make(chan UpstreamAccountSelection, 2)
			failures := make(chan error, 2)
			for _, handle := range handles {
				go func(handle *gorm.DB) {
					<-start
					sent, err := SelectUpstreamAccount(ctx, handle, refs, scope, nil, now.Add(time.Hour))
					results <- sent
					failures <- err
				}(handle)
			}
			close(start)
			a, b := <-results, <-results
			require.NoError(t, <-failures)
			require.NoError(t, <-failures)
			assert.NotEqual(t, a.Ref.MemberID, b.Ref.MemberID)
			require.NoError(t, RecordUpstreamAccountFailure(ctx, db, first, UpstreamAccountScope{}, UpstreamAccountFailure{Kind: UpstreamAccountInvalid, Reason: "invalid_credential"}, now))
			selected, err := SelectUpstreamAccount(ctx, second, refs, scope, nil, now)
			require.NoError(t, err)
			assert.NotEqual(t, first.Ref.MemberID, selected.Ref.MemberID)
		})
	}
}

// Distinct operation/model spellings are separate contracts even when a SQL
// collation equates case, accents, or trailing spaces.
func testAccountExactScopes(t *testing.T, db, second *gorm.DB, ref UpstreamAccountRef, now time.Time) {
	t.Helper()
	ctx := context.Background()
	for _, pair := range []struct {
		name               string
		failed, successful UpstreamAccountScope
	}{
		{"model case", UpstreamAccountScope{Operation: "scope-model", Model: "Vision"}, UpstreamAccountScope{Operation: "scope-model", Model: "vision"}},
		{"operation case", UpstreamAccountScope{Operation: "Speech", Model: "scope-operation"}, UpstreamAccountScope{Operation: "speech", Model: "scope-operation"}},
		{"accent", UpstreamAccountScope{Operation: "scope-accent", Model: "café"}, UpstreamAccountScope{Operation: "scope-accent", Model: "cafe"}},
		{"trailing space", UpstreamAccountScope{Operation: "scope-space", Model: "vision "}, UpstreamAccountScope{Operation: "scope-space", Model: "vision"}},
	} {
		t.Run(pair.name, func(t *testing.T) {
			sent, err := SelectUpstreamAccount(ctx, db, []UpstreamAccountRef{ref}, pair.failed, nil, now)
			require.NoError(t, err)
			require.NoError(t, RecordUpstreamAccountFailure(ctx, db, sent, pair.failed, UpstreamAccountFailure{Kind: UpstreamAccountCooldown, Reason: "rate_limit", RetryAt: now.Add(time.Minute)}, now))
			successful, err := SelectUpstreamAccount(ctx, second, []UpstreamAccountRef{ref}, pair.successful, nil, now)
			require.NoError(t, err)
			require.NoError(t, RecordUpstreamAccountSuccess(ctx, second, successful, pair.successful, now))
			_, err = SelectUpstreamAccount(ctx, db, []UpstreamAccountRef{ref}, pair.failed, nil, now)
			var unavailable *UpstreamAccountUnavailable
			require.ErrorAs(t, err, &unavailable)
			require.NoError(t, RecoverUpstreamAccount(ctx, db, ref, pair.failed, now))
		})
	}
	scope := UpstreamAccountScope{Operation: "exact-wildcard", Model: "Exact"}
	sent, err := SelectUpstreamAccount(ctx, db, []UpstreamAccountRef{ref}, scope, nil, now)
	require.NoError(t, err)
	for _, failureScope := range []UpstreamAccountScope{scope, {Operation: scope.Operation}, {Model: scope.Model}, {}} {
		require.NoError(t, RecordUpstreamAccountFailure(ctx, db, sent, failureScope, UpstreamAccountFailure{Kind: UpstreamAccountCooldown, Reason: "rate_limit", RetryAt: now.Add(time.Minute)}, now))
	}
	fresh, err := SelectUpstreamAccount(ctx, second, []UpstreamAccountRef{ref}, scope, nil, now.Add(time.Minute))
	require.NoError(t, err)
	require.NoError(t, RecordUpstreamAccountSuccess(ctx, second, fresh, scope, now.Add(time.Minute)))
	var states []UpstreamAccountState
	require.NoError(t, db.Where("subject = ?", "member:"+ref.MemberID).Find(&states).Error)
	for _, state := range states {
		assert.Empty(t, state.Kind, "successful exact scope must recover all applicable wildcard dimensions")
	}
}

func testAccountCaseDistinctPoolsAndGroups(t *testing.T, db, second *gorm.DB, now time.Time) {
	t.Helper()
	ctx := context.Background()
	base := "exact:" + uuid.NewString()
	pools := []string{base + ":Pool", base + ":pool"}
	groups := [][]string{{base + ":a", base + ":B"}, {base + ":A", base + ":b"}}
	refs := make([][]UpstreamAccountRef, 2)
	t.Cleanup(func() {
		namespaces := []string{pools[0], pools[1], "pool:" + pools[0], "pool:" + pools[1]}
		for _, pair := range groups {
			for _, group := range pair {
				namespaces = append(namespaces, "quota:"+group)
			}
		}
		// Also cover the pre-release hashed namespace representation.
		for _, namespace := range append([]string(nil), namespaces...) {
			namespaces = append(namespaces, accountStateID(namespace))
		}
		require.NoError(t, db.Where("pool_id IN ?", namespaces).Delete(&UpstreamAccountState{}).Error)
		require.NoError(t, db.Where("pool_id IN ?", namespaces).Delete(&UpstreamAccountMember{}).Error)
		require.NoError(t, db.Where("pool_id IN ?", namespaces).Delete(&UpstreamAccountPool{}).Error)
	})
	for i, pool := range pools {
		for j, group := range groups[i] {
			ref, err := ResolveUpstreamAccountRef(ctx, db, pool, 20+i, j, "fixture-secret-"+group, group)
			require.NoError(t, err)
			refs[i] = append(refs[i], ref)
		}
		_, err := SelectUpstreamAccount(ctx, db, refs[i], UpstreamAccountScope{}, nil, now)
		require.NoError(t, err)
	}
	impostor := refs[0][0]
	impostor.PoolID = pools[1]
	_, err := SelectUpstreamAccount(ctx, db, []UpstreamAccountRef{impostor}, UpstreamAccountScope{}, nil, now)
	require.Error(t, err, "a ref from a case-distinct pool cannot authorize a member from another pool")
	require.Error(t, SetUpstreamAccountQuotaGroup(ctx, db, impostor, "cross-pool", now))
	// A quota failure for lowercase a must leave uppercase A funding untouched.
	sent, err := SelectUpstreamAccount(ctx, db, refs[0][:1], UpstreamAccountScope{}, nil, now)
	require.NoError(t, err)
	require.NoError(t, RecordUpstreamAccountFailure(ctx, db, sent, UpstreamAccountScope{QuotaGroup: groups[0][0]}, UpstreamAccountFailure{Kind: UpstreamAccountCooldown, Reason: "quota_exhausted", RetryAt: now.Add(time.Hour)}, now))
	_, err = SelectUpstreamAccount(ctx, second, refs[1][:1], UpstreamAccountScope{}, nil, now)
	require.NoError(t, err)
	require.NoError(t, RecoverUpstreamAccount(ctx, db, refs[0][0], UpstreamAccountScope{QuotaGroup: groups[0][0]}, now))
	// Stop each selector after it owns one of its two group locks, before its
	// second group lock. CI-aliasing + raw-string ordering then creates a real
	// opposing lock cycle; exact identities allow both admissions to commit.
	release := make(chan struct{})
	ready := make(chan struct{}, 2)
	results := make(chan error, 2)
	for i, handle := range []*gorm.DB{db, second} {
		var creates atomic.Int32
		require.NoError(t, handle.Callback().Create().Before("gorm:create").Register("accountpool_exact_order", func(tx *gorm.DB) {
			if _, ok := tx.Statement.Dest.(*UpstreamAccountPool); ok && creates.Add(1) == 3 {
				ready <- struct{}{}
				<-release
			}
		}))
		go func(handle *gorm.DB, candidates []UpstreamAccountRef) {
			_, err := SelectUpstreamAccount(ctx, handle, candidates, UpstreamAccountScope{}, nil, now)
			results <- err
		}(handle, refs[i])
	}
	<-ready
	<-ready
	close(release)
	require.NoError(t, <-results)
	require.NoError(t, <-results)
	require.NoError(t, db.Callback().Create().Remove("accountpool_exact_order"))
	require.NoError(t, second.Callback().Create().Remove("accountpool_exact_order"))
}
