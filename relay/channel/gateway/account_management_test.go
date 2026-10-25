package gateway

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/crypto"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestGatewayManagementIncludesAllStatesWithoutSelecting(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "gateway.db")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	require.NoError(t, db.AutoMigrate(&model.ProviderKeyPool{}, &model.UpstreamAccountPool{}, &model.UpstreamAccountMember{}, &model.UpstreamAccountState{}))
	c, err := crypto.NewAESGCM([]byte("0123456789abcdef0123456789abcdef"))
	require.NoError(t, err)
	encrypted, err := c.Encrypt([]byte("private-credential"))
	require.NoError(t, err)
	now := time.Now().UTC()
	retry := now.Add(time.Hour)
	rows := []model.ProviderKeyPool{{ProfileId: 44, Status: "active", EncryptedKey: encrypted, CooldownUntil: &retry, LastError: "raw-secret-error"}, {ProfileId: 44, Status: "inactive", EncryptedKey: encrypted}, {ProfileId: 44, Status: "active", EncryptedKey: []byte("invalid")}}
	require.NoError(t, db.Create(&rows).Error)
	selector := NewKeySelector(nil, c) // supplied DB is authority, not selector's handle
	managed, err := selector.ManagementAccounts(context.Background(), db, 44, 59, now)
	require.NoError(t, err)
	require.Len(t, managed, 3)
	assert.Equal(t, "legacy_cooldown", managed[0].Barriers[0].Reason)
	assert.Equal(t, retry, *managed[0].Barriers[0].RetryAt)
	assert.Equal(t, "legacy_inactive", managed[1].Barriers[0].Reason)
	assert.Equal(t, "credential_unavailable", managed[2].Barriers[0].Reason)
	assert.Empty(t, managed[2].Health.MemberID)
	var members []model.UpstreamAccountMember
	require.NoError(t, db.Find(&members).Error)
	for _, member := range members {
		assert.Zero(t, member.LastSelected)
	}
	encoded, err := common.Marshal(managed)
	require.NoError(t, err)
	for _, secret := range []string{"private-credential", "raw-secret-error", "invalid", "identity_digest", "encrypted_key"} {
		assert.NotContains(t, string(encoded), secret)
	}
	// Expiry removes only the timed legacy barrier; no provider rotation/write.
	managed, err = selector.ManagementAccounts(context.Background(), db, 44, 59, retry)
	require.NoError(t, err)
	assert.Empty(t, managed[0].Barriers)
	var persisted model.ProviderKeyPool
	require.NoError(t, db.First(&persisted, rows[0].Id).Error)
	assert.Nil(t, persisted.LastUsedAt)
}

