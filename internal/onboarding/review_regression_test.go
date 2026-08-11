package onboarding

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/require"
)

func TestMergeJSONObjectPreservesLargeSiblingInteger(t *testing.T) {
	input := []byte(`{"large":9007199254740993,"nested":{"keep":true}}`)
	got, err := MergeJSONObject(input, []string{"nested", "llmapi"}, true)
	require.NoError(t, err)
	require.Contains(t, string(got), `"large":9007199254740993`)
}

func TestRestorePreloadsAllBackupsBeforeMutation(t *testing.T) {
	home := t.TempDir()
	first := filepath.Join(home, ".zshrc")
	second := filepath.Join(home, ".profile")
	require.NoError(t, os.WriteFile(first, []byte("first-original"), 0o600))
	require.NoError(t, os.WriteFile(second, []byte("second-original"), 0o600))
	tx, err := BeginTransaction(home)
	require.NoError(t, err)
	require.NoError(t, tx.Write(first, []byte("first-configured")))
	require.NoError(t, tx.Write(second, []byte("second-configured")))
	receipt, err := tx.Commit(ReceiptMeta{})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(receipt.Files[1].BackupPath, []byte("corrupt"), 0o600))
	firstApplied := receipt.Files[0].Path
	require.ErrorContains(t, Restore(home, receipt.ID), "backup is unavailable")
	require.Equal(t, string(receipt.Files[0].ConfiguredHash), digest(mustRead(t, firstApplied)))
}

func TestRestoreAndUninstallAreIdempotent(t *testing.T) {
	home := t.TempDir()
	target := filepath.Join(home, ".zshrc")
	require.NoError(t, os.WriteFile(target, []byte("original"), 0o600))
	tx, err := BeginTransaction(home)
	require.NoError(t, err)
	require.NoError(t, tx.Write(target, []byte("configured")))
	receipt, err := tx.Commit(ReceiptMeta{})
	require.NoError(t, err)
	require.NoError(t, Restore(home, receipt.ID))
	require.NoError(t, Restore(home, receipt.ID))

	tx, err = BeginTransaction(home)
	require.NoError(t, err)
	require.NoError(t, tx.Write(target, []byte("configured-again")))
	receipt, err = tx.Commit(ReceiptMeta{})
	require.NoError(t, err)
	require.NoError(t, Uninstall(home, receipt.ID))
	require.NoError(t, Uninstall(home, receipt.ID))
}

func TestTransactionRejectsStateRootDestination(t *testing.T) {
	home := t.TempDir()
	tx, err := BeginTransaction(home)
	require.NoError(t, err)
	require.ErrorContains(t, tx.Write(filepath.Join(home, ".llmapi", "credentials", "receipt.key"), []byte("x")), "managed destination")
}

func TestLoadReceiptRejectsTamperedReceiptAndNonCanonicalBackup(t *testing.T) {
	home := t.TempDir()
	target := filepath.Join(home, ".zshrc")
	require.NoError(t, os.WriteFile(target, []byte("original"), 0o600))
	tx, err := BeginTransaction(home)
	require.NoError(t, err)
	require.NoError(t, tx.Write(target, []byte("configured")))
	receipt, err := tx.Commit(ReceiptMeta{})
	require.NoError(t, err)

	raw := mustRead(t, receipt.Path)
	var forged Receipt
	require.NoError(t, common.Unmarshal(raw, &forged))
	forged.Files[0].BackupPath = filepath.Join(home, "alternate")
	key, err := receiptKey(home, false)
	require.NoError(t, err)
	forged.Integrity = receiptMAC(key, forged)
	forgedRaw, err := common.Marshal(forged)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(receipt.Path, forgedRaw, 0o600))
	require.ErrorContains(t, Restore(home, receipt.ID), "backup path is invalid")

	require.NoError(t, os.WriteFile(receipt.Path, raw, 0o600))
	raw = bytes.Replace(raw, []byte(`"status":"active"`), []byte(`"status":"restored"`), 1)
	require.NoError(t, os.WriteFile(receipt.Path, raw, 0o600))
	require.ErrorContains(t, Restore(home, receipt.ID), "integrity")
}

func TestSecureReadHoldsNoFollowParentHandle(t *testing.T) {
	home := t.TempDir()
	config := filepath.Join(home, ".config")
	moved := filepath.Join(home, ".config-held")
	alternate := filepath.Join(home, "alternate")
	require.NoError(t, os.Mkdir(config, 0o700))
	require.NoError(t, os.Mkdir(alternate, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(config, "client.json"), []byte("safe"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(alternate, "client.json"), []byte("evil"), 0o600))
	setSecureFSTestHook(func() {
		require.NoError(t, os.Rename(config, moved))
		require.NoError(t, os.Symlink(alternate, config))
	})
	t.Cleanup(func() { setSecureFSTestHook(nil) })
	got, err := secureReadFile(home, filepath.Join(config, "client.json"))
	require.NoError(t, err)
	require.Equal(t, "safe", string(got))
}

func TestBeginTransactionRejectsSuppliedHomeSymlink(t *testing.T) {
	realHome := t.TempDir()
	link := filepath.Join(t.TempDir(), "home")
	require.NoError(t, os.Symlink(realHome, link))
	_, err := BeginTransaction(link)
	require.ErrorContains(t, err, "home directory")
}

func TestLoadReceiptRejectsDuplicateTargets(t *testing.T) {
	home := t.TempDir()
	target := filepath.Join(home, ".zshrc")
	require.NoError(t, os.WriteFile(target, []byte("original"), 0o600))
	tx, err := BeginTransaction(home)
	require.NoError(t, err)
	require.NoError(t, tx.Write(target, []byte("configured")))
	receipt, err := tx.Commit(ReceiptMeta{})
	require.NoError(t, err)

	key, err := receiptKey(receipt.Home, false)
	require.NoError(t, err)
	receipt.Files = append(receipt.Files, ReceiptFile{
		Path: receipt.Files[0].Path, OriginalHash: receipt.Files[0].OriginalHash, ConfiguredHash: receipt.Files[0].ConfiguredHash,
		BackupPath: canonicalBackupPath(receipt.Home, receipt.ID, 1), Existed: true,
	})
	receipt.Integrity = receiptMAC(key, receipt)
	raw, err := common.Marshal(receipt)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(receipt.Path, raw, 0o600))
	require.ErrorContains(t, Restore(home, receipt.ID), "duplicate targets")
}
