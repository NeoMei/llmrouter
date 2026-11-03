package model

import (
	"context"

	userkit "github.com/NeoMei/UniHub/packages/userkit-go"
	"github.com/QuantumNous/new-api/internal/platformaccount"
)

type ProductRuntimeTelemetryLog struct {
	Log                       *Log
	ActorID, AccountID, State string
	Asset, UnitVersion        string
	Adjustment                bool
	ParentCallID              string
	SubscriptionID            int
	PurchaseCallID            string
	BillingSource             string
}

// ReadProductRuntimeTelemetryLogs admits an original log only through its
// durable call, key, owner, payer and deployment. A shared SQL owner alone is
// never evidence that an old native log belongs to the central identity.
func ReadProductRuntimeTelemetryLogs(ctx context.Context, actorID string, userID int, allActors bool, start, end int64) ([]ProductRuntimeTelemetryLog, error) {
	result := make([]ProductRuntimeTelemetryLog, 0)
	if DB == nil {
		return nil, ErrProductFundingPending
	}
	if !platformaccount.RuntimeEnabled() || !DB.Migrator().HasTable(&ProductRuntimeCall{}) {
		return result, nil
	}
	config, err := platformaccount.Current()
	if err != nil {
		return nil, err
	}
	digest, err := ProductRuntimeScopeDigest()
	if err != nil {
		return nil, err
	}
	bindingsQuery := DB.WithContext(ctx).Where("issuer = ?", config.PublicOrigin)
	if !allActors {
		if userID <= 0 || actorID == "" {
			return nil, ErrUniHubProjectionInvalid
		}
		bindingsQuery = bindingsQuery.Where("user_id = ? AND subject = ?", userID, actorID)
	}
	var bindings []UniHubIdentityMapping
	if err = bindingsQuery.Find(&bindings).Error; err != nil {
		return nil, err
	}
	actors := make(map[int]string, len(bindings))
	userIDs := make([]int, 0, len(bindings))
	for _, binding := range bindings {
		actors[binding.UserID] = binding.Subject
		userIDs = append(userIDs, binding.UserID)
	}
	if len(userIDs) == 0 {
		return result, nil
	}
	var accounts []ProductFundingAccount
	if err = DB.WithContext(ctx).Where("user_id IN ? AND scope_digest = ? AND state = ? AND asset = ? AND unit_version = ?", userIDs, digest, "central", userkit.RuntimeAsset, userkit.RuntimeUnitVersion).Find(&accounts).Error; err != nil {
		return nil, err
	}
	payers := make(map[int]ProductFundingAccount, len(accounts))
	userIDs = userIDs[:0]
	for _, account := range accounts {
		if account.AccountID != "" {
			payers[account.UserID] = account
			userIDs = append(userIDs, account.UserID)
		}
	}
	if len(userIDs) == 0 {
		return result, nil
	}
	if LOG_DB == nil {
		return nil, ErrProductFundingPending
	}
	var logs []*Log
	if err = LOG_DB.WithContext(ctx).Where("user_id IN ? AND created_at >= ? AND created_at <= ? AND type IN ?", userIDs, start, end, []int{LogTypeConsume, LogTypeError, LogTypeSystem, LogTypeRefund}).Order("created_at DESC, request_id DESC").Find(&logs).Error; err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(logs))
	seen := make(map[string]bool, len(logs))
	for _, log := range logs {
		if log.RequestId != "" && !seen[log.RequestId] {
			seen[log.RequestId] = true
			ids = append(ids, log.RequestId)
		}
	}
	calls := make(map[string]ProductRuntimeCall, len(ids))
	adjustments := make(map[string]ProductCallAdjustment)
	subscriptionRequests := make(map[string]bool)
	hasAdjustments := DB.Migrator().HasTable(&ProductCallAdjustment{})
	hasSubscriptions := DB.Migrator().HasTable(&SubscriptionPreConsumeRecord{}) && DB.Migrator().HasTable(&ProductSubscriptionPurchase{})
	// MainDB and LogDB can be separate databases. Resolve bounded ID batches
	// rather than joining financial provenance to a potentially remote log DB.
	for offset := 0; offset < len(ids); offset += 500 {
		limit := min(offset+500, len(ids))
		var batch []ProductRuntimeCall
		if err = DB.WithContext(ctx).Where("id IN ? AND scope_digest = ?", ids[offset:limit], digest).Find(&batch).Error; err != nil {
			return nil, err
		}
		for _, call := range batch {
			calls[call.ID] = call
		}
		if hasAdjustments {
			var batch []ProductCallAdjustment
			if err = DB.WithContext(ctx).Where("id IN ? AND state = ?", ids[offset:limit], "applied").Find(&batch).Error; err != nil {
				return nil, err
			}
			for _, op := range batch {
				adjustments[op.ID] = op
			}
		}
		if hasSubscriptions {
			var batch []SubscriptionPreConsumeRecord
			if err = DB.WithContext(ctx).Where("request_id IN ?", ids[offset:limit]).Find(&batch).Error; err != nil {
				return nil, err
			}
			for _, record := range batch {
				subscriptionRequests[record.RequestId] = true
			}
		}
	}
	for _, log := range logs {
		call, ok := calls[log.RequestId]
		payer, owned := payers[log.UserId]
		if !owned {
			continue
		}
		if !ok {
			var entry ProductRuntimeTelemetryLog
			var admitted bool
			if op, exists := adjustments[log.RequestId]; exists {
				entry, admitted, err = readProductAdjustmentTelemetryLog(ctx, log, op, payer, digest)
			} else if subscriptionRequests[log.RequestId] {
				entry, admitted, err = readProductSubscriptionTelemetryLog(ctx, log, payer, digest)
			}
			if err != nil {
				return nil, err
			}
			if admitted {
				entry.ActorID = actors[log.UserId]
				result = append(result, entry)
			}
			continue
		}
		if call.UserID != log.UserId || call.TokenID != log.TokenId || call.AccountID != payer.AccountID || call.ParentCallID != "" {
			continue
		}
		// The account is a cutover receipt. A newer cash call declares and
		// validates its asset/conversion on the frozen call, not that receipt.
		reserve, err := ProductRuntimeCallContract(call)
		if err != nil || reserve.Operation == "subscription_purchase" {
			continue
		}
		result = append(result, ProductRuntimeTelemetryLog{Log: log, ActorID: actors[call.UserID], AccountID: call.AccountID, State: call.State, Asset: reserve.Asset, UnitVersion: reserve.UnitVersion, BillingSource: "unihub"})
	}
	return result, nil
}
