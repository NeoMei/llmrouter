package model

import (
	"context"
	"errors"
	"fmt"
	userkit "github.com/NeoMei/UniHub/packages/userkit-go"
	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
	"strconv"
	"time"
)

type ProductCallRefund struct {
	ID                                string `gorm:"primaryKey;size:128"`
	CallID                            string `gorm:"index;size:128"`
	RequestKey, RequestDigest, Reason string
	Amount                            int
	State                             string `gorm:"index;size:24"`
	CreatedAt, UpdatedAt              int64
}
type ProductCallAdjustment struct {
	ID                                             string `gorm:"primaryKey;size:128"`
	ParentCallID                                   string `gorm:"index;size:128"`
	RequestKey, RequestDigest, Reason, ChildCallID string
	ActualQuota, BaseQuota, Delta                  int
	State                                          string `gorm:"index;size:24"`
	LogJSON                                        string `gorm:"type:text"`
	AuditJSON                                      string `gorm:"type:text"`
	LogDeliveredAt                                 int64
	CreatedAt, UpdatedAt                           int64
}
type ProductCallChargeSegment struct {
	CallID       string `gorm:"primaryKey;size:128"`
	ParentCallID string `gorm:"index;size:128"`
	CreatedAt    int64
}

func RegisterProductRefundModels(db *gorm.DB) error {
	return db.AutoMigrate(&ProductCallRefund{}, &ProductCallAdjustment{}, &ProductCallChargeSegment{})
}

