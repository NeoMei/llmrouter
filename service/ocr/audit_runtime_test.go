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
