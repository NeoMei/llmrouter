package router

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/internal/platformbilling"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func wanRecoveryDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.TempDir()+"/recovery.db"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&platformbilling.Call{}, &wanRecoveryLease{}))
	return db
}

func TestWanRecoverySettlesBoundTaskWithoutRedispatch(t *testing.T) {
	f := newImageGatewayFixture(t, "wan-edit", "", "", "")
	path, contentType, body := imageGatewayRequest(t, "wan-edit", f.img)
	require.Equal(t, http.StatusAccepted, f.request(t, path, contentType, body).Code)
	require.NoError(t, f.db.AutoMigrate(&wanRecoveryLease{}))
	ran, err := runWanRecoveryPass(context.Background(), f.db, "default", "worker", func(ctx context.Context, id string) error {
		return recoverWanImage(ctx, f.adapter, id)
	})
	require.NoError(t, err)
	require.True(t, ran)
	var row platformbilling.Call
	require.NoError(t, f.db.First(&row, "call_id = ?", "image-call").Error)
	assert.Equal(t, "SETTLED", row.Status)
	assert.NotEmpty(t, row.ImageObservation)
	assert.EqualValues(t, 1, f.sends.Load())
	assert.EqualValues(t, 1, f.downloads.Load())
}

func TestWanRecoveryPagesPastUnfinishedAndFailedRows(t *testing.T) {
	db := wanRecoveryDB(t)
	for i := 0; i < wanRecoveryBatch+2; i++ {
		require.NoError(t, db.Create(&platformbilling.Call{CallID: fmt.Sprintf("call-%02d", i), Operation: "image_edit", WireOperation: "images.edits.create", ProviderTaskID: "supplier", Status: "UNKNOWN", InstanceID: "default"}).Error)
	}
	require.NoError(t, db.Create(&platformbilling.Call{CallID: "other", Operation: "video", WireOperation: "videos.create", ProviderTaskID: "supplier", Status: "UNKNOWN", InstanceID: "default"}).Error)
	var visited []string
	poll := func(_ context.Context, id string) error {
		visited = append(visited, id)
		return errors.New("supplier temporarily unavailable")
	}
	for i := 0; i < 2; i++ {
		_, err := runWanRecoveryPass(context.Background(), db, "default", fmt.Sprintf("worker-%d", i), poll)
		require.NoError(t, err)
	}
	assert.Equal(t, []string{"call-00", "call-01", "call-02", "call-03"}, visited)
}

func TestWanRecoveryLeasePreventsCompetingWorkersAndExpires(t *testing.T) {
	db := wanRecoveryDB(t)
	require.NoError(t, db.Create(&platformbilling.Call{CallID: "call", Operation: "image_edit", WireOperation: "images.edits.create", ProviderTaskID: "supplier", Status: "UNKNOWN", InstanceID: "default"}).Error)
	entered, unblock := make(chan struct{}), make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, _ = runWanRecoveryPass(context.Background(), db, "default", "worker-a", func(context.Context, string) error { close(entered); <-unblock; return nil })
	}()
	<-entered
	ran, err := runWanRecoveryPass(context.Background(), db, "default", "worker-b", func(context.Context, string) error { t.Error("competing worker ran"); return nil })
	require.NoError(t, err)
	assert.False(t, ran)
	close(unblock)
	wg.Wait()
	// An abandoned lease can be taken over without a live owner's assistance.
	require.NoError(t, db.Model(&wanRecoveryLease{}).Where("id = ?", "default").Updates(map[string]any{"owner": "dead", "lease_until": time.Now().Add(-time.Second).UnixMilli(), "cursor": ""}).Error)
	count := 0
	ran, err = runWanRecoveryPass(context.Background(), db, "default", "worker-c", func(context.Context, string) error { count++; return nil })
	require.NoError(t, err)
	assert.True(t, ran)
	assert.Equal(t, 1, count)
}

func TestWanRecoveryStopsAndReleasesLeaseAfterCancellation(t *testing.T) {
	db := wanRecoveryDB(t)
	require.NoError(t, db.Create(&platformbilling.Call{CallID: "call", Operation: "image_edit", WireOperation: "images.edits.create", ProviderTaskID: "supplier", Status: "UNKNOWN", InstanceID: "default"}).Error)
	ctx, cancel := context.WithCancel(context.Background())
	entered, done := make(chan struct{}), make(chan error, 1)
	go func() {
		_, err := runWanRecoveryPass(ctx, db, "default", "worker-a", func(ctx context.Context, _ string) error { close(entered); <-ctx.Done(); return ctx.Err() })
		done <- err
	}()
	<-entered
	cancel()
	select {
	case err := <-done:
		assert.ErrorIs(t, err, context.Canceled)
	case <-time.After(3 * time.Second):
		t.Fatal("recovery did not stop")
	}
	ran, err := runWanRecoveryPass(context.Background(), db, "default", "worker-b", func(context.Context, string) error { return nil })
	require.NoError(t, err)
	assert.True(t, ran)
}

func TestWanRecoveryRemainsDisabledUntilCrossNodeFencingIsAvailable(t *testing.T) {
	f := newImageGatewayFixture(t, "wan-edit", "", "", "")
	require.False(t, f.db.Migrator().HasTable(&wanRecoveryLease{}))
	stop, err := StartWanImageRecovery(context.Background(), f.adapter, f.db, "default")
	require.NoError(t, err)
	defer stop()
	require.False(t, f.db.Migrator().HasTable(&wanRecoveryLease{}), "disabled worker must not migrate or start")
}
