package ocr

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestOCRAuditRuntimeBoundedRecoveryAndShutdown(t *testing.T) {
	supplier := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("audit projection called supplier") }))
	defer supplier.Close()
	svc, req := serviceFixture(t, supplier)
	logDB, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "log.db")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	sqlDB, err := logDB.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	defer sqlDB.Close()
	previousLog, enabled := model.LOG_DB, common.LogConsumeEnabled
	model.LOG_DB, common.LogConsumeEnabled = logDB, true
	defer func() { model.LOG_DB, common.LogConsumeEnabled = previousLog, enabled }()
	require.NoError(t, logDB.AutoMigrate(&model.Log{}, &model.OCRLogReceipt{}))
	runtime := &Runtime{DB: svc.db, Billing: svc.billing, Service: svc}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for i := 0; i < 2; i++ {
		require.NoError(t, svc.billing.RecordAdmissionRejection(ctx, req.Admission.UserID, req.Admission.TokenID, req.Admission.Model, map[string]any{}))
	}
	require.NoError(t, runtime.RunAuditBatch(ctx, 1))
	var n int64
	require.NoError(t, logDB.Model(&model.Log{}).Count(&n).Error)
	assert.EqualValues(t, 1, n)
	require.NoError(t, svc.db.Model(&model.OCRAuditEvent{}).Where("projected_at = ?", 0).Count(&n).Error)
	assert.EqualValues(t, 1, n)
	// A temporary LogDB schema outage leaves delivery pending, not a job failure.
	require.NoError(t, logDB.Migrator().DropTable(&model.OCRLogReceipt{}))
	require.Error(t, runtime.RunAuditBatch(ctx, 1))
	require.NoError(t, svc.db.Model(&model.OCRAuditEvent{}).Where("projected_at = ?", 0).Count(&n).Error)
	assert.EqualValues(t, 1, n)
	require.NoError(t, logDB.AutoMigrate(&model.OCRLogReceipt{}))
	restarted := &Runtime{DB: svc.db, Billing: svc.billing, Service: svc}
	require.NoError(t, restarted.RunAuditBatch(ctx, 1))
	require.NoError(t, logDB.Model(&model.Log{}).Count(&n).Error)
	assert.EqualValues(t, 2, n)
	checkMoney(t, svc, req, 1000, 0, 0)
	// Close must cancel and join a worker blocked in an actual log transaction.
	require.NoError(t, svc.billing.RecordAdmissionRejection(ctx, req.Admission.UserID, req.Admission.TokenID, req.Admission.Model, map[string]any{}))
	entered := make(chan struct{})
	require.NoError(t, logDB.Callback().Create().Before("gorm:create").Register("ocr_audit_shutdown", func(tx *gorm.DB) {
		if tx.Statement.Table == "ocr_log_receipts" {
			close(entered)
			<-tx.Statement.Context.Done()
			tx.AddError(tx.Statement.Context.Err())
		}
	}))
	restarted.Start()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	require.NoError(t, restarted.Close())
	require.NoError(t, restarted.Close())
	restarted.Start()
	require.NoError(t, logDB.Callback().Create().Remove("ocr_audit_shutdown"))
	require.NoError(t, svc.db.Model(&model.OCRAuditEvent{}).Where("projected_at = ?", 0).Count(&n).Error)
	assert.EqualValues(t, 1, n)
	checkMoney(t, svc, req, 1000, 0, 0)
}

