package model

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestAPIFileOwnershipAndLifecycle(t *testing.T) {
	require.NoError(t, DB.AutoMigrate(&APIFile{}))
	t.Cleanup(func() { DB.Exec("DELETE FROM api_files") })

	file := &APIFile{
		ID: "file-owned", TenantID: "tenant-a", UserID: 7, Purpose: "fine-tune",
		Filename: "training.jsonl", Bytes: 12, ChecksumSHA256: "checksum",
		MediaType: "application/jsonl", StorageKey: "tenant-a/7/file-owned", Status: APIFileStatusReady,
	}
	require.NoError(t, CreateAPIFile(file))

	owned, err := GetOwnedAPIFile("tenant-a", 7, file.ID)
	require.NoError(t, err)
	assert.Equal(t, file.Filename, owned.Filename)

	_, err = GetOwnedAPIFile("tenant-b", 7, file.ID)
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
	_, err = GetOwnedAPIFile("tenant-a", 8, file.ID)
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)

	files, err := ListAPIFiles("tenant-a", 7, "", 20)
	require.NoError(t, err)
	require.Len(t, files, 1)

	deleted, err := MarkAPIFileDeleted("tenant-a", 7, file.ID)
	require.NoError(t, err)
	assert.True(t, deleted)
	_, err = GetOwnedAPIFile("tenant-a", 7, file.ID)
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
}

func TestListAPIFilesUsesStableIDCursor(t *testing.T) {
	require.NoError(t, DB.AutoMigrate(&APIFile{}))
	DB.Exec("DELETE FROM api_files")
	t.Cleanup(func() { DB.Exec("DELETE FROM api_files") })

	for _, id := range []string{"file-003", "file-002", "file-001"} {
		require.NoError(t, CreateAPIFile(&APIFile{ID: id, TenantID: "tenant", UserID: 1, Status: APIFileStatusReady, StorageKey: id}))
	}
	first, err := ListAPIFiles("tenant", 1, "", 2)
	require.NoError(t, err)
	require.Len(t, first, 2)
	assert.Equal(t, "file-003", first[0].ID)
	assert.Equal(t, "file-002", first[1].ID)

	second, err := ListAPIFiles("tenant", 1, first[1].ID, 2)
	require.NoError(t, err)
	require.Len(t, second, 1)
	assert.Equal(t, "file-001", second[0].ID)
}
