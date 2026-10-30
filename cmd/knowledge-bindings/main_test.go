package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestReadPrivateApprovalRejectsGroupReadableAndSymlink(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "approval.json")
	require.NoError(t, os.WriteFile(path, []byte(`{"schema":1}`), 0600))
	data, err := readPrivateApproval(path)
	require.NoError(t, err)
	require.Equal(t, `{"schema":1}`, string(data))
	require.NoError(t, os.Chmod(path, 0644))
	_, err = readPrivateApproval(path)
	require.Error(t, err)
	require.NoError(t, os.Chmod(path, 0600))
	link := filepath.Join(dir, "link.json")
	require.NoError(t, os.Symlink(path, link))
	_, err = readPrivateApproval(link)
	require.Error(t, err)
}

func TestOpenDatabaseAcceptsOnlySupportedSchemes(t *testing.T) {
	db, kind, err := openDatabase("sqlite:" + filepath.Join(t.TempDir(), "db.sqlite"))
	require.NoError(t, err)
	require.Equal(t, "sqlite", string(kind))
	sqlDB, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, sqlDB.Close())
	_, _, err = openDatabase("oracle://example")
	require.Error(t, err)
}
