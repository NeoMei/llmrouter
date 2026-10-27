package model

import (
	"context"
	"gorm.io/gorm"
)

// The task marker and channel counters commit together after the idempotent
// central financial operation. Retries cannot repeat the local accounting.
func ApplyProductTaskQuota(ctx context.Context, task *Task, target int) (changed bool, err error) {
	err = DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var current Task
		if e := lockForUpdate(tx).First(&current, task.ID).Error; e != nil {
			return e
		}
		if current.ProductCallID != task.ProductCallID {
			return ErrProductFundingConflict
		}
		if current.ProductBillingVersion != task.ProductBillingVersion {
			task.Quota = current.Quota
			task.ProductBillingVersion = current.ProductBillingVersion
			return nil
		}
		delta := target - current.Quota
		if delta == 0 {
			return nil
		}
		if e := tx.Model(&current).Updates(map[string]any{"quota": target, "product_billing_version": current.ProductBillingVersion + 1}).Error; e != nil {
			return e
		}
		if e := tx.Model(&Channel{}).Where("id = ?", current.ChannelId).Update("used_quota", gorm.Expr("used_quota + ?", delta)).Error; e != nil {
			return e
		}
		task.Quota = target
		task.ProductBillingVersion = current.ProductBillingVersion + 1
		changed = true
		return nil
	})
	return
}
func ApplyProductMidjourneyQuota(ctx context.Context, task *Midjourney, target int) (changed bool, err error) {
	err = DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var current Midjourney
		if e := lockForUpdate(tx).First(&current, task.Id).Error; e != nil {
			return e
		}
		if current.ProductCallID != task.ProductCallID {
			return ErrProductFundingConflict
		}
		if current.ProductBillingVersion != task.ProductBillingVersion {
			task.Quota = current.Quota
			task.ProductBillingVersion = current.ProductBillingVersion
			return nil
		}
		delta := target - current.Quota
		if delta == 0 {
			return nil
		}
		if e := tx.Model(&current).Updates(map[string]any{"quota": target, "product_billing_version": current.ProductBillingVersion + 1}).Error; e != nil {
			return e
		}
		if e := tx.Model(&Channel{}).Where("id = ?", current.GetBillingChannelId()).Update("used_quota", gorm.Expr("used_quota + ?", delta)).Error; e != nil {
			return e
		}
		task.Quota = target
		task.ProductBillingVersion = current.ProductBillingVersion + 1
		changed = true
		return nil
	})
	return
}
