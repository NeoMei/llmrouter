package model

import (
	"context"
	"errors"
	"fmt"
	userkit "github.com/NeoMei/UniHub/packages/userkit-go"
	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"strconv"
	"time"
)

type ProductCreditAdjustment struct {
	ID                      string `gorm:"primaryKey;size:64"`
	ScopeDigest, BodyDigest string `gorm:"size:64"`
	ActorID, UserID         int
	Request                 string `gorm:"type:text"`
	Receipt                 string `gorm:"type:text"`
	State                   string `gorm:"size:24"`
	CreatedAt, UpdatedAt    int64
}

// Adjust preserves the original CAS parameters across a lost HTTP response.
// The actor is freshly reauthorized for every attempt; its token is never stored.
func (s *ProductFundingStore) Adjust(ctx context.Context, actorToken string, actorID, userID int, key, mode string, value int) (userkit.RuntimeAdjustment, error) {
	var result userkit.RuntimeAdjustment
	if key == "" || len(key) > 128 || actorID <= 0 || userID <= 0 || value < 0 || value > common.MaxQuota {
		return result, ErrProductFundingConflict
	}
	a, err := s.Account(ctx, userID)
	if err != nil {
		return result, err
	}
	body := productDigest(fmt.Sprintf("%s:%d:%d:%s:%s:%d", s.ScopeDigest(), actorID, userID, a.AccountID, mode, value))
	id := productDigest(fmt.Sprintf("adjust:%s:%d:%s", s.ScopeDigest(), actorID, key))
	var op ProductCreditAdjustment
	err = s.db.WithContext(ctx).First(&op, "id = ?", id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		req := userkit.RuntimeAdjustmentRequest{RequestKey: id, LocalUserID: strconv.Itoa(userID), AccountID: a.AccountID, Reason: "llmrouter administrator " + mode, Asset: a.Asset, UnitVersion: a.UnitVersion}
		switch mode {
		case "add":
			if value == 0 {
				return result, ErrProductFundingConflict
			}
			req.Amount = strconv.Itoa(value)
		case "subtract":
			if value == 0 {
				return result, ErrProductFundingConflict
			}
			req.Amount = strconv.Itoa(-value)
		case "override":
			balance, e := s.Balance(ctx, userID)
			if e != nil {
				return result, e
			}
			target := strconv.Itoa(value)
			req.SetAvailable = &target
			req.ExpectedAvailable = strconv.Itoa(balance)
		default:
			return result, ErrProductFundingConflict
		}
		raw, e := common.Marshal(req)
		if e != nil {
			return result, e
		}
		candidate := ProductCreditAdjustment{ID: id, ScopeDigest: s.ScopeDigest(), BodyDigest: body, ActorID: actorID, UserID: userID, Request: string(raw), State: "pending", CreatedAt: time.Now().Unix()}
		if err = s.db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&candidate).Error; err != nil {
			return result, err
		}
		err = s.db.WithContext(ctx).First(&op, "id = ?", id).Error
	}
	if err != nil {
		return result, err
	}
	if op.ScopeDigest != s.ScopeDigest() || op.BodyDigest != body {
		return result, ErrProductFundingConflict
	}
	var req userkit.RuntimeAdjustmentRequest
	if err = common.UnmarshalJsonStr(op.Request, &req); err != nil {
		return result, err
	}
	result, err = s.client.AdjustCredit(ctx, actorToken, req)
	if err != nil {
		if productRuntimeDefiniteReject(err) {
			receiptCtx, stop := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
			_ = s.db.WithContext(receiptCtx).Model(&op).Updates(map[string]any{"state": "rejected", "updated_at": time.Now().Unix()}).Error
			stop()
		}
		return result, err
	}
	if result.Status != "APPLIED" || result.AccountID != a.AccountID {
		return result, ErrProductFundingPending
	}
	raw, err := common.Marshal(result)
	if err != nil {
		return result, err
	}
	err = s.db.WithContext(ctx).Model(&op).Updates(map[string]any{"state": "applied", "receipt": string(raw), "updated_at": time.Now().Unix()}).Error
	return result, err
}
