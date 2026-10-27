package model

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/metering"
	"github.com/glebarez/sqlite"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// Dedicated, opt-in cross-dialect acceptance. Explicitly requested runs fail,
// rather than skip, when either external database is not configured.
func TestNativeBillingCrossDatabaseAcceptance(t *testing.T) {
	if os.Getenv("LLMROUTER_NATIVE_CROSS_DB") != "1" {
		t.Skip("set LLMROUTER_NATIVE_CROSS_DB=1 with dedicated database DSNs")
	}
	for _, tc := range []struct {
		name, env string
		open      func(string) gorm.Dialector
	}{
		{"sqlite", "", sqlite.Open},
		{"mysql57", "LLMROUTER_NATIVE_MYSQL_DSN", mysql.Open},
		{"postgres96", "LLMROUTER_NATIVE_POSTGRES_DSN", func(s string) gorm.Dialector {
			return postgres.New(postgres.Config{DSN: s, PreferSimpleProtocol: true})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dsn := os.Getenv(tc.env)
			if tc.name == "sqlite" {
				dsn = filepath.Join(t.TempDir(), "native.db")
			} else {
				require.NotEmpty(t, dsn, "dedicated DSN required")
			}
			db, err := gorm.Open(tc.open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
			require.NoError(t, err)
			conn, err := db.DB()
			require.NoError(t, err)
			conn.SetMaxOpenConns(1)
			defer conn.Close()
			require.NoError(t, db.AutoMigrate(&User{}, &Token{}, &Channel{}, &SubscriptionPlan{}, &UserSubscription{}, &OCRJob{}, &OCROperation{}, &OCRAuditEvent{}, &NativeBilling{}, &Log{}, &OCRLogReceipt{}))
			// Run migrations again to verify persisted portable receipt/schema shape.
			require.NoError(t, db.AutoMigrate(&NativeBilling{}, &OCROperation{}, &OCRAuditEvent{}, &OCRLogReceipt{}))
			u := User{Username: "native-" + uuid.NewString()[:8], AffCode: uuid.NewString()[:16], Quota: 1000, Status: common.UserStatusEnabled}
			require.NoError(t, db.Create(&u).Error)
			tok := Token{UserId: u.Id, Key: uuid.NewString(), RemainQuota: 500, Status: common.TokenStatusEnabled, ExpiredTime: -1}
			require.NoError(t, db.Create(&tok).Error)
			ch := Channel{Name: "native-integration"}
			require.NoError(t, db.Omit("ChannelInfo").Create(&ch).Error)
			store := NewNativeBillingStore(db, nil)
			defer store.Close()
			price := metering.Price{Version: "metered_v2", Operation: "speech", Policy: "provider_reported_v1", Terms: []metering.Term{{Key: "characters", Unit: "character", Price: metering.Rational{Numerator: "1", Denominator: "1000"}}}}
			admission := NativeAdmission{UserID: u.Id, TokenID: tok.Id, ChannelID: ch.Id, Model: "fixture", Group: "default", Preference: "wallet_only", Price: price, GroupRatio: "1", QuotaPerUnit: "100", Reservation: nativeUsage("1000")}
			ctx := context.Background()
			old := common.LogConsumeEnabled
			common.LogConsumeEnabled = true
			defer func() { common.LogConsumeEnabled = old }()
			for _, value := range []string{"500", "0"} {
				admission.CallID = uuid.NewString()
				row, err := store.Begin(ctx, admission)
				require.NoError(t, err)
				require.NoError(t, store.RecordUsage(ctx, row.ID, nativeUsage(value)))
				// A new store simulates process restart with only durable evidence.
				restarted := NewNativeBillingStore(db, nil)
				first, err := restarted.Finish(ctx, row.ID, "measured")
				require.NoError(t, err)
				second, err := restarted.Finish(ctx, row.ID, "measured")
				require.NoError(t, err)
				assert.Equal(t, first.FinalQuota, second.FinalQuota)
				assert.Equal(t, "settled", second.State)
				if value == "0" {
					assert.Zero(t, second.FinalQuota)
				} else {
					assert.Equal(t, 50, second.FinalQuota)
				}
				require.NoError(t, restarted.Close())
			}
			// Audited resolution survives restart; the same action cannot debit twice.
			admission.CallID = uuid.NewString()
			row, err := store.Begin(ctx, admission)
			require.NoError(t, err)
			require.NoError(t, store.MarkDispatched(ctx, row.ID, row.AttemptID))
			row, err = store.ResolveMeasurement(ctx, row.ID, "admin-acceptance", u.Id, "confirm_no_output", nil, "confirmed no output")
			require.NoError(t, err)
			restarted := NewNativeBillingStore(db, nil)
			defer restarted.Close()
			row, err = restarted.ResolveMeasurement(ctx, row.ID, "admin-acceptance", u.Id, "confirm_no_output", nil, "confirmed no output")
			require.NoError(t, err)
			assert.Equal(t, "released", row.State)
			assert.Equal(t, "admin-acceptance", row.AdminActionID)
			require.NoError(t, restarted.ledger.ProjectAuditLogs(ctx, db, 100))
			require.NoError(t, restarted.ledger.ProjectAuditLogs(ctx, db, 100))
			var logs []Log
			require.NoError(t, db.Where("user_id = ?", u.Id).Find(&logs).Error)
			require.Len(t, logs, 3)
			var receipts int64
			require.NoError(t, db.Model(&OCRLogReceipt{}).Count(&receipts).Error)
			assert.EqualValues(t, 3, receipts)
			var finalUser User
			var finalToken Token
			require.NoError(t, db.First(&finalUser, u.Id).Error)
			require.NoError(t, db.First(&finalToken, tok.Id).Error)
			assert.Equal(t, 950, finalUser.Quota)
			assert.Equal(t, 450, finalToken.RemainQuota)
			assert.Equal(t, 50, finalToken.UsedQuota)
		})
	}
}
