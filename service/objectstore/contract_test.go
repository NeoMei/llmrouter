package objectstore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func runStoreContract(t *testing.T, store Store) {
	t.Helper()
	ctx := context.Background()
	payload := []byte("gateway object store contract")
	sum := sha256.Sum256(payload)
	checksum := hex.EncodeToString(sum[:])

	require.NoError(t, store.Health(ctx))
	info, err := store.Put(ctx, "tenant/user/file-1", bytes.NewReader(payload), PutOptions{
		ContentType:    "text/plain",
		Size:           int64(len(payload)),
		ChecksumSHA256: checksum,
	})
	require.NoError(t, err)
	assert.Equal(t, int64(len(payload)), info.Size)
	assert.Equal(t, "text/plain", info.ContentType)

	body, opened, err := store.Open(ctx, "tenant/user/file-1")
	require.NoError(t, err)
	defer body.Close()
	actual, err := io.ReadAll(body)
	require.NoError(t, err)
	assert.Equal(t, payload, actual)
	assert.Equal(t, info.Size, opened.Size)

	require.NoError(t, store.Delete(ctx, "tenant/user/file-1"))
	_, _, err = store.Open(ctx, "tenant/user/file-1")
	require.Error(t, err)
}

func TestLocalStoreContract(t *testing.T) {
	store, err := NewLocal(t.TempDir())
	require.NoError(t, err)
	runStoreContract(t, store)
}

func TestLocalStoreRejectsTraversalAndChecksumMismatch(t *testing.T) {
	root := t.TempDir()
	store, err := NewLocal(root)
	require.NoError(t, err)

	_, err = store.Put(context.Background(), "../escape", bytes.NewBufferString("x"), PutOptions{Size: 1})
	require.ErrorContains(t, err, "invalid object key")
	_, err = os.Stat(root + "/../escape")
	assert.Error(t, err)

	_, err = store.Put(context.Background(), "safe/file", bytes.NewBufferString("x"), PutOptions{
		Size: 1, ChecksumSHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	})
	require.ErrorContains(t, err, "checksum")
	_, _, err = store.Open(context.Background(), "safe/file")
	assert.Error(t, err)
}

func TestS3StoreContract(t *testing.T) {
	endpoint := os.Getenv("TEST_S3_ENDPOINT")
	if endpoint == "" {
		t.Skip("TEST_S3_ENDPOINT is not configured")
	}
	store, err := NewS3(S3Config{
		Endpoint:        endpoint,
		Region:          os.Getenv("TEST_S3_REGION"),
		Bucket:          os.Getenv("TEST_S3_BUCKET"),
		AccessKeyID:     os.Getenv("TEST_S3_ACCESS_KEY_ID"),
		SecretAccessKey: os.Getenv("TEST_S3_SECRET_ACCESS_KEY"),
		UsePathStyle:    true,
	})
	require.NoError(t, err)
	runStoreContract(t, store)
}