// Each phase races one real persisted option update with one projection call.
// Independent option/main/log databases avoid accidental SQL-pool ordering of
// the option accesses. A committed-Save barrier releases the writer just before
// its live-option dispatch alongside the worker, without sleeps or retry loops;
// the option mutex is the intended happens-before boundary.
func TestOCRAuditRuntimeLiveConsumeOption(t *testing.T) {
	supplier := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		_, _ = w.Write([]byte(`{"code":0,"data":{"jobId":"owned"}}`))
	}))
	defer supplier.Close()
	svc, req := serviceFixture(t, supplier)
	open := func(name string) *gorm.DB {
		db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), name)), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
		require.NoError(t, err)
		sqlDB, err := db.DB()
		require.NoError(t, err)
		sqlDB.SetMaxOpenConns(1)
		t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
		return db
	}
	optionDB, logDB := open("options.db"), open("logs.db")
	require.NoError(t, optionDB.AutoMigrate(&model.Option{}))
	require.NoError(t, logDB.AutoMigrate(&model.Log{}, &model.OCRLogReceipt{}))
	oldDB, oldLog := model.DB, model.LOG_DB
	model.DB, model.LOG_DB = optionDB, logDB
	common.OptionMapRWMutex.Lock()
	oldMap, oldEnabled := common.OptionMap, common.LogConsumeEnabled
	common.OptionMap = map[string]string{}
	common.LogConsumeEnabled = false
	common.OptionMapRWMutex.Unlock()
	defer func() {
		model.DB, model.LOG_DB = oldDB, oldLog
		common.OptionMapRWMutex.Lock()
		common.OptionMap, common.LogConsumeEnabled = oldMap, oldEnabled
		common.OptionMapRWMutex.Unlock()
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	accepted, err := svc.Create(ctx, req)
	require.NoError(t, err)
	_, err = svc.billing.Finish(ctx, accepted.JobID, "success")
	require.NoError(t, err)
	cancelledAdmission := req.Admission
	cancelledAdmission.IdempotencyKey = "cancel-for-live-option"
	cancelled, err := svc.billing.CreateIntent(ctx, cancelledAdmission)
	require.NoError(t, err)
	_, err = svc.billing.Reserve(ctx, cancelled.ID)
	require.NoError(t, err)
	_, err = svc.billing.Finish(ctx, cancelled.ID, "cancel")
	require.NoError(t, err)
	runtime := &Runtime{DB: svc.db, Billing: svc.billing, Service: svc}
	var successEvent model.OCRAuditEvent
	require.NoError(t, svc.db.First(&successEvent, "job_id = ?", accepted.JobID).Error)
	require.NoError(t, model.UpdateOption("LogConsumeEnabled", "false"))
	for _, tc := range []struct {
		name     string
		project  func() error
		expected error
	}{
		{"worker-selection", func() error { return runtime.RunAuditBatch(ctx, 32) }, nil},
		{"delivery-guard", func() error { return svc.billing.DeliverAuditLog(ctx, logDB, successEvent) }, model.ErrOCRLogDisabled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			start, writerReady := make(chan struct{}), make(chan struct{})
			require.NoError(t, optionDB.Callback().Update().After("gorm:commit_or_rollback_transaction").Register("ocr_live_option_barrier", func(tx *gorm.DB) {
				if tx.Statement.Table == "options" {
					close(writerReady)
					select {
					case <-start:
					case <-ctx.Done():
						tx.AddError(ctx.Err())
					}
				}
			}))
			written, projected := make(chan error, 1), make(chan error, 1)
			go func() { written <- model.UpdateOption("LogConsumeEnabled", "false") }()
			go func() { <-start; projected <- tc.project() }()
			select {
			case <-writerReady:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			close(start)
			require.NoError(t, <-written)
			if tc.expected == nil {
				require.NoError(t, <-projected)
			} else {
				require.ErrorIs(t, <-projected, tc.expected)
			}
			require.NoError(t, optionDB.Callback().Update().Remove("ocr_live_option_barrier"))
		})
	}
	// Disabled successes stay pending, but the cancellation drains exactly once.
	var persisted model.Option
	require.NoError(t, optionDB.First(&persisted, "key = ?", "LogConsumeEnabled").Error)
	assert.Equal(t, "false", persisted.Value)
	var pending model.OCRAuditEvent
	require.NoError(t, svc.db.First(&pending, "id = ?", successEvent.ID).Error)
	assert.Zero(t, pending.ProjectedAt)
	var logs []model.Log
	require.NoError(t, logDB.Order("id").Find(&logs).Error)
	require.Len(t, logs, 1)
	assert.Equal(t, model.LogTypeError, logs[0].Type)
	assert.Zero(t, logs[0].Quota)
	// The live update also resumes durable success delivery without repricing.
	require.NoError(t, model.UpdateOption("LogConsumeEnabled", "true"))
	require.NoError(t, runtime.RunAuditBatch(ctx, 32))
	require.NoError(t, runtime.RunAuditBatch(ctx, 32))
	require.NoError(t, logDB.Order("id").Find(&logs).Error)
	require.Len(t, logs, 2)
	assert.Equal(t, model.LogTypeConsume, logs[1].Type)
	assert.Equal(t, 100, logs[1].Quota)
	checkMoney(t, svc, req, 900, 100, 1)
}
