package router

import (
	"context"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// Explicit test-binary fixture only: never compiled into the product server.
func TestAudioLocalLiveFixture(t *testing.T) {
	if os.Getenv("UNIHUB_AUDIO_E2E") != "1" {
		t.Skip("explicit isolated audio acceptance fixture")
	}
	dir := os.Getenv("UNIHUB_AUDIO_E2E_DIR")
	require.True(t, filepath.IsAbs(dir))
	raw, e := os.ReadFile(filepath.Join(dir, "service-token"))
	require.NoError(t, e)
	f := newAudioGatewayFixture(t, os.Getenv("UNIHUB_AUDIO_KIND"), os.Getenv("UNIHUB_AUDIO_CASE"), os.Getenv("UNIHUB_AUDIO_ORIGIN"), string(raw), filepath.Join(dir, "gateway.db"))
	f.engine.GET("/__fixture/status", func(c *gin.Context) {
		var user model.User
		require.NoError(t, f.db.First(&user, 1).Error)
		var native model.User
		require.NoError(t, f.db.First(&native, 2).Error)
		var token model.Token
		require.NoError(t, f.db.First(&token, 1).Error)
		c.JSON(200, gin.H{"provider_calls": f.sends, "quota": user.Quota, "used_quota": user.UsedQuota, "native_quota": native.Quota, "native_token_quota": token.RemainQuota})
	})
	listener, e := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, e)
	server := &http.Server{Handler: f.engine}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "ready"), []byte("http://"+listener.Addr().String()), 0600))
	signalCh := make(chan os.Signal, 1)
	signal.Notify(signalCh, syscall.SIGTERM, syscall.SIGINT)
	defer signal.Stop(signalCh)
	<-signalCh
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, server.Shutdown(ctx))
	require.ErrorIs(t, <-done, http.ErrServerClosed)
	common.MemoryCacheEnabled = false
}
