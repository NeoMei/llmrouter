package model

import (
	"context"
	"errors"

	userkit "github.com/NeoMei/UniHub/packages/userkit-go"
	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

// An applied adjustment's committed log snapshot is its cross-database outbox.
// The destination SQL row cannot supply its own parent or financial provenance.
func readProductAdjustmentTelemetryLog(ctx context.Context, log *Log, op ProductCallAdjustment, payer ProductFundingAccount, digest string) (ProductRuntimeTelemetryLog, bool, error) {
	empty := ProductRuntimeTelemetryLog{}
	if op.State != "applied" || op.ID != log.RequestId || op.LogJSON == "" || op.Delta == 0 || op.BaseQuota < 0 || op.ActualQuota < 0 || op.BaseQuota > common.MaxQuota || op.ActualQuota > common.MaxQuota || op.ActualQuota-op.BaseQuota != op.Delta {
		return empty, false, nil
	}
	var snapshot Log
	if common.UnmarshalJsonStr(op.LogJSON, &snapshot) != nil {
		return empty, false, nil
	}
	snapshot.Id, snapshot.ChannelName = log.Id, log.ChannelName
	if snapshot != *log || log.Quota <= 0 || (op.Delta > 0 && (log.Type != LogTypeConsume || log.Quota != op.Delta)) || (op.Delta < 0 && (log.Type != LogTypeRefund || log.Quota != -op.Delta)) {
		return empty, false, nil
	}
	var parent ProductRuntimeCall
	if err := DB.WithContext(ctx).First(&parent, "id = ?", op.ParentCallID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return empty, false, nil
		}
		return empty, false, err
	}
	if parent.State != "settled" || parent.ParentCallID != "" || parent.ScopeDigest != digest || parent.UserID != log.UserId || parent.TokenID != log.TokenId || parent.AccountID != payer.AccountID {
		return empty, false, nil
	}
	contract, err := ProductRuntimeCallContract(parent)
	if err != nil {
		return empty, false, nil
	}
	if op.Delta > 0 {
		if op.ChildCallID != "adj_"+op.ID {
			return empty, false, nil
		}
		var child ProductRuntimeCall
		if err = DB.WithContext(ctx).First(&child, "id = ?", op.ChildCallID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return empty, false, nil
			}
			return empty, false, err
		}
		childContract, err := ProductRuntimeCallContract(child)
		if err != nil || child.FinalQuota != op.Delta || !productTelemetryChildMatchesParent(child, parent, childContract, contract, op.BaseQuota) {
			return empty, false, nil
		}
		var count int64
		if err = DB.WithContext(ctx).Model(&ProductCallChargeSegment{}).Where("call_id = ? AND parent_call_id = ?", child.ID, parent.ID).Count(&count).Error; err != nil {
			return empty, false, err
		}
		if count != 1 {
			return empty, false, nil
		}
	} else {
		var refunds []ProductCallRefund
		if err = DB.WithContext(ctx).Where("request_key = ? AND state = ?", op.ID, "applied").Find(&refunds).Error; err != nil {
			return empty, false, err
		}
		var refunded int64
		for _, refund := range refunds {
			if refund.Amount <= 0 || refund.Amount > common.MaxQuota {
				return empty, false, nil
			}
			var call ProductRuntimeCall
			if err = DB.WithContext(ctx).First(&call, "id = ?", refund.CallID).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return empty, false, nil
				}
				return empty, false, err
			}
			refundContract, err := ProductRuntimeCallContract(call)
			if err != nil || call.State != "settled" || call.ScopeDigest != digest || call.UserID != parent.UserID || call.TokenID != parent.TokenID || call.AccountID != parent.AccountID || refundContract.Asset != contract.Asset || refundContract.UnitVersion != contract.UnitVersion {
				return empty, false, nil
			}
			if call.ID != parent.ID {
				var source ProductCallAdjustment
				if err = DB.WithContext(ctx).First(&source, "child_call_id = ? AND parent_call_id = ? AND state = ?", call.ID, parent.ID, "applied").Error; err != nil {
					if errors.Is(err, gorm.ErrRecordNotFound) {
						return empty, false, nil
					}
					return empty, false, err
				}
				if source.Delta <= 0 || source.Delta != call.FinalQuota || !productTelemetryChildMatchesParent(call, parent, refundContract, contract, source.BaseQuota) {
					return empty, false, nil
				}
				var count int64
				if err = DB.WithContext(ctx).Model(&ProductCallChargeSegment{}).Where("call_id = ? AND parent_call_id = ?", call.ID, parent.ID).Count(&count).Error; err != nil {
					return empty, false, err
				}
				if count != 1 {
					return empty, false, nil
				}
			}
			refunded += int64(refund.Amount)
			if refunded > int64(-op.Delta) {
				return empty, false, nil
			}
		}
		if refunded != int64(-op.Delta) {
			return empty, false, nil
		}
	}
	return ProductRuntimeTelemetryLog{Log: log, AccountID: parent.AccountID, State: op.State, Asset: contract.Asset, UnitVersion: contract.UnitVersion, Adjustment: true, ParentCallID: parent.ID, BillingSource: "unihub"}, true, nil
}

// A supplemental segment inherits the accepted obligation's price. Its own
// internally valid contract cannot select a new rate, model or rounding base.
func productTelemetryChildMatchesParent(child, parent ProductRuntimeCall, childContract, parentContract userkit.RuntimeReserveRequest, base int) bool {
	if child.State != "settled" || child.ParentCallID != parent.ID || child.UserID != parent.UserID || child.TokenID != parent.TokenID || child.AccountID != parent.AccountID || child.ScopeDigest != parent.ScopeDigest || !child.ExternalCounters || !child.FinancialCommitment || childContract.Asset != parentContract.Asset || childContract.UnitVersion != parentContract.UnitVersion || childContract.Model != parentContract.Model || childContract.Operation != parentContract.Operation || childContract.PriceVersion != parentContract.PriceVersion || childContract.PricingDigest != parentContract.PricingDigest {
		return false
	}
	if parentContract.Asset == userkit.RuntimeCashAsset {
		return child.CashBaseQuota == base && child.CashRoundingVersion == parent.CashRoundingVersion && child.ConversionVersion == parent.ConversionVersion && child.ConversionNumerator == parent.ConversionNumerator && child.ConversionDenominator == parent.ConversionDenominator && child.SnapshotDigest == parent.SnapshotDigest
	}
	return true
}
