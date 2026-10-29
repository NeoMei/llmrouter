package platformbilling

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// External PostgreSQL targets must be dedicated task-owned schemas.
func TestKnowledgeResultVaultContract(t *testing.T) {
	targets := map[string]string{"sqlite": "file:" + t.TempDir() + "/results.db"}
	if dsn := os.Getenv("KNOWLEDGE_RESULT_TEST_POSTGRES_DSN"); dsn != "" {
		targets["postgres"] = dsn
	}
	for dialect, dsn := range targets {
		t.Run(dialect, func(t *testing.T) {
			var dialector gorm.Dialector = sqlite.Open(dsn)
			if dialect == "postgres" {
				dialector = postgres.Open(dsn)
			}
			db, err := gorm.Open(dialector, &gorm.Config{})
			require.NoError(t, err)
			require.NoError(t, db.AutoMigrate(&Call{}))
			key := []byte("0123456789abcdef0123456789abcdef")
			v, err := newKnowledgeResultVault(db, key, time.Hour)
			require.NoError(t, err)
			clock := time.Now()
			v.now = func() time.Time { return clock }
			row := Call{CallID: "vault-test", InstanceID: "default", ActorID: "actor", AccountID: "payer", TaskID: "task", OriginProductID: "superwagie", Model: "model", Operation: "embedding", WireOperation: "embeddings.create", Digest: "digest", Status: "UNKNOWN", Evidence: `{"version":"metered_v2","operation":"embedding","state":"confirmed"}`}
			require.NoError(t, db.Create(&row).Error)
			result := []byte(`{"data":[{"embedding":[0.1,0.2]}]}`)
			require.NoError(t, v.capture(context.Background(), row.CallID, result))
			assert.Error(t, v.capture(context.Background(), row.CallID, result))
			var stored knowledgeResultRow
			require.NoError(t, db.First(&stored, "call_id = ?", row.CallID).Error)
			assert.NotContains(t, string(stored.Ciphertext), string(result))
			_, err = v.readSettled(context.Background(), row.CallID)
			assert.Error(t, err)
			require.NoError(t, db.Model(&Call{}).Where("call_id = ?", row.CallID).Update("status", "SETTLED").Error)
			got, err := v.readSettled(context.Background(), row.CallID)
			require.NoError(t, err)
			assert.Equal(t, result, got)
			reopened, err := newKnowledgeResultVault(db, key, time.Hour)
			require.NoError(t, err)
			reopened.now = v.now
			got, err = reopened.readSettled(context.Background(), row.CallID)
			require.NoError(t, err)
			assert.Equal(t, result, got)
			require.NoError(t, db.Model(&Call{}).Where("call_id = ?", row.CallID).Update("evidence_conflict", true).Error)
			_, err = v.readSettled(context.Background(), row.CallID)
			assert.Error(t, err)
			require.NoError(t, db.Model(&Call{}).Where("call_id = ?", row.CallID).Update("evidence_conflict", false).Error)
			require.NoError(t, db.Model(&knowledgeResultRow{}).Where("call_id = ?", row.CallID).Update("expires_at", clock.Add(-time.Minute).UnixMilli()).Error)
			_, err = v.readSettled(context.Background(), row.CallID)
			assert.Error(t, err)
			_, _, err = v.purgeExpired(context.Background(), 10, "")
			assert.Error(t, err)
			require.NoError(t, db.First(&stored, "call_id = ?", row.CallID).Error)
			assert.NotEmpty(t, stored.Ciphertext)
			stored.ExpiresAt = clock.Add(time.Hour).UnixMilli()
			require.NoError(t, db.Model(&knowledgeResultRow{}).Where("call_id = ?", row.CallID).Update("expires_at", stored.ExpiresAt).Error)
			stored.Ciphertext[len(stored.Ciphertext)-1] ^= 1
			require.NoError(t, db.Model(&knowledgeResultRow{}).Where("call_id = ?", row.CallID).Update("ciphertext", stored.Ciphertext).Error)
			_, err = v.readSettled(context.Background(), row.CallID)
			assert.Error(t, err)
			stored.Ciphertext[len(stored.Ciphertext)-1] ^= 1
			require.NoError(t, db.Model(&knowledgeResultRow{}).Where("call_id = ?", row.CallID).Update("ciphertext", stored.Ciphertext).Error)
			require.NoError(t, db.Model(&Call{}).Where("call_id = ?", row.CallID).Update("account_id", "attacker").Error)
			_, err = v.readSettled(context.Background(), row.CallID)
			assert.Error(t, err)
			_, err = newKnowledgeResultVault(db, []byte("different-key-different-key-1234"), time.Hour)
			assert.Error(t, err)
			require.NoError(t, db.Model(&Call{}).Where("call_id = ?", row.CallID).Update("account_id", "payer").Error)
			clock = clock.Add(2 * time.Hour)
			count, _, err := v.purgeExpired(context.Background(), 10, "")
			require.NoError(t, err)
			assert.EqualValues(t, 1, count)
			require.NoError(t, db.First(&stored, "call_id = ?", row.CallID).Error)
			assert.Empty(t, stored.Ciphertext)
			var callCount int64
			require.NoError(t, db.Model(&Call{}).Where("call_id = ?", row.CallID).Count(&callCount).Error)
			assert.EqualValues(t, 1, callCount)
			assert.Error(t, v.capture(context.Background(), row.CallID, result))
		})
	}
}

func TestKnowledgeResultPurgeAdvancesPastCorruption(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+t.TempDir()+"/purge.db"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&Call{}))
	v, err := newKnowledgeResultVault(db, []byte("0123456789abcdef0123456789abcdef"), time.Hour)
	require.NoError(t, err)
	now := time.Now()
	v.now = func() time.Time { return now }
	for _, id := range []string{"a", "b", "c"} {
		row := Call{CallID: id, Operation: "rerank", WireOperation: "rerank.create", Status: "UNKNOWN", Evidence: `{"version":"metered_v2","operation":"rerank","state":"confirmed"}`}
		require.NoError(t, db.Create(&row).Error)
		require.NoError(t, v.capture(context.Background(), id, []byte(`{"results":[]}`)))
	}
	require.NoError(t, db.Model(&knowledgeResultRow{}).Where("call_id = ?", "a").Update("expires_at", now.Add(-time.Minute).UnixMilli()).Error)
	now = now.Add(2 * time.Hour)
	count, cursor, err := v.purgeExpired(context.Background(), 1, "")
	assert.Error(t, err)
	assert.Zero(t, count)
	assert.Equal(t, "a", cursor)
	count, cursor, err = v.purgeExpired(context.Background(), 1, cursor)
	require.NoError(t, err)
	assert.EqualValues(t, 1, count)
	assert.Equal(t, "b", cursor)
	count, cursor, err = v.purgeExpired(context.Background(), 1, cursor)
	require.NoError(t, err)
	assert.EqualValues(t, 1, count)
	assert.Equal(t, "c", cursor)
	var rows []knowledgeResultRow
	require.NoError(t, db.Order("call_id").Find(&rows).Error)
	require.Len(t, rows, 3)
	assert.NotEmpty(t, rows[0].Ciphertext)
	assert.Empty(t, rows[1].Ciphertext)
	assert.Empty(t, rows[2].Ciphertext)
}