func (s *ProductFundingStore) Refund(ctx context.Context, callID, key string, amount int, reason string) error {
	if callID == "" || key == "" || reason == "" || amount <= 0 || amount > common.MaxQuota {
		return ErrProductFundingConflict
	}
	id := productDigest("refund:" + callID + ":" + key)
	digest := productDigest(fmt.Sprintf("%s:%d:%s", callID, amount, reason))
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var call ProductRuntimeCall
		if e := lockForUpdate(tx).First(&call, "id = ?", callID).Error; e != nil {
			return e
		}
		if call.State != "settled" {
			return ErrProductFundingPending
		}
		if call.ScopeDigest == "" || call.ScopeDigest != s.ScopeDigest() {
			return ErrProductFundingConflict
		}
		if call.TokenID > 0 {
			var token Token
			if err := lockForUpdate(tx).Unscoped().First(&token, call.TokenID).Error; err != nil {
				return err
			}
			if token.AuthRealm == NativeFundingLocalOnly {
				return ErrProductFundingConflict
			}
		}
		parentID := callID
		var segment ProductCallChargeSegment
		if e := tx.First(&segment, "call_id = ?", callID).Error; e == nil {
			parentID = segment.ParentCallID
		} else if !errors.Is(e, gorm.ErrRecordNotFound) {
			return e
		}
		if parentID != callID {
			var parent ProductRuntimeCall
			if e := lockForUpdate(tx).First(&parent, "id = ?", parentID).Error; e != nil {
				return e
			}
			if parent.ScopeDigest != s.ScopeDigest() {
				return ErrProductFundingConflict
			}
		}
		var changing int64
		if e := tx.Model(&ProductCallAdjustment{}).Where("parent_call_id = ? AND state = ? AND id <> ?", parentID, "pending", key).Count(&changing).Error; e != nil {
			return e
		}
		if changing > 0 {
			return ErrProductFundingPending
		}
		var old ProductCallRefund
		e := tx.First(&old, "id = ?", id).Error
		if e == nil {
			if old.RequestDigest != digest {
				return ErrProductFundingConflict
			}
			return nil
		}
		if !errors.Is(e, gorm.ErrRecordNotFound) {
			return e
		}
		var total int64
		if e = tx.Model(&ProductCallRefund{}).Select("COALESCE(SUM(amount),0)").Where("call_id = ? AND state IN ?", callID, []string{"pending", "applied"}).Scan(&total).Error; e != nil {
			return e
		}
		if total+int64(amount) > int64(call.FinalQuota) {
			return ErrProductFundingConflict
		}
		return tx.Create(&ProductCallRefund{ID: id, CallID: callID, RequestKey: key, RequestDigest: digest, Reason: reason, Amount: amount, State: "pending", CreatedAt: time.Now().Unix()}).Error
	})
	if err != nil {
		return err
	}
	return s.resumeProductRefund(ctx, id)
}
func (s *ProductFundingStore) resumeProductRefund(ctx context.Context, id string) error {
	var receipt ProductCallRefund
	if e := s.db.WithContext(ctx).First(&receipt, "id = ?", id).Error; e != nil {
		return e
	}
	if receipt.State == "applied" {
		return nil
	}
	var call ProductRuntimeCall
	if e := s.db.WithContext(ctx).First(&call, "id = ?", receipt.CallID).Error; e != nil {
		return e
	}
	if call.ScopeDigest == "" || call.ScopeDigest != s.ScopeDigest() {
		return ErrProductFundingConflict
	}
	if call.TokenID > 0 {
		var token Token
		if err := s.db.WithContext(ctx).Unscoped().First(&token, call.TokenID).Error; err != nil {
			return err
		}
		if token.AuthRealm == NativeFundingLocalOnly {
			return ErrProductFundingConflict
		}
	}
	out, e := s.client.Refund(ctx, receipt.CallID, userkit.RuntimeRefundRequest{RequestKey: receipt.ID, Amount: strconv.Itoa(receipt.Amount), Reason: receipt.Reason})
	if e != nil {
		return e
	}
	if out.Status != "APPLIED" || out.AccountID != call.AccountID || out.Amount != strconv.Itoa(receipt.Amount) {
		return ErrProductFundingPending
	}
	e = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var user User
		if e := lockForUpdate(tx).Unscoped().First(&user, call.UserID).Error; e != nil {
			return e
		}
		var current ProductCallRefund
		if e := lockForUpdate(tx).First(&current, "id = ?", id).Error; e != nil {
			return e
		}
		if current.State == "applied" {
			return nil
		}
		if current.RequestDigest != receipt.RequestDigest {
			return ErrProductFundingConflict
		}
		if call.TokenID > 0 {
			var token Token
			if e := lockForUpdate(tx).Unscoped().First(&token, call.TokenID).Error; e != nil {
				return e
			}
			if token.AuthRealm == NativeFundingLocalOnly {
				return ErrProductFundingConflict
			}
			remain := int64(token.RemainQuota) + int64(receipt.Amount)
			if remain > int64(common.MaxQuota) {
				remain = int64(common.MaxQuota)
			}
			used := token.UsedQuota - receipt.Amount
			if used < 0 {
				used = 0
			}
			if e := tx.Unscoped().Model(&token).Updates(map[string]any{"remain_quota": remain, "used_quota": used}).Error; e != nil {
				return e
			}
		}
		used := user.UsedQuota - receipt.Amount
		if used < 0 {
			used = 0
		}
		if e := tx.Unscoped().Model(&user).Update("used_quota", used).Error; e != nil {
			return e
		}
		return tx.Model(&current).Updates(map[string]any{"state": "applied", "updated_at": time.Now().Unix()}).Error
	})
	if e == nil {
		_ = InvalidateUserTokensCache(call.UserID)
		_ = invalidateUserCache(call.UserID)
	}
	return e
}
func (s *ProductFundingStore) RecoverRefunds(ctx context.Context) error {
	var first error
	var refunds []ProductCallRefund
	if e := s.db.WithContext(ctx).Where("state = ?", "pending").Order("updated_at, id").Find(&refunds).Error; e != nil {
		return e
	}
	refundCtx, stopRefunds := productRecoveryFirstPhase(ctx)
	for _, r := range refunds {
		if refundCtx.Err() != nil {
			break
		}
		if e := s.db.WithContext(refundCtx).Model(&ProductCallRefund{}).Where("id = ? AND state = ?", r.ID, "pending").Update("updated_at", time.Now().Unix()).Error; e != nil {
			if first == nil {
				first = e
			}
			continue
		}
		if e := s.resumeProductRefund(refundCtx, r.ID); e != nil && first == nil {
			first = e
		}
	}
	stopRefunds()
	var adjustments []ProductCallAdjustment
	if e := s.db.WithContext(ctx).Where("state = ? OR (state = ? AND log_json <> '' AND log_delivered_at = 0)", "pending", "applied").Order("updated_at, id").Find(&adjustments).Error; e != nil {
		return e
	}
	for _, a := range adjustments {
		if ctx.Err() != nil {
			break
		}
		if e := s.db.WithContext(ctx).Model(&ProductCallAdjustment{}).Where("id = ?", a.ID).Update("updated_at", time.Now().Unix()).Error; e != nil {
			if first == nil {
				first = e
			}
			continue
		}
		if e := s.resumeProductAdjustment(ctx, a.ID); e != nil && first == nil {
			first = e
		}
	}
	return first
}
func RefundProductCall(ctx context.Context, callID, key string, amount int, reason string) error {
	s, e := productFundingStore()
	if e != nil {
		return e
	}
	return s.Refund(ctx, callID, key, amount, reason)
}
func RepriceProductCall(ctx context.Context, callID, key string, actual int, reason string, audit ...RecordTaskBillingLogParams) error {
	s, e := productFundingStore()
	if e != nil {
		return e
	}
	return s.Reprice(ctx, callID, key, actual, reason, audit...)
}
func RecoverProductRefunds(ctx context.Context) error {
	if DB == nil || !DB.Migrator().HasTable(&ProductCallRefund{}) {
		return nil
	}
	s, e := productFundingStore()
	if e != nil {
		return e
	}
	return s.RecoverRefunds(ctx)
}
func (s *ProductFundingStore) productSegments(ctx context.Context, tx *gorm.DB, parent string) ([]ProductRuntimeCall, int, error) {
	var segments []ProductCallChargeSegment
	if e := tx.WithContext(ctx).Where("parent_call_id = ?", parent).Order("created_at DESC,call_id DESC").Find(&segments).Error; e != nil {
		return nil, 0, e
	}
	ids := []string{}
	for _, segment := range segments {
		ids = append(ids, segment.CallID)
	}
	ids = append(ids, parent)
	out := []ProductRuntimeCall{}
	total := 0
	for _, id := range ids {
		var call ProductRuntimeCall
		if e := tx.First(&call, "id = ?", id).Error; e != nil {
			if errors.Is(e, gorm.ErrRecordNotFound) {
				continue
			}
			return nil, 0, e
		}
		if call.State != "settled" {
			continue
		}
		var refunded int64
		if e := tx.Model(&ProductCallRefund{}).Select("COALESCE(SUM(amount),0)").Where("call_id = ? AND state = ?", id, "applied").Scan(&refunded).Error; e != nil {
			return nil, 0, e
		}
		call.FinalQuota -= int(refunded)
		total += call.FinalQuota
		out = append(out, call)
	}
	return out, total, nil
}
func (s *ProductFundingStore) Reprice(ctx context.Context, parent, key string, actual int, reason string, audit ...RecordTaskBillingLogParams) error {
	if parent == "" || key == "" || reason == "" || actual < 0 || actual > common.MaxQuota {
		return ErrProductFundingConflict
	}
	id := productDigest("adjust:" + parent + ":" + key)
	digest := productDigest(fmt.Sprintf("%s:%d:%s", parent, actual, reason))
	auditJSON := ""
	if len(audit) > 0 {
		raw, e := common.Marshal(audit[0])
		if e != nil {
			return e
		}
		auditJSON = string(raw)
	}
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var call ProductRuntimeCall
		if e := lockForUpdate(tx).First(&call, "id = ?", parent).Error; e != nil {
			return e
		}
		if call.State != "settled" {
			return ErrProductFundingPending
		}
		var old ProductCallAdjustment
		e := tx.First(&old, "id = ?", id).Error
		if e == nil {
			if old.RequestDigest != digest {
				return ErrProductFundingConflict
			}
			return nil
		}
		if !errors.Is(e, gorm.ErrRecordNotFound) {
			return e
		}
		var pending int64
		if e = tx.Model(&ProductCallAdjustment{}).Where("parent_call_id = ? AND state = ?", parent, "pending").Count(&pending).Error; e != nil {
			return e
		}
		if pending > 0 {
			return ErrProductFundingPending
		}
		var refundPending int64
		if e = tx.Model(&ProductCallRefund{}).Where("state = ? AND (call_id = ? OR call_id IN (SELECT call_id FROM product_call_charge_segments WHERE parent_call_id = ?))", "pending", parent, parent).Count(&refundPending).Error; e != nil {
			return e
		}
		if refundPending > 0 {
			return ErrProductFundingPending
		}
		_, total, e := s.productSegments(ctx, tx, parent)
		if e != nil {
			return e
		}
		op := ProductCallAdjustment{ID: id, ParentCallID: parent, RequestKey: key, RequestDigest: digest, Reason: reason, ActualQuota: actual, BaseQuota: total, Delta: actual - total, State: "pending", AuditJSON: auditJSON, CreatedAt: time.Now().Unix()}
		if op.Delta > 0 {
			op.ChildCallID = "adj_" + id
			if e = tx.Create(&ProductCallChargeSegment{CallID: op.ChildCallID, ParentCallID: parent, CreatedAt: time.Now().Unix()}).Error; e != nil {
				return e
			}
		}
		return tx.Create(&op).Error
	})
	if err != nil {
		return err
	}
	return s.resumeProductAdjustment(ctx, id)
}
func (s *ProductFundingStore) resumeProductAdjustment(ctx context.Context, id string) error {
	var op ProductCallAdjustment
	if e := s.db.WithContext(ctx).First(&op, "id = ?", id).Error; e != nil {
		return e
	}
	if op.State == "applied" {
		return s.deliverProductAdjustmentLog(ctx, id)
	}
	var parent ProductRuntimeCall
	if e := s.db.WithContext(ctx).First(&parent, "id = ?", op.ParentCallID).Error; e != nil {
		return e
	}
	if parent.ScopeDigest == "" || parent.ScopeDigest != s.ScopeDigest() {
		return ErrProductFundingConflict
	}
	if op.Delta > 0 {
		var original userkit.RuntimeReserveRequest
		if e := common.UnmarshalJsonStr(parent.ReserveJSON, &original); e != nil {
			return e
		}
		account, e := s.Account(ctx, parent.UserID)
		if e != nil {
			return e
		}
		if account.AccountID != parent.AccountID {
			return ErrProductFundingConflict
		}
		var child ProductRuntimeCall
		e = s.db.WithContext(ctx).First(&child, "id = ?", op.ChildCallID).Error
		if e != nil && !errors.Is(e, gorm.ErrRecordNotFound) {
			return e
		}
		if child.State == "settle_pending" {
			if e = s.reconcile(ctx, child.ID); e != nil {
				return e
			}
			child.State = "settled"
		}
		if child.State != "settled" {
			_, e = s.Reserve(ctx, ProductFundingAdmission{ParentCallID: parent.ID, CallID: op.ChildCallID, UserID: parent.UserID, TokenID: parent.TokenID, Model: original.Model, Operation: original.Operation, PriceSnapshot: parent.ReserveJSON, Quota: op.Delta, ExternalCounters: true, FinancialCommitment: true})
			if e != nil {
				return e
			}
			if e = s.MarkDispatched(ctx, op.ChildCallID); e != nil {
				return e
			}
			usage := fmt.Sprintf(`{"parent_call_id":%q,"adjustment_id":%q,"delta":%d}`, parent.ID, op.ID, op.Delta)
			if e = s.Settle(ctx, op.ChildCallID, op.Delta, usage); e != nil {
				return e
			}
		}
	} else if op.Delta < 0 {
		segments, _, e := s.productSegments(ctx, s.db, op.ParentCallID)
		if e != nil {
			return e
		}
		remaining := -op.Delta
		// Every segment refund has a stable per-operation key. Subtract already
		// applied receipts when recovering an interrupted multi-segment refund.
		for _, segment := range segments {
			refundID := productDigest("refund:" + segment.ID + ":" + op.ID)
			var old ProductCallRefund
			if e = s.db.WithContext(ctx).First(&old, "id = ?", refundID).Error; e == nil {
				if e = s.resumeProductRefund(ctx, old.ID); e != nil {
					return e
				}
				remaining -= old.Amount
				continue
			} else if !errors.Is(e, gorm.ErrRecordNotFound) {
				return e
			}
			amount := segment.FinalQuota
			if amount > remaining {
				amount = remaining
			}
			if amount > 0 {
				if e = s.Refund(ctx, segment.ID, op.ID, amount, op.Reason); e != nil {
					return e
				}
				remaining -= amount
			}
			if remaining == 0 {
				break
			}
		}
		if remaining != 0 {
			return ErrProductFundingPending
		}
	}
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var user User
		if e := lockForUpdate(tx).Unscoped().First(&user, parent.UserID).Error; e != nil {
			return e
		}
		var current ProductCallAdjustment
		if e := lockForUpdate(tx).First(&current, "id = ?", id).Error; e != nil {
			return e
		}
		if current.State == "applied" {
			return nil
		}
		logJSON, e := productAdjustmentTaskAccounting(tx, parent, current, user)
		if e != nil {
			return e
		}
		if op.Delta > 0 {
			used := int64(user.UsedQuota) + int64(op.Delta)
			if used > int64(common.MaxQuota) {
				used = int64(common.MaxQuota)
			}
			if e := tx.Unscoped().Model(&user).Update("used_quota", used).Error; e != nil {
				return e
			}
		}
		return tx.Model(&current).Updates(map[string]any{"state": "applied", "log_json": logJSON, "updated_at": time.Now().Unix()}).Error
	})
	if err != nil {
		return err
	}
	_ = invalidateUserCache(parent.UserID)
	return s.deliverProductAdjustmentLog(ctx, id)
}

