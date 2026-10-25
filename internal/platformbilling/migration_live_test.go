package platformbilling

import (
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// Opt-in and destructive only to explicitly supplied disposable databases.
// This verifies the real pre-funding GORM schema upgrade on supported engines.
func TestFundingMigrationRealDatabases(t *testing.T) {
	for _, dialect := range []string{"mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			if os.Getenv("UNIHUB_GATEWAY_MIGRATION_DISPOSABLE") != "1" {
				t.Skip("explicit disposable-database opt-in required")
			}
			dsn := os.Getenv("UNIHUB_GATEWAY_MIGRATION_" + map[string]string{"mysql": "MYSQL_DSN", "postgres": "POSTGRES_DSN"}[dialect])
			if dsn == "" {
				t.Skip("disposable database not configured")
			}
			var driver gorm.Dialector
			if dialect == "mysql" {
				driver = mysql.Open(dsn)
			} else {
				driver = postgres.Open(dsn)
			}
			db, err := gorm.Open(driver, &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
			require.True(t, err == nil, "disposable database connection failed")
			sqlDB, err := db.DB()
			require.NoError(t, err)
			t.Cleanup(func() { _ = sqlDB.Close() })
			// Every field in the schema deployed before asset-aware billing.
			type legacyCall struct {
				CallID          string `gorm:"primaryKey;size:128"`
				ActorID         string `gorm:"size:128"`
				AccountID       string `gorm:"size:128"`
				OriginProductID string `gorm:"size:128"`
				CredentialID    string `gorm:"size:128"`
				TaskID          string `gorm:"size:128"`
				Model           string `gorm:"size:255"`
				Digest          string `gorm:"size:64"`
				RateID          string `gorm:"size:128"`
				Amount          string `gorm:"size:32"`
				Status          string `gorm:"size:32;index"`
				Intent          string `gorm:"type:text"`
				Charged         string `gorm:"size:32"`
				CreatedAt       time.Time
				UpdatedAt       time.Time
			}
			require.NoError(t, db.Migrator().DropTable(&Attempt{}, &Call{}))
			require.NoError(t, db.Table("platform_model_calls").AutoMigrate(&legacyCall{}))
			intent := `{"usage":{"input":"8","output":"4","cache_read":"2","cache_write":"0","reasoning":"1"},"provider_cost":""}`
			for _, state := range []string{"SETTLE_PENDING", "UNKNOWN", "RESERVED"} {
				row := legacyCall{CallID: state, ActorID: "actor", AccountID: "fixed-payer", OriginProductID: "superwagie", CredentialID: "credential", TaskID: "task", Model: "model", Digest: "unchanged-digest", RateID: "legacy-rate", Amount: "100", Status: state, Intent: intent, Charged: "0", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
				require.NoError(t, db.Table("platform_model_calls").Create(&row).Error)
			}
			config := Config{Enabled: true, Origin: "http://localhost:8088", ServiceToken: "test-only-service-token-over-32-characters", Group: "platform", MaxInputTokens: 1000, MaxOutputTokens: 100, MaxBodyBytes: 8192}
			for range 2 {
				_, err = New(db, config)
				require.NoError(t, err)
				var calls []Call
				require.NoError(t, db.Order("call_id").Find(&calls).Error)
				require.Len(t, calls, 3)
				for _, row := range calls {
					assert.Equal(t, row.CallID, row.Status)
					assert.Equal(t, "CREDIT/6", row.Asset)
					assert.Equal(t, "credits", row.FundingMode)
					assert.Equal(t, "llmrouter", row.FundingProductID)
					assert.Zero(t, row.BillingProfileVersion)
					assert.Equal(t, "fixed-payer", row.AccountID)
					assert.Equal(t, "unchanged-digest", row.Digest)
					assert.Equal(t, "legacy-rate", row.RateID)
					assert.JSONEq(t, intent, row.Intent)
				}
			}
			t.Log("pre-funding durable calls migrated twice with funding defaults; payer, digest, rate, terminal intent and state preserved")
		})
	}
}
