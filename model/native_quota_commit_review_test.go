package model

import (
	"context"
	"fmt"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNativeQuotaCommitFailureDoesNotLeaveUnownedDebit(t *testing.T) {
	for _, batch := range []bool{false, true} {
		t.Run(fmt.Sprintf("batch_%t", batch), func(t *testing.T) {
			truncateTables(t)
			resetBatchUpdateTestState(t)
			useUserCacheMiniRedis(t)
			common.BatchUpdateEnabled = batch
			token := createReserveTestToken(t, 100)
			require.NoError(t, DB.Model(&token).Update("auth_realm", NativeFundingLocalOnly).Error)
			enrollNativeQuotaFixture(t, token.Id)
			_, err := GetTokenByKey(token.Key, false)
			require.NoError(t, err)
			// The write succeeds; SQLite rejects COMMIT because the deferred FK is
			// unresolved. This exercises the transaction result, not a failed UPDATE.
			require.NoError(t, DB.Exec("PRAGMA foreign_keys = ON").Error)
			require.NoError(t, DB.Exec("CREATE TABLE quota_commit_parent (id INTEGER PRIMARY KEY)").Error)
			require.NoError(t, DB.Exec("CREATE TABLE quota_commit_child (parent_id INTEGER REFERENCES quota_commit_parent(id) DEFERRABLE INITIALLY DEFERRED)").Error)
			require.NoError(t, DB.Exec("CREATE TRIGGER quota_commit_failure AFTER UPDATE ON tokens BEGIN INSERT INTO quota_commit_child(parent_id) VALUES (987654); END").Error)
			t.Cleanup(func() {
				require.NoError(t, DB.Exec("DROP TRIGGER IF EXISTS quota_commit_failure").Error)
				require.NoError(t, DB.Exec("DROP TABLE IF EXISTS quota_commit_child").Error)
				require.NoError(t, DB.Exec("DROP TABLE IF EXISTS quota_commit_parent").Error)
			})
			ok, err := TryReserveTokenQuota(token.Id, token.Key, 60, false)
			// SQLite keeps a transaction open after a deferred-constraint COMMIT
			// rejection; end that definitely-uncommitted fixture transaction.
			if err != nil {
				_ = DB.Exec("ROLLBACK").Error
			}
			if !batch {
				require.Error(t, err, "synchronous persistence must report failed COMMIT")
			}
			expected := 100
			if err != nil {
				assert.False(t, ok)
			} else {
				require.True(t, ok)
				expected = 40
			}
			cached, cacheErr := common.RDB.HGet(context.Background(), getTokenCacheKey(token.Key), "RemainQuota").Int()
			require.NoError(t, cacheErr)
			assert.Equal(t, expected, cached, "failed admission must not retain a Redis debit")
			batchUpdateLocks[BatchUpdateTypeTokenQuota].Lock()
			pending := batchUpdateStores[BatchUpdateTypeTokenQuota][token.Id]
			batchUpdateLocks[BatchUpdateTypeTokenQuota].Unlock()
			expectedPending := 0

			assert.Equal(t, expectedPending, pending, "only an accepted admission owns a queued debit")
			require.NoError(t, DB.Exec("DROP TRIGGER quota_commit_failure").Error)
			batchUpdate()
			assert.Equal(t, expected, getTokenFromDB(t, token.Id).RemainQuota)
		})
	}
}