// The source marker, channel totals and durable log snapshot commit with the
// financial journal. Recovery never needs to poll or resend the supplier.
func productAdjustmentTaskAccounting(tx *gorm.DB, parent ProductRuntimeCall, op ProductCallAdjustment, user User) (string, error) {
	var task Task
	var mj Midjourney
	if tx.Migrator().HasTable(&Task{}) {
		if e := lockForUpdate(tx).Where("product_call_id = ?", parent.ID).First(&task).Error; e != nil && !errors.Is(e, gorm.ErrRecordNotFound) {
			return "", e
		}
	}
	if tx.Migrator().HasTable(&Midjourney{}) {
		if e := lockForUpdate(tx).Where("product_call_id = ?", parent.ID).First(&mj).Error; e != nil && !errors.Is(e, gorm.ErrRecordNotFound) {
			return "", e
		}
	}
	if task.ID == 0 && mj.Id == 0 {
		return "", nil
	}
	if task.ID != 0 && mj.Id != 0 {
		return "", ErrProductFundingConflict
	}
	var original userkit.RuntimeReserveRequest
	if e := common.UnmarshalJsonStr(parent.ReserveJSON, &original); e != nil {
		return "", e
	}
	log := Log{RequestId: op.ID, UserId: parent.UserID, Username: user.Username, CreatedAt: time.Now().Unix(), ModelName: original.Model, TokenId: parent.TokenID, Content: "任务费用调整"}
	other := map[string]any{"product_call_id": parent.ID, "adjustment_id": op.ID, "reason": op.Reason, "actual_quota": op.ActualQuota}
	delta := 0
	if task.ID != 0 {
		delta = op.ActualQuota - task.Quota
		log.ChannelId, log.Group = task.ChannelId, task.Group
		other["task_id"], other["pre_consumed_quota"] = task.TaskID, task.Quota
		if bc := task.PrivateData.BillingContext; bc != nil {
			other["model_price"], other["model_ratio"], other["group_ratio"] = bc.ModelPrice, bc.ModelRatio, bc.GroupRatio
			for k, v := range bc.OtherRatios {
				other[k] = v
			}
		}
		if delta != 0 {
			if e := tx.Model(&task).Updates(map[string]any{"quota": op.ActualQuota, "product_billing_version": task.ProductBillingVersion + 1}).Error; e != nil {
				return "", e
			}
		}
	} else {
		delta = op.ActualQuota - mj.Quota
		log.ChannelId = mj.GetBillingChannelId()
		other["task_id"], other["pre_consumed_quota"] = mj.MjId, mj.Quota
		if delta != 0 {
			if e := tx.Model(&mj).Updates(map[string]any{"quota": op.ActualQuota, "product_billing_version": mj.ProductBillingVersion + 1}).Error; e != nil {
				return "", e
			}
		}
	}
	if delta == 0 {
		return "", nil
	}
	if e := tx.Model(&Channel{}).Where("id = ?", log.ChannelId).Update("used_quota", gorm.Expr("used_quota + ?", delta)).Error; e != nil {
		return "", e
	}
	log.Type, log.Quota = LogTypeConsume, delta
	if delta < 0 {
		log.Type, log.Quota = LogTypeRefund, -delta
	}
	var token Token
	if e := tx.Unscoped().First(&token, parent.TokenID).Error; e == nil {
		log.TokenName = token.Name
	} else if !errors.Is(e, gorm.ErrRecordNotFound) {
		return "", e
	}
	log.Other = common.MapToJsonStr(other)
	nodeName := common.NodeName
	if op.AuditJSON != "" {
		var audit RecordTaskBillingLogParams
		if e := common.UnmarshalJsonStr(op.AuditJSON, &audit); e != nil {
			return "", e
		}
		log.Content, log.ModelName = audit.Content, audit.ModelName
		if audit.NodeName != "" {
			nodeName = audit.NodeName
		}
		for k, v := range audit.Other {
			other[k] = v
		}
		log.Other = common.MapToJsonStr(other)
	}
	if log.Type == LogTypeConsume && common.LogConsumeEnabled && common.DataExportEnabled {
		// Persist the original export dimensions with the journal rather than
		// relying on an in-memory callback after the financial commit.
		var row QuotaData
		hour := log.CreatedAt - log.CreatedAt%3600
		query := lockForUpdate(tx).Where("user_id = ? AND username = ? AND model_name = ? AND created_at = ? AND use_group = ? AND token_id = ? AND channel_id = ? AND node_name = ?", log.UserId, log.Username, log.ModelName, hour, log.Group, log.TokenId, log.ChannelId, nodeName)
		e := query.First(&row).Error
		if errors.Is(e, gorm.ErrRecordNotFound) {
			row = QuotaData{UserID: log.UserId, Username: log.Username, ModelName: log.ModelName, CreatedAt: hour, UseGroup: log.Group, TokenID: log.TokenId, ChannelID: log.ChannelId, NodeName: nodeName, Count: 1, Quota: log.Quota}
			e = tx.Create(&row).Error
		} else if e == nil {
			e = tx.Model(&row).Updates(map[string]any{"count": gorm.Expr("count + 1"), "quota": gorm.Expr("quota + ?", log.Quota)}).Error
		}
		if e != nil {
			return "", e
		}
	}
	raw, e := common.Marshal(log)
	return string(raw), e
}

