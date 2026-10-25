package model

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func accountPoolFixture(t *testing.T) (*gorm.DB, *gorm.DB, []UpstreamAccountRef) {
	t.Helper()
	dsn := filepath.Join(t.TempDir(), "pool.db") + "?_pragma=busy_timeout(5000)"
	open := func() *gorm.DB {
		db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
		require.NoError(t, err)
		sqlDB, err := db.DB()
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
		return db
	}
	db, second := open(), open()
	require.NoError(t, db.AutoMigrate(&UpstreamAccountPool{}, &UpstreamAccountMember{}, &UpstreamAccountState{}))
	refs := make([]UpstreamAccountRef, 3)
	for i, credential := range []string{"fixture-secret-A", "fixture-secret-B", "fixture-secret-C"} {
		var err error
		refs[i], err = ResolveUpstreamAccountRef(context.Background(), db, "channel:17", 17, i, credential, "")
		require.NoError(t, err)
	}
	return db, second, refs
}

func TestUpstreamAccountSelectionPersistenceAndScopes(t *testing.T) {
	db, second, refs := accountPoolFixture(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 24, 0, 0, 0, 0, time.UTC)
	speech := UpstreamAccountScope{Operation: "speech", Model: "m"}
	text := UpstreamAccountScope{Operation: "text", Model: "m"}
	var sentA UpstreamAccountSelection
	for i, handle := range []*gorm.DB{db, second, db} {
		sent, err := SelectUpstreamAccount(ctx, handle, refs, speech, nil, now)
		require.NoError(t, err)
		assert.Equal(t, refs[i].MemberID, sent.Ref.MemberID)
		if i == 0 {
			sentA = sent
		}
	}
	require.NoError(t, RecordUpstreamAccountFailure(ctx, second, sentA, speech, UpstreamAccountFailure{Kind: UpstreamAccountCooldown, Reason: "rate_limit", RetryAt: now.Add(time.Minute)}, now))
	_, err := SelectUpstreamAccount(ctx, db, refs[:1], speech, nil, now)
	var unavailable *UpstreamAccountUnavailable
	require.ErrorAs(t, err, &unavailable)
	assert.Equal(t, now.Add(time.Minute), unavailable.RetryAt)
	textSent, err := SelectUpstreamAccount(ctx, second, refs[:1], text, nil, now)
	require.NoError(t, err)
	assert.Equal(t, refs[0], textSent.Ref)
	require.NoError(t, RecordUpstreamAccountSuccess(ctx, db, sentA, speech, now.Add(time.Second)))
	_, err = SelectUpstreamAccount(ctx, db, refs[:1], speech, nil, now.Add(time.Second))
	require.ErrorAs(t, err, &unavailable)
	_, err = SelectUpstreamAccount(ctx, second, refs[:1], speech, nil, now.Add(time.Minute))
	require.NoError(t, err)
	require.NoError(t, SetUpstreamAccountPaused(ctx, db, refs[0], true, now))
	require.NoError(t, RecoverUpstreamAccount(ctx, second, refs[0], speech, now))
	_, err = SelectUpstreamAccount(ctx, second, refs[:1], text, nil, now.Add(time.Hour))
	require.ErrorAs(t, err, &unavailable)
	assert.True(t, unavailable.RetryAt.IsZero())
	require.NoError(t, SetUpstreamAccountPaused(ctx, db, refs[0], false, now))
	require.NoError(t, RecordUpstreamAccountFailure(ctx, db, textSent, UpstreamAccountScope{}, UpstreamAccountFailure{Kind: UpstreamAccountInvalid, Reason: "invalid_credential"}, now))
	selected, err := SelectUpstreamAccount(ctx, second, refs, text, nil, now)
	require.NoError(t, err)
	assert.NotEqual(t, refs[0].MemberID, selected.Ref.MemberID)
	_, err = SelectUpstreamAccount(ctx, db, refs, text, map[string]bool{refs[1].MemberID: true, refs[2].MemberID: true}, now)
	require.ErrorAs(t, err, &unavailable)
}

