package platformbilling

import (
	"github.com/QuantumNous/new-api/common"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"os"
	"strings"
	"testing"
)

// External targets must be dedicated task-owned schemas; only this fixture's
// table is created. No database or unrelated table is dropped.
func TestKnowledgeFrozenSnapshotStorage(t *testing.T) {
	targets := map[string]string{"sqlite": ":memory:"}
	if file := os.Getenv("KNOWLEDGE_STORAGE_DATABASES"); file != "" {
		raw, e := os.ReadFile(file)
		require.NoError(t, e)
		require.NoError(t, common.Unmarshal(raw, &targets))
		targets["sqlite"] = ":memory:"
	}
	for dialect, dsn := range targets {
		t.Run(dialect, func(t *testing.T) {
			var driver gorm.Dialector
			switch dialect {
			case "sqlite":
				driver = sqlite.Open(dsn)
			case "postgres":
				driver = postgres.Open(dsn)
			default:
				driver = mysql.Open(dsn)
			}
			db, e := gorm.Open(driver, &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
			require.True(t, e == nil, "database connection failed")
			sql, e := db.DB()
			require.NoError(t, e)
			defer sql.Close()
			table := "knowledge_frozen_snapshot_fixture"
			// Upgrade an existing small TEXT column while preserving an uncertain call.
			type oldSnapshot struct {
				CallID        string `gorm:"primaryKey;size:128"`
				MediaSnapshot string `gorm:"type:text"`
				Status        string `gorm:"size:32"`
			}
			if db.Migrator().HasTable(table) {
				require.NoError(t, db.Table(table).Where("call_id = ?", "storage-proof").Delete(&oldSnapshot{}).Error)
			}
			require.NoError(t, db.Table(table).AutoMigrate(&oldSnapshot{}))
			require.NoError(t, db.Table(table).Create(&oldSnapshot{CallID: "storage-proof", MediaSnapshot: "original", Status: "UNKNOWN"}).Error)
			snapshot := `{"effective_body":{"image":"data:image/png;base64,` + strings.Repeat("A", 256*1024) + `"}}`
			for i := 0; i < 2; i++ {
				require.NoError(t, db.Table(table).AutoMigrate(&Call{}))
				var row Call
				require.NoError(t, db.Table(table).First(&row, "call_id = ?", "storage-proof").Error)
				assert.Equal(t, "UNKNOWN", row.Status)
				if i == 0 {
					assert.Equal(t, "original", row.MediaSnapshot)
					require.NoError(t, db.Table(table).Where("call_id = ?", "storage-proof").Update("media_snapshot", snapshot).Error)
				} else {
					assert.Equal(t, snapshot, row.MediaSnapshot)
				}
			}
			t.Logf("migrated twice; exact %d-byte frozen body and UNKNOWN preserved", len(snapshot))
		})
	}
}
