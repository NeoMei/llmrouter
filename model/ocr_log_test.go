package model

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/glebarez/sqlite"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// Each fixture owns unique rows; configured DSNs must point to disposable fixtures.
func TestOCRLogProjectionDatabases(t *testing.T) {
	for _, tc := range []struct {
		name, env string
		open      func(string) gorm.Dialector
	}{
		{"sqlite-shared", "", sqlite.Open}, {"sqlite-separate", "", sqlite.Open},
		{"mysql57", "LLMROUTER_ACCOUNT_POOL_MYSQL_DSN", mysql.Open},
		{"mysql57-found-rows", "LLMROUTER_ACCOUNT_POOL_MYSQL_DSN", mysql.Open},
		{"postgres96", "LLMROUTER_ACCOUNT_POOL_POSTGRES_DSN", func(dsn string) gorm.Dialector {
			return postgres.New(postgres.Config{DSN: dsn, PreferSimpleProtocol: true})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dsn := os.Getenv(tc.env)
			if tc.env != "" && dsn == "" {
				t.Skip(tc.env + " not configured")
			}
			if tc.env == "" {
				dsn = filepath.Join(t.TempDir(), "main.db") + "?_pragma=busy_timeout(5000)"
			}
			if tc.name == "mysql57-found-rows" {
				dsn += "&clientFoundRows=true"
			}
			open := func(dsn string) *gorm.DB {
				db, err := gorm.Open(tc.open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
				require.NoError(t, err)
				sqlDB, err := db.DB()
				require.NoError(t, err)
				if strings.HasPrefix(tc.name, "sqlite") {
					sqlDB.SetMaxOpenConns(1)
				}
				t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
				return db
			}
			db := open(dsn)
			logDB := db
			logDSN := dsn
			if tc.name == "sqlite-separate" {
				logDSN = filepath.Join(t.TempDir(), "log.db") + "?_pragma=busy_timeout(5000)"
				logDB = open(logDSN)
			}
			independentLog := open(logDSN)
			independentMain := open(dsn)
			require.NoError(t, db.AutoMigrate(&User{}, &Token{}, &Channel{}, &SubscriptionPlan{}, &UserSubscription{}, &OCRJob{}, &OCROperation{}, &OCRAuditEvent{}))
			oldDB, oldLog, oldEnabled, oldMainType, oldLogType := DB, LOG_DB, common.LogConsumeEnabled, common.MainDatabaseType(), common.LogDatabaseType()
			DB, LOG_DB, common.LogConsumeEnabled = db, logDB, true
			typ := common.DatabaseTypeSQLite
			if strings.HasPrefix(tc.name, "mysql") {
				typ = common.DatabaseTypeMySQL
			}
			if tc.name == "postgres96" {
				typ = common.DatabaseTypePostgreSQL
			}
			common.SetDatabaseTypes(typ, typ)
			initCol()
			t.Cleanup(func() {
				DB, LOG_DB, common.LogConsumeEnabled = oldDB, oldLog, oldEnabled
				common.SetDatabaseTypes(oldMainType, oldLogType)
				initCol()
			})
			for i := 0; i < 2; i++ {
				require.NoError(t, migrateLOGDB())
			}
			u := User{Username: "ocrlog-" + uuid.NewString()[:12], AffCode: uuid.NewString()[:16], Quota: 1000, Status: common.UserStatusEnabled, AuthVersion: 1}
			require.NoError(t, db.Create(&u).Error)
			tok := Token{UserId: u.Id, Name: "trusted token", Key: uuid.NewString(), RemainQuota: 500, Status: common.TokenStatusEnabled, ExpiredTime: -1}
			require.NoError(t, db.Create(&tok).Error)
			ch := Channel{Name: "trusted channel"}
			require.NoError(t, db.Omit("ChannelInfo").Create(&ch).Error)
			var eventIDs []string
			t.Cleanup(func() {
				if len(eventIDs) > 0 {
					require.NoError(t, logDB.Where("event_id IN ?", eventIDs).Delete(&OCRLogReceipt{}).Error)
					require.NoError(t, db.Where("id IN ?", eventIDs).Delete(&OCRAuditEvent{}).Error)
				}
				require.NoError(t, logDB.Where("user_id = ?", u.Id).Delete(&Log{}).Error)
				var ids []string
				require.NoError(t, db.Model(&OCRJob{}).Where("user_id = ?", u.Id).Pluck("id", &ids).Error)
				if len(ids) > 0 {
					require.NoError(t, db.Where("job_id IN ?", ids).Delete(&OCROperation{}).Error)
					require.NoError(t, db.Where("id IN ?", ids).Delete(&OCRJob{}).Error)
				}
				require.NoError(t, db.Unscoped().Delete(&tok).Error)
				require.NoError(t, db.Unscoped().Delete(&u).Error)
				require.NoError(t, db.Delete(&ch).Error)
			})
			s := NewOCRBillingStore(db, nil)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			admission := OCRAdmission{IdempotencyKey: uuid.NewString(), UserID: u.Id, TokenID: tok.Id, ChannelID: ch.Id, Model: "ocr-log-" + uuid.NewString(), Group: "default", Preference: "wallet_only", Price: .0002, GroupRatio: 1, QuotaPerUnit: 500000}
			makeEvent := func(outcome string) OCRAuditEvent {
				a := admission
				a.IdempotencyKey = uuid.NewString()
				j, err := s.CreateIntent(ctx, a)
				require.NoError(t, err)
				_, err = s.Reserve(ctx, j.ID)
				require.NoError(t, err)
				if outcome != "cancel" {
					claim, err := s.ClaimSubmission(ctx, j.ID, ocrTestSelection(ch.Id))
					require.NoError(t, err)
					require.NoError(t, s.RecordSubmission(ctx, claim, "PRIVATE_PROVIDER_JOB", false))
				}
				_, err = s.Finish(ctx, j.ID, outcome)
				require.NoError(t, err)
				var e OCRAuditEvent
				require.NoError(t, db.Where("job_id = ?", j.ID).First(&e).Error)
				eventIDs = append(eventIDs, e.ID)
				return e
			}
			e := makeEvent("success")
			require.NoError(t, db.Model(&OCRJob{}).Where("id = ?", e.JobID).Update("created_at", e.CreatedAt-4).Error)
			// Durable counters are owned by Finish, never by any projection attempt.
			snapshot := func() []int64 {
				var user User
				var token Token
				var channel Channel
				require.NoError(t, db.Unscoped().First(&user, u.Id).Error)
				require.NoError(t, db.Unscoped().First(&token, tok.Id).Error)
				require.NoError(t, db.First(&channel, ch.Id).Error)
				return []int64{int64(user.Quota), int64(user.UsedQuota), int64(user.RequestCount), int64(token.RemainQuota), int64(token.UsedQuota), channel.UsedQuota}
			}
			money := snapshot()
			// Separate connection pools force first-time receipt contention, not just
			// repeated reads of a receipt created by an earlier delivery.
			locked, contenderReady := make(chan struct{}), make(chan struct{})
			require.NoError(t, logDB.Callback().Query().After("gorm:query").Register("ocr_log_lock_barrier", func(tx *gorm.DB) {
				if tx.Statement.Table == "ocr_log_receipts" && tx.Statement.Context.Value(ocrRaceContextKey{}) == "winner" {
					close(locked)
					select {
					case <-contenderReady:
					case <-ctx.Done():
						tx.AddError(ctx.Err())
					}
				}
			}))
			require.NoError(t, independentLog.Callback().Create().Before("gorm:create").Register("ocr_log_contender", func(tx *gorm.DB) {
				if tx.Statement.Table == "ocr_log_receipts" {
					close(contenderReady)
				}
			}))
			results := make(chan error, 2)
			go func() { results <- s.DeliverAuditLog(context.WithValue(ctx, ocrRaceContextKey{}, "winner"), logDB, e) }()
			select {
			case <-locked:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			go func() { results <- NewOCRBillingStore(independentMain, nil).DeliverAuditLog(ctx, independentLog, e) }()
			require.NoError(t, <-results)
			require.NoError(t, <-results)
			require.NoError(t, logDB.Callback().Query().Remove("ocr_log_lock_barrier"))
			require.NoError(t, independentLog.Callback().Create().Remove("ocr_log_contender"))
			require.NoError(t, s.DeliverAuditLog(ctx, logDB, e))
			assert.Equal(t, money, snapshot())

			var logs []Log
			require.NoError(t, logDB.Where("user_id = ?", u.Id).Find(&logs).Error)
			require.Len(t, logs, 1)
			l := logs[0]
			assert.Equal(t, 100, l.Quota)
			assert.Equal(t, LogTypeConsume, l.Type)
			assert.Zero(t, l.PromptTokens)
			assert.Zero(t, l.CompletionTokens)
			assert.Equal(t, 4, l.UseTime)
			assert.Equal(t, u.Username, l.Username)
			assert.Equal(t, tok.Name, l.TokenName)
			assert.Equal(t, e.CreatedAt, l.CreatedAt)
			assert.Equal(t, "ocr-audit-"+e.ID, l.RequestId)
			assert.Equal(t, "default", l.Group)
			assert.Empty(t, l.UpstreamRequestId)
			var other map[string]any
			require.NoError(t, common.UnmarshalJsonStr(l.Other, &other))
			assert.Equal(t, "request", other["billing_unit"])
			assert.Equal(t, float64(1), other["request_count"])
			assert.Equal(t, .0002, other["model_price"])
			assert.Equal(t, "wallet", other["billing_source"])
			// A log commit can precede a lost main acknowledgement. Restart only reuses receipt.
			fault := errors.New("lost main acknowledgement")
			require.NoError(t, db.Callback().Update().Before("gorm:update").Register("ocr_log_ack_loss", func(tx *gorm.DB) {
				if tx.Statement.Table == "ocr_audit_events" {
					tx.AddError(fault)
				}
			}))
			require.ErrorIs(t, s.ProjectAuditLogs(ctx, logDB, 100), fault)
			require.NoError(t, db.Callback().Update().Remove("ocr_log_ack_loss"))
			var pending OCRAuditEvent
			require.NoError(t, db.First(&pending, "id = ?", e.ID).Error)
			assert.Zero(t, pending.ProjectedAt)
			restarted := NewOCRBillingStore(db, nil)
			require.NoError(t, restarted.ProjectAuditLogs(ctx, logDB, 100))
			var n int64
			require.NoError(t, logDB.Model(&Log{}).Where("user_id = ?", u.Id).Count(&n).Error)
			assert.EqualValues(t, 1, n)
			assert.Equal(t, money, snapshot())
			// Existing user/token/admin query APIs can read native per-request logs.
			users, total, err := GetUserLogs(u.Id, LogTypeConsume, 0, 0, "", "", 0, 10, "default", l.RequestId, "")
			require.NoError(t, err)
			assert.EqualValues(t, 1, total)
			require.Len(t, users, 1)
			byToken, err := GetLogByTokenId(tok.Id)
			require.NoError(t, err)
			require.Len(t, byToken, 1)
			admins, total, err := GetAllLogs(LogTypeConsume, 0, 0, "", u.Username, "", 0, 10, 0, "", l.RequestId, "")
			require.NoError(t, err)
			assert.EqualValues(t, 1, total)
			require.Len(t, admins, 1)
			// Ordinary log retention must leave its private deduplication receipt.
			require.NoError(t, logDB.Model(&Log{}).Where("id = ?", l.Id).Update("created_at", 1).Error)
			LOG_DB = logDB.Where("user_id = ?", u.Id)
			purged, err := DeleteOldLogBatch(ctx, 2, 100)
			LOG_DB = logDB
			require.NoError(t, err)
			assert.EqualValues(t, 1, purged)
			require.NoError(t, restarted.DeliverAuditLog(ctx, logDB, e))
			require.NoError(t, logDB.Model(&Log{}).Where("request_id = ?", l.RequestId).Count(&n).Error)
			assert.Zero(t, n)
			for _, outcome := range []string{"failure", "cancel"} {
				event := makeEvent(outcome)
				money = snapshot()
				require.NoError(t, s.DeliverAuditLog(ctx, logDB, event))
				var row Log
				require.NoError(t, logDB.First(&row, "request_id = ?", "ocr-audit-"+event.ID).Error)
				assert.Equal(t, LogTypeError, row.Type)
				assert.Zero(t, row.Quota)
				assert.Contains(t, row.Content, outcome)
				assert.Equal(t, money, snapshot())
			}
			// Log transaction rollback cannot leave a receipt or a partial row.
			rollbackEvent := makeEvent("success")
			money = snapshot()
			require.NoError(t, logDB.Callback().Update().Before("gorm:update").Register("ocr_log_receipt_failure", func(tx *gorm.DB) {
				if tx.Statement.Table == "ocr_log_receipts" {
					tx.AddError(fault)
				}
			}))
			require.ErrorIs(t, s.DeliverAuditLog(ctx, logDB, rollbackEvent), fault)
			require.NoError(t, logDB.Callback().Update().Remove("ocr_log_receipt_failure"))
			require.NoError(t, logDB.Model(&OCRLogReceipt{}).Where("event_id = ?", rollbackEvent.ID).Count(&n).Error)
			assert.Zero(t, n)
			require.NoError(t, logDB.Model(&Log{}).Where("request_id = ?", "ocr-audit-"+rollbackEvent.ID).Count(&n).Error)
			assert.Zero(t, n)
			assert.Equal(t, money, snapshot())
			require.NoError(t, s.DeliverAuditLog(ctx, logDB, rollbackEvent))
			assert.Equal(t, money, snapshot())
			// Admission events have no job and never transfer arbitrary payload fields.
			reason := map[string]any{"reason": "PRIVATE_REASON_URL", "provider_id": "PRIVATE_PROVIDER_ID", "admin_info": map[string]any{"secret": "PRIVATE_ADMIN_SECRET", "quota_saturation": map[string]any{"op": "QuotaFromFloat", "kind": "overflow", "clamped": common.MaxQuota, "secret": "PRIVATE_CLAMP_SECRET"}}}
			require.NoError(t, s.RecordAdmissionRejection(ctx, u.Id, tok.Id, admission.Model, reason))
			var rejected OCRAuditEvent
			require.NoError(t, db.Where("kind = ? AND payload LIKE ?", "admission_rejected", "%"+admission.Model+"%").Order("created_at desc, id desc").First(&rejected).Error)
			eventIDs = append(eventIDs, rejected.ID)
			require.NoError(t, s.DeliverAuditLog(ctx, logDB, rejected))
			var rejectionLog Log
			require.NoError(t, logDB.First(&rejectionLog, "request_id = ?", "ocr-audit-"+rejected.ID).Error)
			assert.Equal(t, LogTypeError, rejectionLog.Type)
			assert.Zero(t, rejectionLog.Quota)
			assert.NotContains(t, rejectionLog.Other, "PRIVATE")
			assert.Contains(t, rejectionLog.Other, "quota_saturation")
			users, _, err = GetUserLogs(u.Id, 0, 0, 0, "", "", 0, 10, "", rejectionLog.RequestId, "")
			require.NoError(t, err)
			require.Len(t, users, 1)
			assert.NotContains(t, users[0].Other, "admin_info")
			tokenLogs, err := GetLogByTokenId(tok.Id)
			require.NoError(t, err)
			for _, row := range tokenLogs {
				assert.NotContains(t, row.Other, "admin_info")
			}
			admins, _, err = GetAllLogs(0, 0, 0, "", u.Username, "", 0, 10, 0, "", rejectionLog.RequestId, "")
			require.NoError(t, err)
			require.Len(t, admins, 1)
			assert.Contains(t, admins[0].Other, "quota_saturation")
			// Disabled consumption logging pauses durable delivery, never acknowledges it.
			disabled := makeEvent("success")
			money = snapshot()
			require.NoError(t, db.Model(&OCRAuditEvent{}).Where("id = ?", disabled.ID).Update("created_at", 1).Error)
			afterDisabled := makeEvent("cancel")
			assert.Equal(t, money, snapshot())
			common.LogConsumeEnabled = false
			require.ErrorIs(t, s.DeliverAuditLog(ctx, logDB, disabled), ErrOCRLogDisabled)
			require.NoError(t, s.ProjectAuditLogs(ctx, logDB, 100))
			var eligible int64
			require.NoError(t, db.Model(&OCRAuditEvent{}).Where("projected_at = ? AND kind <> ?", 0, "success").Count(&eligible).Error)
			assert.Zero(t, eligible)
			var afterRow Log
			require.NoError(t, logDB.First(&afterRow, "request_id = ?", "ocr-audit-"+afterDisabled.ID).Error)
			assert.Equal(t, LogTypeError, afterRow.Type)
			common.LogConsumeEnabled = true
			pending = OCRAuditEvent{}
			require.NoError(t, db.First(&pending, "id = ?", disabled.ID).Error)
			assert.Zero(t, pending.ProjectedAt)
			require.NoError(t, s.ProjectAuditLogs(ctx, logDB, 100))
			var delivered OCRAuditEvent
			require.NoError(t, db.First(&delivered, "id = ?", disabled.ID).Error)
			assert.Positive(t, delivered.ProjectedAt)
			assert.Equal(t, money, snapshot())
			// A main acknowledgement may itself commit before its reply is lost.
			mainAckEvent := makeEvent("success")
			money = snapshot()
			var mainLose atomic.Int32
			mainLose.Store(1)
			mainPool := ocrCommitAckPool{ConnPool: db.ConnPool, begin: db.ConnPool.(gorm.TxBeginner), lose: &mainLose}
			faultMain := db.Session(&gorm.Session{Context: ctx, NewDB: true})
			faultMain.ConnPool = mainPool
			faultMain.Statement.ConnPool = mainPool
			require.ErrorIs(t, NewOCRBillingStore(faultMain, nil).ProjectAuditLogs(ctx, logDB, 100), io.ErrUnexpectedEOF)
			var mainAcknowledged OCRAuditEvent
			require.NoError(t, db.First(&mainAcknowledged, "id = ?", mainAckEvent.ID).Error)
			assert.Positive(t, mainAcknowledged.ProjectedAt)
			require.NoError(t, NewOCRBillingStore(db, nil).ProjectAuditLogs(ctx, logDB, 100))
			require.NoError(t, logDB.Model(&Log{}).Where("request_id = ?", "ocr-audit-"+mainAckEvent.ID).Count(&n).Error)
			assert.EqualValues(t, 1, n)
			assert.Equal(t, money, snapshot())
			// Deleted names use a safe empty fallback and lifecycle facts remain available.
			deleted := makeEvent("success")
			money = snapshot()
			require.NoError(t, db.Delete(&u).Error)
			require.NoError(t, db.Delete(&tok).Error)
			require.NoError(t, s.DeliverAuditLog(ctx, logDB, deleted))
			var deletedLog Log
			require.NoError(t, logDB.First(&deletedLog, "request_id = ?", "ocr-audit-"+deleted.ID).Error)
			assert.Empty(t, deletedLog.Username)
			assert.Empty(t, deletedLog.TokenName)
			assert.Equal(t, money, snapshot())
			// SQL commit actually succeeds while the LogDB acknowledgement is lost.
			var lose atomic.Int32
			lose.Store(1)
			pool := ocrCommitAckPool{ConnPool: logDB.ConnPool, begin: logDB.ConnPool.(gorm.TxBeginner), lose: &lose}
			faultLog := logDB.Session(&gorm.Session{Context: ctx, NewDB: true})
			faultLog.ConnPool = pool
			faultLog.Statement.ConnPool = pool
			ackEvent := rejected
			ackEvent.ID = uuid.NewString()
			ackEvent.JobID = ackEvent.ID
			ackEvent.ProjectedAt = 0
			require.NoError(t, db.Create(&ackEvent).Error)
			eventIDs = append(eventIDs, ackEvent.ID)
			require.ErrorIs(t, s.DeliverAuditLog(ctx, faultLog, ackEvent), io.ErrUnexpectedEOF)
			require.NoError(t, restarted.ProjectAuditLogs(ctx, logDB, 100))
			require.NoError(t, logDB.Model(&Log{}).Where("request_id = ?", "ocr-audit-"+ackEvent.ID).Count(&n).Error)
			assert.EqualValues(t, 1, n)
			assert.Equal(t, money, snapshot())
			var ackDelivered OCRAuditEvent
			require.NoError(t, db.First(&ackDelivered, "id = ?", ackEvent.ID).Error)
			assert.Positive(t, ackDelivered.ProjectedAt)
			require.NoError(t, logDB.Where("user_id = ?", u.Id).Find(&logs).Error)
			for _, row := range logs {
				wire, err := common.Marshal(row)
				require.NoError(t, err)
				assert.NotContains(t, string(wire), "PRIVATE")
				assert.NotContains(t, string(wire), tok.Key)
			}
		})
	}
}

// The two main migration owners must both install the private receipt, while
// retaining existing rows on a second startup. Every database is temporary.
func TestOCRLogProjectionMainMigrationPaths(t *testing.T) {
	for _, tc := range []struct {
		name    string
		migrate func() error
	}{{"normal", migrateDB}, {"fast", migrateDBFast}} {
		t.Run(tc.name, func(t *testing.T) {
			db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "migration.db")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
			require.NoError(t, err)
			sqlDB, err := db.DB()
			require.NoError(t, err)
			sqlDB.SetMaxOpenConns(1)
			defer sqlDB.Close()
			previousDB, previousLog, mainType, logType := DB, LOG_DB, common.MainDatabaseType(), common.LogDatabaseType()
			DB, LOG_DB = db, db
			common.SetDatabaseTypes(common.DatabaseTypeSQLite, common.DatabaseTypeSQLite)
			initCol()
			defer func() { DB, LOG_DB = previousDB, previousLog; common.SetDatabaseTypes(mainType, logType); initCol() }()
			require.NoError(t, tc.migrate())
			require.True(t, db.Migrator().HasTable(&OCRLogReceipt{}))
			receipt := OCRLogReceipt{EventID: uuid.NewString(), LogID: 123}
			require.NoError(t, db.Create(&receipt).Error)
			require.NoError(t, tc.migrate())
			var retained OCRLogReceipt
			require.NoError(t, db.First(&retained, "event_id = ?", receipt.EventID).Error)
			assert.Equal(t, receipt, retained)
		})
	}
}

type ocrUnsupportedLogDialect struct{ gorm.Dialector }

func (ocrUnsupportedLogDialect) Name() string { return "clickhouse" }

func TestOCRLogProjectionUnsupportedAndCancellation(t *testing.T) {
	s, a := ocrFixture(t)
	ctx := context.Background()
	require.NoError(t, s.RecordAdmissionRejection(ctx, a.UserID, a.TokenID, a.Model, map[string]any{}))
	var event OCRAuditEvent
	require.NoError(t, s.db.First(&event).Error)
	config := *s.db.Config
	config.Dialector = ocrUnsupportedLogDialect{s.db.Dialector}
	unsupported := s.db.Session(&gorm.Session{Context: ctx, NewDB: true})
	unsupported.Config = &config
	require.ErrorIs(t, s.DeliverAuditLog(ctx, unsupported, event), ErrOCRLogUnsupported)
	require.ErrorIs(t, s.ProjectAuditLogs(ctx, unsupported, 1), ErrOCRLogUnsupported)
	assert.False(t, s.db.Migrator().HasTable(&OCRLogReceipt{}))
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	require.ErrorIs(t, s.ProjectAuditLogs(canceled, s.db, 1), context.Canceled)
	var retained OCRAuditEvent
	require.NoError(t, s.db.First(&retained, "id = ?", event.ID).Error)
	assert.Zero(t, retained.ProjectedAt)
	require.Error(t, s.ProjectAuditLogs(ctx, s.db, 0))
	require.Error(t, s.ProjectAuditLogs(ctx, s.db, 101))
	ocrBalances(t, s, a, 1000, 500, 0)
}