func TestUpstreamAccountIdentityAndSharedQuota(t *testing.T) {
	db, second, refs := accountPoolFixture(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 24, 0, 0, 0, 0, time.UTC)
	reordered, err := ResolveUpstreamAccountRef(ctx, second, "channel:17", 17, 99, "fixture-secret-A", "shared")
	require.NoError(t, err)
	assert.Equal(t, refs[0].MemberID, reordered.MemberID)
	replacement, err := ResolveUpstreamAccountRef(ctx, db, "channel:17", 17, 0, "replacement", "")
	require.NoError(t, err)
	assert.NotEqual(t, refs[0].MemberID, replacement.MemberID)
	require.NoError(t, SetUpstreamAccountQuotaGroup(ctx, db, refs[0], "shared", now))
	require.NoError(t, SetUpstreamAccountQuotaGroup(ctx, db, refs[1], "shared", now))
	refs[0].QuotaGroup = "shared"
	refs[1].QuotaGroup = "shared"
	sent, err := SelectUpstreamAccount(ctx, db, refs[:1], UpstreamAccountScope{}, nil, now)
	require.NoError(t, err)
	require.NoError(t, RecordUpstreamAccountFailure(ctx, second, sent, UpstreamAccountScope{QuotaGroup: "shared"}, UpstreamAccountFailure{Kind: UpstreamAccountCooldown, Reason: "quota_exhausted", RetryAt: now.Add(time.Hour)}, now))
	selected, err := SelectUpstreamAccount(ctx, db, refs, UpstreamAccountScope{Operation: "text"}, nil, now)
	require.NoError(t, err)
	assert.Equal(t, refs[2].MemberID, selected.Ref.MemberID)
	selected, err = SelectUpstreamAccount(ctx, db, []UpstreamAccountRef{replacement}, UpstreamAccountScope{}, nil, now)
	require.NoError(t, err)
	assert.Equal(t, replacement.MemberID, selected.Ref.MemberID)
	public, err := common.Marshal(selected)
	require.NoError(t, err)
	assert.NotContains(t, string(public), "replacement")
	assert.NotContains(t, string(public), "digest")
	var member UpstreamAccountMember
	require.NoError(t, db.First(&member, "member_id = ?", refs[0].MemberID).Error)
	public, err = common.Marshal(member)
	require.NoError(t, err)
	assert.NotContains(t, string(public), "fixture-secret")
	assert.NotContains(t, string(public), member.IdentityDigest)
}

func TestUpstreamAccountConcurrentSelectorsAndFailClosed(t *testing.T) {
	db, second, refs := accountPoolFixture(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 24, 0, 0, 0, 0, time.UTC)
	start := make(chan struct{})
	results := make(chan UpstreamAccountSelection, 2)
	failures := make(chan error, 2)
	for _, handle := range []*gorm.DB{db, second} {
		go func(handle *gorm.DB) {
			<-start
			selected, err := SelectUpstreamAccount(ctx, handle, refs, UpstreamAccountScope{}, nil, now)
			results <- selected
			failures <- err
		}(handle)
	}
	close(start)
	first, next := <-results, <-results
	require.NoError(t, <-failures)
	require.NoError(t, <-failures)
	assert.NotEqual(t, first.Ref.MemberID, next.Ref.MemberID)
	require.NoError(t, db.Migrator().DropTable(&UpstreamAccountState{}))
	_, err := SelectUpstreamAccount(ctx, second, refs, UpstreamAccountScope{}, nil, now)
	require.Error(t, err)
	var unavailable *UpstreamAccountUnavailable
	assert.False(t, errors.As(err, &unavailable), "database failure must propagate separately from pool exhaustion")
}

func TestUpstreamAccountQuotaGroupAcrossPools(t *testing.T) {
	db, second, refs := accountPoolFixture(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 24, 0, 0, 0, 0, time.UTC)
	require.NoError(t, SetUpstreamAccountQuotaGroup(ctx, db, refs[0], "supplier:account1", now))
	refs[0].QuotaGroup = "supplier:account1"
	separate, err := ResolveUpstreamAccountRef(ctx, second, "channel:18", 18, 0, "other-endpoint-key", "supplier:account1")
	require.NoError(t, err)
	sent, err := SelectUpstreamAccount(ctx, db, refs[:1], UpstreamAccountScope{Operation: "speech"}, nil, now)
	require.NoError(t, err)
	require.NoError(t, RecordUpstreamAccountFailure(ctx, db, sent, UpstreamAccountScope{QuotaGroup: "supplier:account1"}, UpstreamAccountFailure{Kind: UpstreamAccountCooldown, Reason: "quota_exhausted", RetryAt: now.Add(time.Hour)}, now))
	_, err = SelectUpstreamAccount(ctx, second, []UpstreamAccountRef{separate}, UpstreamAccountScope{Operation: "vision"}, nil, now)
	var unavailable *UpstreamAccountUnavailable
	require.ErrorAs(t, err, &unavailable)
	assert.Equal(t, now.Add(time.Hour), unavailable.RetryAt)
	require.NoError(t, RecoverUpstreamAccount(ctx, second, separate, UpstreamAccountScope{QuotaGroup: "supplier:account1"}, now))
	_, err = SelectUpstreamAccount(ctx, db, refs[:1], UpstreamAccountScope{Operation: "speech"}, nil, now)
	require.NoError(t, err)
	require.NoError(t, RecordUpstreamAccountFailure(ctx, db, sent, UpstreamAccountScope{Operation: "speech"}, UpstreamAccountFailure{Kind: UpstreamAccountCooldown, Reason: "rate_limit", RetryAt: now.Add(time.Minute)}, now))
	_, err = SelectUpstreamAccount(ctx, second, []UpstreamAccountRef{separate}, UpstreamAccountScope{Operation: "speech"}, nil, now)
	require.NoError(t, err)
}

