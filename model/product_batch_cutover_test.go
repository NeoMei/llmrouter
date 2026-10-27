package model

import (
	"context"
	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestProductCutoverRejectsUnflushedLegacyMoneyQueue(t *testing.T) {
	s, db, ledger := productFundingFixture(t)
	old := common.BatchUpdateEnabled
	common.BatchUpdateEnabled = true
	t.Cleanup(func() { common.BatchUpdateEnabled = old; resetBatchUpdateTestState(t) })
	resetBatchUpdateTestState(t)
	addNewRecord(BatchUpdateTypeUserQuota, 42, -40)
	require.Error(t, s.Import(context.Background(), 42, "payer", "reviewed"))
	require.Zero(t, ledger.imports)
	var u User
	require.NoError(t, db.First(&u, 42).Error)
	require.Equal(t, 500, u.Quota)
}
