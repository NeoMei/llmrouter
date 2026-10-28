package continuityindex

import (
	"context"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	mysqlgorm "gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// Only the coordinator-created isolated schemas are permitted. No deployment or
// shared fixture schema is accepted, even with the explicit opt-in flag.
func TestDedicatedDatabaseOwnership(t *testing.T) {
	if os.Getenv("NATIVE_INDEX_DISPOSABLE") != "1" {
		t.Skip("dedicated database opt-in required")
	}
	for _, dialect := range []string{"postgres", "mysql57", "mysql84"} {
		t.Run(dialect, func(t *testing.T) {
			dsn := os.Getenv("NATIVE_INDEX_" + strings.ToUpper(dialect) + "_DSN")
			require.NotEmpty(t, dsn, "dedicated DSN required")
			if dialect == "postgres" {
				u, err := url.Parse(dsn)
				require.True(t, err == nil, "invalid private database URL")
				require.Equal(t, "sw_continuity_3d3a", u.Query().Get("search_path"))
				require.Equal(t, "127.0.0.1", u.Hostname())
			} else {
				parsed, err := mysql.ParseDSN(dsn)
				require.True(t, err == nil, "invalid private database DSN")
				require.Equal(t, "sw_continuity_3d3a", parsed.DBName)
				require.True(t, strings.HasPrefix(parsed.Addr, "127.0.0.1:"))
			}
			open := func() *gorm.DB {
				var driver gorm.Dialector
				if dialect == "postgres" {
					driver = postgres.Open(dsn)
				} else {
					driver = mysqlgorm.Open(dsn)
				}
				db, err := gorm.Open(driver, &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
				require.True(t, err == nil, "dedicated connection failed")
				sql, err := db.DB()
				require.NoError(t, err)
				sql.SetMaxOpenConns(4)
				t.Cleanup(func() { _ = sql.Close() })
				return db
			}
			ctx := context.Background()
			db := open()
			var version string
			require.NoError(t, db.Raw("SELECT version()").Scan(&version).Error)
			t.Logf("database version: %s", version)
			if dialect == "mysql57" {
				require.True(t, strings.HasPrefix(version, "5.7."))
			}
			if dialect == "mysql84" {
				require.True(t, strings.HasPrefix(version, "8.4."))
			}
			require.NoError(t, db.Migrator().DropTable(&referenceRow{}, &capacityGate{}))
			cfg := testConfig()
			cfg.Capacity = 2
			idx, err := New(db, cfg)
			require.NoError(t, err)
			require.NoError(t, idx.Migrate(ctx))
			require.NoError(t, idx.Migrate(ctx))
			c := completion()
			require.NoError(t, idx.Register(ctx, c))
			require.NoError(t, idx.Register(ctx, c))
			sql, err := db.DB()
			require.NoError(t, err)
			require.NoError(t, sql.Close())
			db = open()
			idx, err = New(db, cfg)
			require.NoError(t, err)
			require.NoError(t, idx.Migrate(ctx))
			got, err := idx.Resolve(ctx, c.Owner, selectors(c))
			require.NoError(t, err)
			assert.Equal(t, "root-a", got.RootTaskID)
			conflict := c
			conflict.Owner.ActorID = "other"
			conflict.ResponseID = "would-be-partial"
			conflict.OutputItemIDs = []string{c.OutputItemIDs[0]}
			assert.ErrorIs(t, idx.Register(ctx, conflict), ErrConflict)
			_, err = idx.Resolve(ctx, conflict.Owner, []Selector{{Response, conflict.ResponseID}})
			assert.ErrorIs(t, err, ErrUnavailable)
			// Restarted clients sharing the database must agree on capacity policy.
			mismatch := cfg
			mismatch.Capacity = 3
			other, err := New(db, mismatch)
			require.NoError(t, err)
			assert.ErrorIs(t, other.Migrate(ctx), ErrConfig)
			// Fresh dedicated tables, two independent connections, a barrier and one
			// capacity slot for one complete two-row completion. No timing assertions.
			require.NoError(t, db.Migrator().DropTable(&referenceRow{}, &capacityGate{}))
			require.NoError(t, idx.Migrate(ctx))
			second, err := New(open(), cfg)
			require.NoError(t, err)
			require.NoError(t, second.Migrate(ctx))
			start := make(chan struct{})
			results := make(chan error, 2)
			a := completion()
			b := completion()
			b.ResponseID = "response-b"
			b.OutputItemIDs = []string{"item-b"}
			b.Provenance.CallID = "call-b"
			go func() { <-start; results <- idx.Register(ctx, a) }()
			go func() { <-start; results <- second.Register(ctx, b) }()
			close(start)
			errs := []error{<-results, <-results}
			successes, capacity := 0, 0
			for _, err := range errs {
				if err == nil {
					successes++
				} else if err == ErrCapacity {
					capacity++
				} else {
					t.Errorf("unexpected registration class: %v", err)
				}
			}
			assert.Equal(t, 1, successes)
			assert.Equal(t, 1, capacity)
			resolved := 0
			for _, candidate := range []TrustedCompletion{a, b} {
				got, err := idx.Resolve(ctx, candidate.Owner, selectors(candidate))
				if err == nil {
					resolved++
					assert.Len(t, got.Bindings, 2)
				} else {
					assert.ErrorIs(t, err, ErrUnavailable)
					for _, s := range selectors(candidate) {
						_, err = idx.Resolve(ctx, candidate.Owner, []Selector{s})
						assert.ErrorIs(t, err, ErrUnavailable)
					}
				}
			}
			assert.Equal(t, 1, resolved)
			// Same provider resource namespace/ID cannot be claimed by two owners.
			require.NoError(t, db.Migrator().DropTable(&referenceRow{}, &capacityGate{}))
			require.NoError(t, idx.Migrate(ctx))
			b = a
			b.Owner.ActorID = "foreign"
			b.Provenance.CallID = "foreign-call"
			start = make(chan struct{})
			results = make(chan error, 2)
			go func() { <-start; results <- idx.Register(ctx, a) }()
			go func() { <-start; results <- second.Register(ctx, b) }()
			close(start)
			errs = []error{<-results, <-results}
			successes, conflicts := 0, 0
			for _, err := range errs {
				if err == nil {
					successes++
				} else if err == ErrConflict {
					conflicts++
				} else {
					t.Errorf("unexpected collision class: %v", err)
				}
			}
			assert.Equal(t, 1, successes)
			assert.Equal(t, 1, conflicts)
		})
	}
}
