package model

import (
	"context"
	"strconv"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/metering"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// NativeUsageReceipt stays in MainDB with the dashboard row it acknowledges.
// Retain it when usage history is purged so an audit retry cannot recreate it.
type NativeUsageReceipt struct {
	EventID     string `gorm:"primaryKey;size:36"`
	QuotaDataID int
}

func nativeUsageTokens(usage metering.Usage) (prompt, completion int) {
	for _, measurement := range usage.Measurements {
		if usage.State != "confirmed" || measurement.Unit != "token" || measurement.Quantity.Scale != 0 {
			continue
		}
		n, err := strconv.ParseInt(measurement.Quantity.Value, 10, 32)
		if err != nil || n < 0 {
			continue
		}
		target := &prompt
		switch measurement.Key {
		case "input", "cache_read", "cache_write":
		case "output", "reasoning":
			target = &completion
		default:
			continue
		}
		if n > int64(common.MaxQuota-*target) {
			*target = common.MaxQuota
		} else {
			*target += int(n)
		}
	}
	return
}

func (s *OCRBillingStore) projectNativeUsage(ctx context.Context, event OCRAuditEvent, log Log) error {
	if !common.DataExportEnabled {
		return nil
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		receipt := NativeUsageReceipt{EventID: event.ID}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&receipt).Error; err != nil {
			return err
		}
		if err := lockForUpdate(tx).First(&receipt, "event_id = ?", event.ID).Error; err != nil {
			return err
		}
		if receipt.QuotaDataID != 0 {
			return nil
		}
		tokens := log.PromptTokens
		if log.CompletionTokens > common.MaxQuota-tokens {
			tokens = common.MaxQuota
		} else {
			tokens += log.CompletionTokens
		}
		row := QuotaData{UserID: log.UserId, Username: log.Username, ModelName: log.ModelName, CreatedAt: event.CreatedAt - event.CreatedAt%3600,
			UseGroup: log.Group, TokenID: log.TokenId, ChannelID: log.ChannelId, NodeName: common.NodeName, TokenUsed: tokens, Count: 1, Quota: log.Quota}
		if err := tx.Create(&row).Error; err != nil {
			return err
		}
		return tx.Model(&NativeUsageReceipt{}).Where("event_id = ?", event.ID).Update("quota_data_id", row.Id).Error
	})
}
