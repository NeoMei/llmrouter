package common

import (
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBodyStorageNewReaderHasIndependentCursor(t *testing.T) {
	payload := []byte("independent-replay-body")
	for _, tc := range []struct {
		name string
		new  func(*testing.T) BodyStorage
	}{
		{name: "memory", new: func(t *testing.T) BodyStorage { return newMemoryStorage(payload) }},
		{name: "disk", new: func(t *testing.T) BodyStorage {
			storage, err := newDiskStorage(payload, t.TempDir())
			require.NoError(t, err)
			return storage
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			storage := tc.new(t)
			t.Cleanup(func() { _ = storage.Close() })
			a, err := storage.NewReader()
			require.NoError(t, err)
			defer a.Close()
			b, err := storage.NewReader()
			require.NoError(t, err)
			defer b.Close()

			one := make([]byte, 3)
			_, err = io.ReadFull(storage, one)
			require.NoError(t, err)
			assert.Equal(t, "ind", string(one))
			allA, err := io.ReadAll(a)
			require.NoError(t, err)
			allB, err := io.ReadAll(b)
			require.NoError(t, err)
			assert.Equal(t, payload, allA)
			assert.Equal(t, payload, allB)
		})
	}
}

func TestBodyStorageNewReaderRejectsClosedStorage(t *testing.T) {
	storage := newMemoryStorage([]byte("body"))
	replay, err := storage.NewReader()
	require.NoError(t, err)
	require.NoError(t, storage.Close())
	_, err = storage.NewReader()
	require.ErrorIs(t, err, ErrStorageClosed)
	got, err := io.ReadAll(replay)
	require.NoError(t, err)
	assert.Equal(t, "body", string(got))
	require.NoError(t, replay.Close())
}
