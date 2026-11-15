package service

import (
	"errors"

	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/logger"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
)

// usageSettlementUnconfirmed identifies a success-path usage that is not a
// supplier-confirmed meter. A standard upstream Usage without BillingUsage is
// confirmed; only an explicit Estimated BillingUsage or a missing usage object
// holds the reservation for reconciliation.
func usageSettlementUnconfirmed(relayInfo *relaycommon.RelayInfo, originUsage *dto.Usage) (string, bool) {
	if relayInfo == nil {
		return "", false
	}
	if originUsage == nil {
		return "missing supplier usage", true
	}
	if originUsage.BillingUsage != nil && originUsage.BillingUsage.Estimated {
		return "estimated upstream usage", true
	}
	return "", false
}

// hasValidatedSupplierUsage keeps legitimate non-token, pre-output validated
// and use-price settlements out of the missing-usage hold.
// originUsage is the unmodified vendor object. billingUsage may be a remapped
// or locally derived view; it must never turn a missing vendor meter into a
// confirmed one.
func hasValidatedSupplierUsage(relayInfo *relaycommon.RelayInfo, originUsage, billingUsage *dto.Usage) bool {
	if originUsage == nil {
		return false
	}
	usage := billingUsage
	if usage == nil {
		usage = originUsage
	}
	// ActualUsageValidated is promoted from the embedded *ChannelMeta pointer.
	// Guard the pointer so a bare RelayInfo (e.g. a unit-test fixture or a
	// pre-channel attempt) cannot panic on the promoted field.
	if relayInfo != nil && relayInfo.ChannelMeta != nil && relayInfo.ActualUsageValidated {
		return true
	}
	if usage.BillingUsage != nil && usage.BillingUsage.Estimated {
		return false
	}
	if usage.TotalTokens != 0 || usage.PromptTokens != 0 || usage.CompletionTokens != 0 ||
		usage.InputTokens != 0 || usage.OutputTokens != 0 {
		return true
	}
	if usage.BillingUsage != nil {
		return true
	}
	if relayInfo != nil && (relayInfo.PriceData.UsePrice || relayInfo.PriceData.ModelPrice != 0 ||
		relayInfo.RelayFormat == types.RelayFormatMultimodalEmbedding) {
		return true
	}
	if usage.ClaudeCacheCreation5mTokens != 0 || usage.ClaudeCacheCreation1hTokens != 0 ||
		usage.PromptTokensDetails.CachedTokens != 0 || usage.PromptTokensDetails.CachedCreationTokens != 0 {
		return true
	}
	if usage.ServerToolUse != nil {
		return true
	}
	return false
}

// holdUnconfirmedUsage keeps the pre-consume held, records an explicit pending
// audit log and prevents a clean consumption log or quota settlement.
func holdUnconfirmedUsage(ctx *gin.Context, relayInfo *relaycommon.RelayInfo, reason string) {
	markBillingReconciliationPending(relayInfo, errors.New(reason))
	logger.LogError(ctx, "供应商用量未确认，预扣保持挂起等待对账: "+reason)
}