func TestUpstreamAccountExplicitClockAndManagementBoundaries(t *testing.T) {
	db, _, _ := accountPoolFixture(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 24, 0, 0, 0, 0, time.UTC)
	ref, err := ResolveUpstreamAccountRef(ctx, db, strings.Repeat("p", 96), 19, 0, "fixture-secret-boundary", strings.Repeat("g", 96))
	require.NoError(t, err)
	sent, err := SelectUpstreamAccount(ctx, db, []UpstreamAccountRef{ref}, UpstreamAccountScope{}, nil, now)
	require.NoError(t, err)
	require.NoError(t, RecordUpstreamAccountFailure(ctx, db, sent, UpstreamAccountScope{}, UpstreamAccountFailure{Kind: UpstreamAccountCooldown, Reason: "rate_limit", RetryAt: now.Add(time.Minute)}, now))
	var state UpstreamAccountState
	require.NoError(t, db.Where("subject = ?", "member:"+ref.MemberID).First(&state).Error)
	assert.Equal(t, now.UnixNano(), state.UpdatedAt)
	_, err = ResolveUpstreamAccountRef(ctx, db, strings.Repeat("p", 97), 19, 0, "private", "")
	require.Error(t, err)
	require.Error(t, RecordUpstreamAccountFailure(ctx, db, sent, UpstreamAccountScope{}, UpstreamAccountFailure{Kind: UpstreamAccountInvalid, Reason: "rate_limit"}, now))
	fresh, err := SelectUpstreamAccount(ctx, db, []UpstreamAccountRef{ref}, UpstreamAccountScope{}, nil, now.Add(time.Minute))
	require.NoError(t, err)
	require.NoError(t, RecordUpstreamAccountSuccess(ctx, db, fresh, UpstreamAccountScope{}, now.Add(time.Minute)))
	state = UpstreamAccountState{}
	require.NoError(t, db.Where("subject = ?", "member:"+ref.MemberID).First(&state).Error)
	assert.Empty(t, state.Kind)
}

func TestUpstreamAccountQuotaMembershipIsPersistedAuthority(t *testing.T) {
	db, second, refs := accountPoolFixture(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 24, 0, 0, 0, 0, time.UTC)
	require.NoError(t, SetUpstreamAccountQuotaGroup(ctx, db, refs[0], "supplier:shared", now))
	resolved, err := ResolveUpstreamAccountRef(ctx, second, "channel:17", 17, 0, "fixture-secret-A", "")
	require.NoError(t, err)
	assert.Equal(t, "supplier:shared", resolved.QuotaGroup)
	refs[0].QuotaGroup = "request:forged"
	sent, err := SelectUpstreamAccount(ctx, second, refs[:1], UpstreamAccountScope{}, nil, now)
	require.NoError(t, err)
	assert.Equal(t, "supplier:shared", sent.Ref.QuotaGroup)
	require.NoError(t, SetUpstreamAccountQuotaGroup(ctx, db, refs[0], "", now))
	resolved, err = ResolveUpstreamAccountRef(ctx, second, "channel:17", 17, 0, "fixture-secret-A", "request:forged")
	require.NoError(t, err)
	assert.Empty(t, resolved.QuotaGroup)
}

func TestUpstreamAccountExactScopesSQLite(t *testing.T) {
	db, second, refs := accountPoolFixture(t)
	testAccountExactScopes(t, db, second, refs[0], time.Date(2026, 10, 24, 0, 0, 0, 0, time.UTC))
}

func TestUpstreamAccountRejectsCaseAliasedMemberHandle(t *testing.T) {
	db, _, _ := accountPoolFixture(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 24, 0, 0, 0, 0, time.UTC)
	ref := UpstreamAccountRef{PoolID: "channel:17", MemberID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", ChannelID: 17, KeyIndex: 3}
	require.NoError(t, db.Create(&UpstreamAccountMember{MemberID: ref.MemberID, PoolID: accountStateID("pool:" + ref.PoolID), IdentityDigest: strings.Repeat("a", 64)}).Error)
	aliased := ref
	aliased.MemberID = strings.ToUpper(ref.MemberID)
	require.Error(t, SetUpstreamAccountQuotaGroup(ctx, db, aliased, "unrelated-account", now))
	require.Error(t, SetUpstreamAccountPaused(ctx, db, aliased, true, now))
	_, err := SelectUpstreamAccount(ctx, db, []UpstreamAccountRef{ref}, UpstreamAccountScope{}, nil, now)
	require.NoError(t, err)
	var member UpstreamAccountMember
	require.NoError(t, db.First(&member, "member_id = ?", ref.MemberID).Error)
	assert.Empty(t, member.QuotaGroup)
}
