package model

import (
	"context"
	"errors"
	"github.com/glebarez/sqlite"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRelayMediaConfiguredDatabases(t *testing.T) {
	for _, tc := range []struct {
		name, env string
		open      func(string) gorm.Dialector
	}{{"sqlite", "", sqlite.Open}, {"mysql57", "LLMROUTER_ACCOUNT_POOL_MYSQL_DSN", mysql.Open}, {"postgres96", "LLMROUTER_ACCOUNT_POOL_POSTGRES_DSN", func(d string) gorm.Dialector {
		return postgres.New(postgres.Config{DSN: d, PreferSimpleProtocol: true})
	}}} {
		t.Run(tc.name, func(t *testing.T) {
			dsn := os.Getenv(tc.env)
			if tc.env != "" && dsn == "" {
				t.Skip(tc.env + " not configured")
			}
			if tc.env == "" {
				dsn = filepath.Join(t.TempDir(), "media.db")
			}
			db, err := gorm.Open(tc.open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
			require.NoError(t, err)
			sql, err := db.DB()
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, sql.Close()) })
			require.NoError(t, db.AutoMigrate(&RelayMediaResource{}))
			require.NoError(t, db.AutoMigrate(&RelayMediaResource{}))
			request := uuid.NewString()
			now := time.Now().Unix()
			r := RelayMediaResource{ID: uuid.NewString(), UserID: 11, TokenID: 22, RequestID: request, ObjectKey: "fixture/" + request, Format: "mp4", Size: 100, SHA256: "hash", State: "staging", ExpiresAt: now + 3600}
			require.NoError(t, db.Create(&r).Error)
			t.Cleanup(func() { require.NoError(t, db.Where("request_id = ?", request).Delete(&RelayMediaResource{}).Error) })
			require.NoError(t, RelayMediaReady(context.Background(), db, r.ID, now))
			require.ErrorIs(t, RelayMediaReady(context.Background(), db, r.ID, now), ErrRelayMediaUnavailable)
			require.Error(t, RevokeRelayMedia(context.Background(), db, r.ID, 99, 22))
			require.Error(t, RevokeRelayMedia(context.Background(), db, r.ID, 11, 99))
			require.NoError(t, ReadRelayMedia(context.Background(), db, r.ID, func(v RelayMediaResource) error {
				assert.Equal(t, "ready", v.State)
				assert.Equal(t, r.ExpiresAt, v.ExpiresAt)
				return nil
			}))
			// Scope all purge operations to this fixture's opaque request association.
			scoped := db.Scopes(func(tx *gorm.DB) *gorm.DB { return tx.Where("request_id = ?", request) })
			removed := false
			remove := func(context.Context, string) error { removed = true; return nil }
			require.NoError(t, PurgeRelayMedia(context.Background(), scoped, now, 100, remove))
			assert.False(t, removed)
			require.NoError(t, RevokeRelayMedia(context.Background(), db, r.ID, 11, 22))
			require.NoError(t, RevokeRelayMedia(context.Background(), db, r.ID, 11, 22))
			require.ErrorIs(t, RelayMediaReady(context.Background(), db, r.ID, now), ErrRelayMediaUnavailable)
			fail := errors.New("storage unavailable")
			require.ErrorIs(t, PurgeRelayMedia(context.Background(), scoped, now, 100, func(context.Context, string) error { return fail }), fail)
			require.NoError(t, db.First(&r, "id = ?", r.ID).Error)
			assert.Equal(t, "revoked", r.State)
			require.NoError(t, PurgeRelayMedia(context.Background(), scoped, now, 100, remove))
			assert.True(t, removed)
			require.ErrorIs(t, db.First(&r, "id = ?", r.ID).Error, gorm.ErrRecordNotFound)
			// Expired staging and ready rows are both collected, active rows survive.
			for _, state := range []string{"staging", "ready"} {
				r.ID = uuid.NewString()
				r.State = state
				r.ExpiresAt = now
				require.NoError(t, db.Create(&r).Error)
				require.ErrorIs(t, RelayMediaReady(context.Background(), db, r.ID, now), ErrRelayMediaUnavailable)
			}
			require.NoError(t, PurgeRelayMedia(context.Background(), scoped, now, 100, remove))
			var n int64
			require.NoError(t, db.Model(&RelayMediaResource{}).Where("request_id = ?", request).Count(&n).Error)
			assert.Zero(t, n)
		})
	}
}
