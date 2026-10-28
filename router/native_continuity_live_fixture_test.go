package router

import (
	"github.com/joho/godotenv"
	"github.com/stretchr/testify/require"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// This subprocess uses the exact selected route fixture with real UniHub HTTP.
// Its synthetic supplier keeps a fixed loopback authority across gateway restarts.
func TestNativeContinuityLocalLiveFixture(t *testing.T) {
	if os.Getenv("NATIVE_CONTINUITY_E2E") != "1" {
		t.Skip("explicit isolated continuity acceptance fixture")
	}
	dir := os.Getenv("NATIVE_CONTINUITY_DIR")
	require.True(t, filepath.IsAbs(dir))
	values, e := godotenv.Read(filepath.Join(dir, "fixture.env"))
	require.NoError(t, e)
	addr := "127.0.0.1:0"
	if previous, e := os.ReadFile(filepath.Join(dir, "supplier.addr")); e == nil {
		addr = string(previous)
	}
	f := newNativeContinuityFixture(t, false, false, map[string]string{"database": filepath.Join(dir, "gateway.db"), "supplier": addr, "origin": os.Getenv("UNIHUB_PLATFORM_E2E_ORIGIN"), "service": values["MODEL_SERVICE_TOKEN"]})
	require.NoError(t, os.WriteFile(filepath.Join(dir, "supplier.addr"), []byte(strings.TrimPrefix(f.supplier.URL, "http://")), 0600))
	ready := os.Getenv("NATIVE_CONTINUITY_READY")
	require.True(t, filepath.IsAbs(ready))
	require.NoError(t, os.WriteFile(ready, []byte(f.gateway.URL), 0600))
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGTERM, syscall.SIGINT)
	defer signal.Stop(stop)
	<-stop
}
