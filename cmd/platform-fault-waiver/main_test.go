package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestWaiverOperatorRejectsNonRootBeforeOpeningDatabase(t *testing.T) {
	require.Error(t, run(nil, func(string) string { t.Fatal("read environment before root check"); return "" }, 501))
}

func TestWaiverOperatorPrivateApprovalRejectsReadableAndSymlink(t *testing.T) {
	path := filepath.Join(t.TempDir(), "approval.json")
	require.NoError(t, os.WriteFile(path, []byte(`{"schema":1}`), 0600))
	data, err := readPrivateApproval(path)
	require.NoError(t, err)
	require.Equal(t, `{"schema":1}`, string(data))
	require.NoError(t, os.Chmod(path, 0644))
	_, err = readPrivateApproval(path)
	require.Error(t, err)
	require.NoError(t, os.Chmod(path, 0600))
	require.NoError(t, os.Symlink(path, path+".link"))
	_, err = readPrivateApproval(path + ".link")
	require.Error(t, err)
}

func TestWaiverOperatorRejectsApplyWithoutExplicitMode(t *testing.T) {
	require.Error(t, run([]string{"--apply"}, func(string) string { return "" }, 0))
}
