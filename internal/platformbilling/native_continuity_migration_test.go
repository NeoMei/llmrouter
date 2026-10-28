package platformbilling

import (
	"bytes"
	"context"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	ci "github.com/QuantumNous/new-api/internal/platformbilling/continuityindex"
	"github.com/glebarez/sqlite"
	driver "github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestNativeContinuityMigration(t *testing.T) {
	for _, dialect := range []string{"sqlite", "postgres", "mysql57", "mysql84"} {
		t.Run(dialect, func(t *testing.T) {
			var dialector gorm.Dialector
			if dialect == "sqlite" {
				dialector = sqlite.Open(filepath.Join(t.TempDir(), "migration.db"))
			} else {
				if os.Getenv("NATIVE_INDEX_DISPOSABLE") != "1" {
					t.Skip("dedicated local database opt-in required")
				}
				dsn := os.Getenv("NATIVE_INDEX_" + strings.ToUpper(dialect) + "_DSN")
				require.NotEmpty(t, dsn)
				var bootstrap gorm.Dialector
				var target string
				if dialect == "postgres" {
					u, e := url.Parse(dsn)
					require.True(t, e == nil)
					require.Equal(t, "127.0.0.1", u.Hostname())
					require.Equal(t, "sw_continuity_3d3a", u.Query().Get("search_path"))
					bootstrap = postgres.Open(dsn)
					q := u.Query()
					q.Set("search_path", "sw_continuity_3d3b")
					u.RawQuery = q.Encode()
					target = u.String()
				} else {
					c, e := driver.ParseDSN(dsn)
					require.True(t, e == nil)
					require.Equal(t, "sw_continuity_3d3a", c.DBName)
					require.True(t, strings.HasPrefix(c.Addr, "127.0.0.1:"))
					bootstrap = mysql.Open(dsn)
					c.DBName = "sw_continuity_3d3b"
					target = c.FormatDSN()
				}
				db, e := gorm.Open(bootstrap, &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
				require.True(t, e == nil, "isolated bootstrap connection")
				if dialect == "postgres" {
					require.NoError(t, db.Exec("CREATE SCHEMA IF NOT EXISTS sw_continuity_3d3b").Error)
					dialector = postgres.Open(target)
				} else {
					require.NoError(t, db.Exec("CREATE DATABASE IF NOT EXISTS sw_continuity_3d3b").Error)
					dialector = mysql.Open(target)
				}
				sql, e := db.DB()
				require.NoError(t, e)
				require.NoError(t, sql.Close())
			}
			db, e := gorm.Open(dialector, &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
			require.True(t, e == nil, "isolated migration connection")
			sql, e := db.DB()
			require.NoError(t, e)
			t.Cleanup(func() { _ = sql.Close() })
			if dialect != "sqlite" {
				var version string
				require.NoError(t, db.Raw("SELECT version()").Scan(&version).Error)
				t.Logf("database version %s", version)
			}
			require.NoError(t, db.Migrator().DropTable("platform_native_history_references", "platform_native_history_capacity", &Attempt{}, &Call{}))
			type legacyCall struct {
				CallID                                                                                                                                                                                                    string `gorm:"primaryKey;size:128"`
				ActorID, AccountID, OriginProductID, CredentialID, CredentialKind, TaskID, Model, Digest, DigestVersion, WireOperation, InstanceID, Asset, FundingMode, FundingProductID, RateID, Amount, Status, Charged string
				BillingProfileVersion                                                                                                                                                                                     int
				Intent, Evidence, RateSnapshot, FundingSnapshot, MediaSnapshot                                                                                                                                            string `gorm:"type:text"`
			}
			require.NoError(t, db.Table("platform_model_calls").AutoMigrate(&legacyCall{}))
			original := legacyCall{CallID: "existing", ActorID: "actor", AccountID: "payer", OriginProductID: "superwagie", CredentialID: "credential", CredentialKind: "task", TaskID: "root", Model: "model", Digest: strings.Repeat("d", 64), DigestVersion: "platform-wire-v3", WireOperation: "responses.create", InstanceID: "default", Asset: "CNY/6", FundingMode: "prepaid", FundingProductID: "llmrouter", RateID: "rate", Amount: "500", Status: "SETTLE_PENDING", Charged: "15", BillingProfileVersion: 4, Intent: `{"usage":{"input":"10"}}`, Evidence: `{"state":"confirmed"}`, RateSnapshot: `{"id":"rate"}`, FundingSnapshot: `{"payer":"original"}`, MediaSnapshot: `{"existing":"policy"}`}
			require.NoError(t, db.Table("platform_model_calls").Create(&original).Error)
			config := Config{Enabled: true, Origin: "http://127.0.0.1:8088", ServiceToken: "isolated-migration-service-token-32bytes", Group: "platform", MaxInputTokens: 1000, MaxOutputTokens: 100, MaxBodyBytes: 8192, NativeHistory: &NativeHistoryConfig{ActiveKeyVersion: "v1", Keys: map[string][]byte{"v1": bytes.Repeat([]byte{3}, 32)}, TTLSeconds: 3600, Capacity: 100, Policies: map[string]NativeHistoryPolicy{"model": {Version: "test-v1", ChannelID: 1, Namespace: `{"API":"http://127.0.0.1:8088/v1","Project":""}`, UpstreamModel: "model", DefaultStored: true}}}}
			for range 2 {
				a, e := New(db, config)
				require.NoError(t, e)
				var got legacyCall
				require.NoError(t, db.Table("platform_model_calls").First(&got, "call_id = ?", "existing").Error)
				assert.Equal(t, original, got)
				var current Call
				require.NoError(t, db.First(&current, "call_id = ?", "existing").Error)
				assert.Empty(t, current.ContinuitySnapshot)
				assert.Empty(t, current.NativeObservation)
				var n int64
				require.NoError(t, db.Table("platform_native_history_references").Count(&n).Error)
				assert.Zero(t, n)
				// Old financial journals never acquire grants merely by running migration.
				_, e = a.history.Resolve(context.Background(), ci.Owner{ActorID: "actor", AccountID: "payer", ProductID: "superwagie", InstanceID: "default", CredentialID: "credential", RootTaskID: "root", Mode: ci.TaskCredential}, []ci.Selector{{Kind: ci.Response, ID: "untrusted-old-id"}})
				assert.ErrorIs(t, e, ci.ErrUnavailable)
			}
		})
	}
}