func (s *ProductFundingStore) deliverProductAdjustmentLog(ctx context.Context, id string) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var op ProductCallAdjustment
		if e := lockForUpdate(tx).First(&op, "id = ?", id).Error; e != nil {
			return e
		}
		if op.State != "applied" || op.LogJSON == "" || op.LogDeliveredAt != 0 {
			return nil
		}
		if LOG_DB == nil {
			return ErrProductFundingPending
		}
		logDB := LOG_DB.WithContext(ctx)
		mainSQL, mainErr := s.db.DB()
		logSQL, logErr := LOG_DB.DB()
		if mainErr == nil && logErr == nil && mainSQL == logSQL {
			logDB = tx
		}
		var log Log
		if e := common.UnmarshalJsonStr(op.LogJSON, &log); e != nil {
			return e
		}
		if log.Type != LogTypeConsume || common.LogConsumeEnabled {
			// Keep the request id stable across an uncertain log DB response. A
			// single source-row lock serializes recovery and the foreground caller.
			var count int64
			if e := logDB.Model(&Log{}).Where("request_id = ?", log.RequestId).Count(&count).Error; e != nil {
				return e
			}
			if count == 0 {
				if e := logDB.Create(&log).Error; e != nil {
					return e
				}
			}
		}
		return tx.Model(&op).Update("log_delivered_at", time.Now().Unix()).Error
	})
}
