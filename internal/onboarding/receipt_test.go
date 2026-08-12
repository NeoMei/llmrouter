package onboarding

import (
	"github.com/stretchr/testify/require"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
)

func TestReceiptContainsNoCredentialsAndUninstallRestores(t *testing.T) {
	home := t.TempDir()
	target := filepath.Join(home, ".config")
	tx, err := BeginTransaction(home)
	require.NoError(t, err)
	require.NoError(t, tx.Allow(target))
	require.NoError(t, tx.Write(target, []byte("configured")))
	receipt, err := tx.Commit(ReceiptMeta{Clients: []ClientID{ClientAider}})
	require.NoError(t, err)
	raw := mustRead(t, receipt.Path)
	require.NotContains(t, string(raw), "token")
	require.NoError(t, Uninstall(home, receipt.ID))
	_, err = os.Stat(target)
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestListReceiptsNewestFirst(t *testing.T) {
	home := t.TempDir()
	for _, filename := range []string{".one", ".two"} {
		tx, err := BeginTransaction(home)
		require.NoError(t, err)
		target := filepath.Join(home, filename)
		require.NoError(t, tx.Allow(target))
		require.NoError(t, tx.Write(target, []byte(filename)))
		_, err = tx.Commit(ReceiptMeta{})
		require.NoError(t, err)
	}
	receipts, err := ListReceipts(home)
	require.NoError(t, err)
	require.Len(t, receipts, 2)
	require.True(t, receipts[0].CreatedAt.After(receipts[1].CreatedAt) || receipts[0].CreatedAt.Equal(receipts[1].CreatedAt))
}

func TestRestoreRejectsReceiptFileOutsideHome(t *testing.T) {
	home := t.TempDir()
	canonical, err := canonicalHome(home)
	require.NoError(t, err)
	home = canonical
	outside := filepath.Join(t.TempDir(), "outside")
	receiptDir := filepath.Join(home, ".llmrouter", "receipts")
	receiptID := "forged"
	receipt := Receipt{
		ID: receiptID, Path: filepath.Join(receiptDir, receiptID+".json"), Home: home,
		Files: []ReceiptFile{{Path: outside, OriginalHash: digest(nil), ConfiguredHash: digest(nil), BackupPath: outside, Existed: false}},
	}
	receipt.CreatedAt = time.Now().UTC()
	require.NoError(t, ensureStateRoots(home))
	key, err := receiptKey(home, true)
	require.NoError(t, err)
	receipt.Integrity = receiptMAC(key, receipt)
	require.NoError(t, os.MkdirAll(receiptDir, 0o700))
	raw, err := common.Marshal(receipt)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(receipt.Path, raw, 0o600))
	require.ErrorContains(t, Restore(home, receiptID), "receipt file path is invalid")
}