func TestGatewayRelayCannotOverwriteCommittedAdminGroup(t *testing.T) {
	dir := t.TempDir()
	open := func() *gorm.DB {
		db, err := gorm.Open(sqlite.Open(filepath.Join(dir, "interleaving.db")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
		require.NoError(t, err)
		sqlDB, err := db.DB()
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
		return db
	}
	adminDB, relayDB := open(), open()
	require.NoError(t, adminDB.AutoMigrate(&model.Channel{}, &model.ProviderKeyPool{}, &model.UpstreamAccountPool{}, &model.UpstreamAccountMember{}, &model.UpstreamAccountState{}))
	ch := model.Channel{Key: "fixture", Models: "fixture"}
	require.NoError(t, adminDB.Create(&ch).Error)
	c, err := crypto.NewAESGCM([]byte("0123456789abcdef0123456789abcdef"))
	require.NoError(t, err)
	encrypted, err := c.Encrypt([]byte("fixture-generation-A"))
	require.NoError(t, err)
	key := model.ProviderKeyPool{ProfileId: 44, Status: "active", EncryptedKey: encrypted}
	require.NoError(t, adminDB.Create(&key).Error)
	ctx := context.Background()
	adminSel, relaySel := NewKeySelector(adminDB, c), NewKeySelector(relayDB, c)
	rows, err := adminSel.ManagementAccounts(ctx, adminDB, 44, ch.Id, time.Now())
	require.NoError(t, err)
	owner, health := rows[0].Ref, rows[0].Health
	group, expected := "new-admin-group", ""
	scope := model.UpstreamAccountScope{Operation: "audio.speech.create", Model: "fixture"}
	fired := false
	require.NoError(t, relayDB.Callback().Query().After("gorm:query").Register("fixture_admin_after_owner_read", func(tx *gorm.DB) {
		member, ok := tx.Statement.Dest.(*model.UpstreamAccountMember)
		if !ok || fired || member.MemberID != owner.MemberID {
			return
		}
		fired = true
		require.NoError(t, model.WithAccountManagementChannel(ctx, adminDB, ch.Id, func(db *gorm.DB, current *model.Channel) error {
			currentRows, err := adminSel.ManagementAccounts(ctx, db, 44, current.Id, time.Now())
			if err != nil {
				return err
			}
			if err = model.MutateManagedAccount(ctx, db, currentRows[0].Ref, currentRows[0].Health, "quota-group", model.UpstreamAccountScope{}, &group, &expected, time.Now()); err != nil {
				return err
			}
			ref := currentRows[0].Health
			ref.QuotaGroup = group
			groupScope := scope
			groupScope.QuotaGroup = group
			return model.RecordUpstreamAccountFailure(ctx, db, model.UpstreamAccountSelection{Ref: ref}, groupScope, model.UpstreamAccountFailure{Kind: model.UpstreamAccountCooldown, Reason: "quota_exhausted", RetryAt: time.Now().Add(time.Hour)}, time.Now())
		}))
	}))
	selected, _, err := relaySel.SelectPlatformAccount(ctx, 44, ch.Id, scope, nil)
	var unavailable *model.UpstreamAccountUnavailable
	require.ErrorAs(t, err, &unavailable, "current selected-scope group refusal must prevent admission")
	assert.Empty(t, selected.Ref.MemberID)
	assert.True(t, fired)
	var storedOwner, storedHealth model.UpstreamAccountMember
	require.NoError(t, adminDB.First(&storedOwner, "member_id = ?", owner.MemberID).Error)
	require.NoError(t, adminDB.First(&storedHealth, "member_id = ?", health.MemberID).Error)
	assert.Equal(t, group, storedOwner.QuotaGroup)
	assert.Equal(t, group, storedHealth.QuotaGroup)
	require.NoError(t, relayDB.Callback().Query().Remove("fixture_admin_after_owner_read"))
	// A distinct operation remains eligible, credential refresh inherits the same
	// group, and the stable owner's manual pause still blocks that new generation.
	other := model.UpstreamAccountScope{Operation: "chat.completions.create", Model: "fixture"}
	selected, credential, err := relaySel.SelectPlatformAccount(ctx, 44, ch.Id, other, nil)
	require.NoError(t, err)
	assert.Equal(t, group, selected.Ref.QuotaGroup)
	assert.Equal(t, "fixture-generation-A", credential)
	encrypted, err = c.Encrypt([]byte("fixture-generation-B"))
	require.NoError(t, err)
	require.NoError(t, adminDB.Model(&key).Update("encrypted_key", encrypted).Error)
	refreshed, credential, err := relaySel.SelectPlatformAccount(ctx, 44, ch.Id, other, nil)
	require.NoError(t, err)
	assert.NotEqual(t, selected.Ref.MemberID, refreshed.Ref.MemberID)
	assert.Equal(t, group, refreshed.Ref.QuotaGroup)
	assert.Equal(t, "fixture-generation-B", credential)
	_, _, err = relaySel.SelectPlatformAccount(ctx, 44, ch.Id, scope, nil)
	require.ErrorAs(t, err, &unavailable)
	require.NoError(t, model.SetUpstreamAccountPaused(ctx, adminDB, owner, true, time.Now()))
	_, _, err = relaySel.SelectPlatformAccount(ctx, 44, ch.Id, other, nil)
	require.ErrorAs(t, err, &unavailable)
}
