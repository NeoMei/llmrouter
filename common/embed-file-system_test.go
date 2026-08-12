package common

import (
	"io/fs"
	"net/http"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/require"
)

func newTestEmbedFS() *embedFileSystem {
	return &embedFileSystem{
		FileSystem: http.FS(fstest.MapFS{
			"install":               {Mode: 0o755 | fs.ModeDir, Data: nil},
			"install/index.html":    {Mode: 0o644, Data: []byte("onboarding")},
			"install/manifest.json": {Mode: 0o644, Data: []byte(`{"schemaVersion":1}`)},
			"install/install.sh":    {Mode: 0o755, Data: []byte("#!/bin/sh")},
		}),
	}
}

func TestEmbedFileSystemOpenNormalizesTrailingSlash(t *testing.T) {
	efs := newTestEmbedFS()
	// A trailing-slash directory path must resolve (this is what
	// gin-contrib/static.Exists uses for /install/).
	f, err := efs.Open("/install/")
	require.NoError(t, err)
	st, err := f.Stat()
	require.NoError(t, err)
	require.True(t, st.IsDir())
	f.Close()

	// A concrete file under the directory must still open.
	f, err = efs.Open("/install/index.html")
	require.NoError(t, err)
	buf := make([]byte, 16)
	n, err := f.Read(buf)
	require.NoError(t, err)
	require.Equal(t, "onboarding", string(buf[:n]))
	f.Close()
}

func TestEmbedFileSystemExistsDetectsDirectory(t *testing.T) {
	efs := newTestEmbedFS()
	require.True(t, efs.Exists("/", "/install/"))
	require.True(t, efs.Exists("/", "/install"))
	require.True(t, efs.Exists("/", "/install/manifest.json"))
	require.False(t, efs.Exists("/", "/missing"))
}
