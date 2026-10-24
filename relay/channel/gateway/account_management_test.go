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
