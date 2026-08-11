package onboarding

import (
	"github.com/stretchr/testify/require"
	"os"
	"path/filepath"
	"testing"
)

func TestTransactionRollbackAndConflict(t *testing.T) {
	home := t.TempDir()
	target := filepath.Join(home, ".zshrc")
	require.NoError(t, os.WriteFile(target, []byte("original\n"), 0o600))
	tx, err := BeginTransaction(home)
	require.NoError(t, err)
	require.NoError(t, tx.Write(target, []byte("configured\n")))
	receipt, err := tx.Commit(ReceiptMeta{Clients: []ClientID{ClientClaudeCode}})
	require.NoError(t, err)
	require.NotContains(t, string(mustRead(t, receipt.Path)), "secret")
	require.NoError(t, os.WriteFile(target, []byte("manual change\n"), 0o600))
	require.ErrorContains(t, Restore(home, receipt.ID), "changed since configuration")
}

func TestTransactionRejectsDestinationSymlink(t *testing.T) {
	home := t.TempDir()
	real := filepath.Join(home, "real")
	link := filepath.Join(home, ".zshrc")
	require.NoError(t, os.WriteFile(real, nil, 0o600))
	require.NoError(t, os.Symlink(real, link))
	tx, err := BeginTransaction(home)
	require.NoError(t, err)
	require.ErrorContains(t, tx.Write(link, []byte("x")), "symlink")
}

func TestTransactionRestoresOriginalAndDeletesNewFile(t *testing.T) {
	home := t.TempDir()
	original := filepath.Join(home, ".profile")
	added := filepath.Join(home, ".config", "aider.conf")
	require.NoError(t, os.WriteFile(original, []byte("original"), 0o600))
	tx, err := BeginTransaction(home)
	require.NoError(t, err)
	require.NoError(t, tx.Write(original, []byte("configured")))
	require.NoError(t, tx.Write(added, []byte("new")))
	receipt, err := tx.Commit(ReceiptMeta{})
	require.NoError(t, err)
	require.NoError(t, Restore(home, receipt.ID))
	require.Equal(t, "original", string(mustRead(t, original)))
	_, err = os.Stat(added)
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestTransactionRejectsOutsideHome(t *testing.T) {
	tx, err := BeginTransaction(t.TempDir())
	require.NoError(t, err)
	require.ErrorContains(t, tx.Write(filepath.Join(t.TempDir(), "outside"), []byte("x")), "outside home")
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	return data
}

func TestRestoreRejectsBackupSymlink(t *testing.T) {
	home := t.TempDir()
	target := filepath.Join(home, ".zshrc")
	require.NoError(t, os.WriteFile(target, []byte("original"), 0o600))
	tx, err := BeginTransaction(home)
	require.NoError(t, err)
	require.NoError(t, tx.Write(target, []byte("configured")))
	receipt, err := tx.Commit(ReceiptMeta{})
	require.NoError(t, err)

	alternate := filepath.Join(home, "alternate")
	require.NoError(t, os.WriteFile(alternate, []byte("original"), 0o600))
	require.NoError(t, os.Remove(receipt.Files[0].BackupPath))
	require.NoError(t, os.Symlink(alternate, receipt.Files[0].BackupPath))
	require.ErrorContains(t, Restore(home, receipt.ID), "symlink")
	require.Equal(t, "configured", string(mustRead(t, target)))
}
