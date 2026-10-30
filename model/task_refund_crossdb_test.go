package model

import (
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// DSNs must point to disposable, dedicated test databases/schemas.
func TestRefundSelectionCrossDatabase(t *testing.T) {
	for _, tc := range []struct {
		name, env string
		open      func(string) gorm.Dialector
	}{
		{"mysql", "LLMROUTER_TASK_REFUND_MYSQL_DSN", mysql.Open},
		{"postgres", "LLMROUTER_TASK_REFUND_POSTGRES_DSN", postgres.Open},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dsn := os.Getenv(tc.env)
			if dsn == "" {
				t.Skip("dedicated database DSN not configured")
			}
			db, err := gorm.Open(tc.open(dsn), &gorm.Config{})
			require.NoError(t, err)
			conn, err := db.DB()
			require.NoError(t, err)
			defer conn.Close()
			require.NoError(t, db.AutoMigrate(&Task{}))
			old := DB
			DB = db
			defer func() { DB = old }()
			for _, owner := range []string{"native", "platform", "", "null"} {
				task := Task{TaskID: "refund-" + owner, FundingOwner: owner, Status: TaskStatusFailure, Progress: "100%", Quota: 1, SubmitTime: TaskRefundLegacyCutoff}
				require.NoError(t, db.Create(&task).Error)
				if owner == "null" {
					require.NoError(t, db.Model(&Task{}).Where("id = ?", task.ID).Update("funding_owner", nil).Error)
				}
			}
			rows := GetUnrefundedFailedTasks(time.Now().Unix()+60, 2)
			require.Len(t, rows, 2)
			assert.Equal(t, "refund-", rows[0].TaskID)
			assert.Equal(t, "refund-null", rows[1].TaskID)
		})
	}
}
