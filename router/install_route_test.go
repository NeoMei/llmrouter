package router

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type installTestFS struct {
	http.FileSystem
}

func (f *installTestFS) Exists(prefix string, path string) bool {
	_, err := f.Open(path)
	return err == nil
}

func newOnboardingFS() *installTestFS {
	fsys := fstest.MapFS{
		"install":               {Mode: 0o755 | fs.ModeDir, Data: nil},
		"install/index.html":    {Mode: 0o644, Data: []byte("onboarding-index")},
		"install/manifest.json": {Mode: 0o644, Data: []byte(`{"schemaVersion":1}`)},
		"install/install.sh":    {Mode: 0o755, Data: []byte("#!/bin/sh")},
	}
	return &installTestFS{FileSystem: http.FS(fsys)}
}

// TestInstallRouteNotHijackedByTrailingSlashRedirect guards against a regression
// where relay wildcard routes (e.g. /:mode/mj/task/:id/fetch) make gin's
// RedirectTrailingSlash 301-redirect /install/ to /install before the onboarding
// static handler runs. SetWebRouter registers /install explicitly to keep the
// trailing-slash URL serving the onboarding index.html directly.
func TestInstallRouteNotHijackedByTrailingSlashRedirect(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()

	// A relay-style wildcard route that previously triggered the hijack.
	r.GET("/:mode/mj/task/:id/fetch", func(c *gin.Context) {
		c.Status(http.StatusOK)
	})

	// Register the onboarding directory exactly as SetWebRouter does.
	frontendFS := newOnboardingFS()
	installServer := http.FileServer(frontendFS)
	r.GET("/install", func(c *gin.Context) {
		installServer.ServeHTTP(c.Writer, c.Request)
	})
	r.GET("/install/*filepath", func(c *gin.Context) {
		installServer.ServeHTTP(c.Writer, c.Request)
	})

	// /install/ must serve the onboarding index directly (200), not 301 to
	// /install.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/install/", nil)
	r.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), "onboarding-index")

	// Sibling assets must remain reachable.
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/install/manifest.json", nil)
	r.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), `"schemaVersion"`)

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/install/install.sh", nil)
	r.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), "#!/bin/sh")
}
