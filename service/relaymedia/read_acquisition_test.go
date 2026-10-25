package relaymedia

import (
	"context"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service/objectstore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"io"
	"sync/atomic"
	"testing"
	"time"
)

type blockedMediaOpen struct {
	objectstore.Store
	entered, release chan struct{}
}

func (b *blockedMediaOpen) Open(ctx context.Context, key string) (io.ReadCloser, objectstore.ObjectInfo, error) {
	close(b.entered)
	select {
	case <-b.release:
		return b.Store.Open(ctx, key)
	case <-ctx.Done():
		return nil, objectstore.ObjectInfo{}, ctx.Err()
	}
}

type mediaUnrelatedRow struct {
	ID    int `gorm:"primaryKey"`
	Value int
}

func TestRelayMediaAcquisitionDoesNotHoldSQLiteTransaction(t *testing.T) {
	for _, change := range []string{"none", "revoke", "expire", "delete"} {
		t.Run(change, func(t *testing.T) {
			s, data := fixtureService(t)
			p := prepareFixture(t, s, data)
			token := tokenOf(t, p)
			require.NoError(t, s.db.AutoMigrate(&mediaUnrelatedRow{}))
			require.NoError(t, s.db.Create(&mediaUnrelatedRow{ID: 1}).Error)
			writer, err := gorm.Open(s.db.Dialector, &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
			require.NoError(t, err)
			sql, err := writer.DB()
			require.NoError(t, err)
			defer sql.Close()
			sql.SetMaxOpenConns(1)
			require.NoError(t, writer.Exec("PRAGMA busy_timeout = 1").Error)
			var mode string
			require.NoError(t, writer.Raw("PRAGMA journal_mode").Scan(&mode).Error)
			require.Equal(t, "delete", mode)
			blocked := &blockedMediaOpen{Store: s.objects, entered: make(chan struct{}), release: make(chan struct{})}
			s.objects = blocked
			var clock atomic.Int64
			clock.Store(time.Now().Unix())
			s.now = func() time.Time { return time.Unix(clock.Load(), 0) }
			type result struct {
				data []byte
				err  error
			}
			done := make(chan result, 1)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			go func() { b, e := s.ReadGranted(ctx, p.ID, token); done <- result{b, e} }()
			select {
			case <-blocked.entered:
			case <-time.After(5 * time.Second):
				t.Fatal("Open did not start")
			}
			// The channel proves Open is pending; a 1ms busy timeout observes a lock
			// directly, never infers concurrency correctness from elapsed time.
			writeErr := writer.Model(&mediaUnrelatedRow{}).Where("id = ?", 1).Update("value", 1).Error
			var changeErr error
			switch change {
			case "revoke", "delete":
				changeErr = model.RevokeRelayMedia(context.Background(), writer, p.ID, 11, 22)
				if change == "delete" && changeErr == nil {
					changeErr = model.PurgeRelayMedia(context.Background(), writer, clock.Load(), 100, blocked.Store.Delete)
				}
			case "expire":
				clock.Store(p.GrantExpiresAt.Unix())
			}
			close(blocked.release)
			require.NoError(t, writeErr, "unrelated writer must commit while Store.Open is pending")
			require.NoError(t, changeErr)
			select {
			case got := <-done:
				if change == "none" {
					require.NoError(t, got.err)
					assert.Equal(t, data, got.data)
				} else {
					require.Error(t, got.err)
					assert.Empty(t, got.data, "acquisition revoked/expired/deleted before final check")
				}
			case <-time.After(5 * time.Second):
				t.Fatal("read did not finish")
			}
			var row mediaUnrelatedRow
			require.NoError(t, writer.First(&row, 1).Error)
			assert.Equal(t, 1, row.Value)
		})
	}
}
