package model

import (
	"context"
	"errors"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	ErrOCRLogUnsupported = errors.New("OCR consumption-log projection requires SQLite, MySQL or PostgreSQL; authoritative audit retained")
	ErrOCRLogDisabled    = errors.New("OCR consumption logging disabled; authoritative audit retained pending delivery")
)

// OCRLogReceipt belongs to LogDB, not the financial journal. Keep receipts when
// ordinary logs are purged: LogID records a completed projection even after that
// historical Log is deleted. Never reuse an event ID or cascade log deletion.
type OCRLogReceipt struct {
	EventID string `gorm:"primaryKey;size:36"`
	LogID   int
}

// ocrConsumeLoggingEnabled snapshots the live option using the same mutex as
// updateOptionMap. Release it before database work; startup/test assignment of
// the existing bool remains compatible when workers are not running.
func ocrConsumeLoggingEnabled() bool {
	common.OptionMapRWMutex.RLock()
	enabled := common.LogConsumeEnabled
	common.OptionMapRWMutex.RUnlock()
	return enabled
}

// DeliverAuditLog projects only allowlisted audit/lifecycle facts. Main-database
// reads finish before the LogDB transaction. Retrying after either database loses
// its commit acknowledgement cannot replay funding, counters or supplier work.
func (s *OCRBillingStore) DeliverAuditLog(ctx context.Context, logDB *gorm.DB, event OCRAuditEvent) error {
	if logDB == nil {
		return errors.New("OCR consumption-log database unavailable; authoritative audit retained")
	}
	switch logDB.Dialector.Name() {
	case "sqlite", "mysql", "postgres":
	default:
		return ErrOCRLogUnsupported
	}
	if event.Kind == "success" && !ocrConsumeLoggingEnabled() {
		return ErrOCRLogDisabled
	}
	if event.ID == "" || len(event.ID) > 36 {
		return ErrOCRConflict
	}
	var payload struct {
		UserID    int    `json:"user_id"`
		TokenID   int    `json:"token_id"`
		Model     string `json:"model"`
		Rejection struct {
			AdminInfo struct {
				Saturation *struct {
					Op      string                `json:"op"`
					Kind    common.QuotaClampKind `json:"kind"`
					Clamped int                   `json:"clamped"`
				} `json:"quota_saturation"`
			} `json:"admin_info"`
		} `json:"rejection"`
	}
	if err := common.UnmarshalJsonStr(event.Payload, &payload); err != nil {
		return errors.New("OCR audit payload invalid; authoritative audit retained")
	}
	log := Log{CreatedAt: event.CreatedAt, Type: LogTypeError, Content: "OCR " + event.Kind, RequestId: "ocr-audit-" + event.ID}
	other := map[string]any{"operation": "ocr.jobs.create", "outcome": event.Kind, "billing_unit": "request"}
	if event.Kind == "admission_rejected" {
		log.UserId, log.TokenId, log.ModelName = payload.UserID, payload.TokenID, payload.Model
		// Never copy arbitrary rejection/admin fields, even from a durable payload.
		if clamp := payload.Rejection.AdminInfo.Saturation; clamp != nil {
			if (clamp.Op == "QuotaFromFloat" || clamp.Op == "QuotaRound" || clamp.Op == "QuotaFromDecimal") && (clamp.Kind == common.QuotaClampOverflow || clamp.Kind == common.QuotaClampUnderflow || clamp.Kind == common.QuotaClampNaN) {
				other["admin_info"] = map[string]any{"quota_saturation": clamp}
			}
		}
	} else {
		if event.Kind != "success" && event.Kind != "failure" && event.Kind != "cancel" {
			return ErrOCRConflict
		}
		var job OCRJob
		if err := s.db.WithContext(ctx).First(&job, "id = ?", event.JobID).Error; err != nil {
			return err
		}
		if job.Decision != event.Kind || job.Quota < 0 || job.Quota > common.MaxQuota {
			return ErrOCRConflict
		}
		expected := "refunded"
		if event.Kind == "success" {
			expected = "settled"
		}
		if job.BillingState != expected {
			return ErrOCRConflict
		}
		var admission OCRAdmission
		if err := common.UnmarshalJsonStr(job.Contract, &admission); err != nil {
			return errors.New("OCR billing snapshot invalid; authoritative audit retained")
		}
		log.UserId, log.TokenId, log.ChannelId = job.UserID, job.TokenID, job.ChannelID
		log.ModelName, log.Group = job.Model, job.BillingGroup
		if event.Kind == "success" {
			log.Type, log.Quota = LogTypeConsume, job.Quota
		}
		// Duration is measured only from durable lifecycle timestamps. Keep the
		// int32 SQL column bounded even for malformed/ancient imported timestamps.
		if event.CreatedAt > job.CreatedAt && job.CreatedAt >= 0 {
			duration := event.CreatedAt - job.CreatedAt
			if duration > common.MaxQuota {
				duration = common.MaxQuota
			}
			log.UseTime = int(duration)
		}
		other["request_count"] = 0
		if event.Kind == "success" {
			other["request_count"] = 1
		}
		other["model_price"], other["group_ratio"], other["quota_per_unit"] = admission.Price, admission.GroupRatio, admission.QuotaPerUnit
		other["billing_source"], other["billing_preference"] = job.Funding, job.Preference
		other["started_at"], other["completed_at"] = job.CreatedAt, event.CreatedAt
	}
	if log.UserId <= 0 || log.TokenId <= 0 || len(log.ModelName) > 128 {
		return ErrOCRConflict
	}
	// Read only display columns via SQL. Missing/soft-deleted records have empty
	// names; transient lookup failures retain the event rather than hide it.
	var user User
	if err := s.db.WithContext(ctx).Select("username").First(&user, log.UserId).Error; err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	log.Username = user.Username
	var token Token
	if err := s.db.WithContext(ctx).Select("name").Where("id = ? AND user_id = ?", log.TokenId, log.UserId).First(&token).Error; err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	log.TokenName = token.Name
	raw, err := common.Marshal(other)
	if err != nil {
		return err
	}
	log.Other = string(raw)
	return logDB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		receipt := OCRLogReceipt{EventID: event.ID}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&receipt).Error; err != nil {
			return err
		}
		// RowsAffected is deliberately irrelevant (including MySQL clientFoundRows).
		if err := lockForUpdate(tx).First(&receipt, "event_id = ?", event.ID).Error; err != nil {
			return err
		}
		if receipt.LogID != 0 {
			return nil
		}
		if err := tx.Create(&log).Error; err != nil {
			return err
		}
		return tx.Model(&OCRLogReceipt{}).Where("event_id = ?", event.ID).Update("log_id", log.Id).Error
	})
}

// ProjectAuditLogs is a bounded repeatable outbox drain. Consumption disabled
// leaves success events pending; error/cancel/rejection events continue through.
// LogDB receipt+Log commit before main acknowledgement, with no cross-DB lock.
func (s *OCRBillingStore) ProjectAuditLogs(ctx context.Context, logDB *gorm.DB, limit int) error {
	if limit < 1 || limit > 100 {
		return errors.New("invalid OCR audit projection batch size")
	}
	if logDB == nil {
		return errors.New("OCR consumption-log database unavailable; authoritative audit retained")
	}
	switch logDB.Dialector.Name() {
	case "sqlite", "mysql", "postgres":
	default:
		return ErrOCRLogUnsupported
	}
	query := s.db.WithContext(ctx).Where("projected_at = ?", 0)
	if !ocrConsumeLoggingEnabled() {
		query = query.Where("kind <> ?", "success")
	}
	return s.projectAudit(ctx, query, limit, func(ctx context.Context, event OCRAuditEvent) error {
		return s.DeliverAuditLog(ctx, logDB, event)
	})
}
