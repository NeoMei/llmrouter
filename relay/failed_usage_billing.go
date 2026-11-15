package relay

import (
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/types"

	"github.com/gin-gonic/gin"
)

// Settlement entry points are variables so focused tests can observe the exact
// usage passed to the ledger without running the global quota/notify pipeline.
var (
	postTextConsumeQuotaFn  = service.PostTextConsumeQuota
	postAudioConsumeQuotaFn = service.PostAudioConsumeQuota
)

type failedUsageOutcome int

const (
	// failedUsageNone means there is no vendor usage to settle. The caller
	// keeps its original error and retry/refund behaviour.
	failedUsageNone failedUsageOutcome = iota
	// failedUsageSettled means the provider's terminal usage was charged once
	// against the existing reservation; the error must not be retried.
	failedUsageSettled
	// failedUsagePending means the usage is estimated, unconfirmed, or the
	// settlement did not commit. The hold stays for reconciliation and the
	// error must not be retried or refunded.
	failedUsagePending
	// failedUsageSettleError means settlement failed after vendor usage was
	// observed. Surface a service error requiring reconciliation.
	failedUsageSettleError
)

// consumeFailedUsage settles vendor-reported usage from a generated failure.
// A provider can bill input, output and hosted tools even though the client
// receives an error. Settling once here prevents the controller's deferred
// Refund from returning the whole pre-consume, and skip_retry prevents a second
// attempt from billing the same usage again.
func consumeFailedUsage(c *gin.Context, info *relaycommon.RelayInfo, usage any) failedUsageOutcome {
	usageDTO, ok := usage.(*dto.Usage)
	if !ok || usageDTO == nil || info == nil {
		return failedUsageNone
	}
	if info.FailedUsageConsumed || info.NativeBilling != nil || info.PlatformMode || info.ManagedNativePricing() {
		return failedUsageNone
	}
	if usageDTO.BillingUsage != nil && usageDTO.BillingUsage.Estimated {
		markReconciliationPending(info, "estimated upstream usage")
		return failedUsagePending
	}
	billable := usageDTO.PromptTokens != 0 || usageDTO.CompletionTokens != 0 || usageDTO.TotalTokens != 0
	if info.ResponsesUsageInfo != nil {
		for _, tool := range info.ResponsesUsageInfo.BuiltInTools {
			if tool != nil && tool.CallCount > 0 {
				billable = true
				break
			}
		}
	}
	if !billable {
		return failedUsageNone
	}
	info.FailedUsageConsumed = true
	if isAudioBillingModel(info) {
		postAudioConsumeQuotaFn(c, info, usageDTO, "")
	} else {
		postTextConsumeQuotaFn(c, info, usageDTO, nil)
	}
	// The settlement path may hold an estimated or missing supplier meter for
	// reconciliation rather than commit a charge. Do not present that hold as a
	// settled vendor bill or allow a later refund of the reservation.
	if info.BillingSettlementFailed {
		return failedUsagePending
	}
	if info.Billing != nil && !info.Billing.IsSettled() {
		return failedUsageSettleError
	}
	return failedUsageSettled
}

func markReconciliationPending(info *relaycommon.RelayInfo, reason string) {
	info.BillingSettlementFailed = true
	if info.Billing != nil {
		info.Billing.MarkReconciliationPending(reason)
	}
}

func isAudioBillingModel(info *relaycommon.RelayInfo) bool {
	return strings.HasPrefix(info.OriginModelName, "gpt-4o-audio")
}

// finalizeFailedUsageError attaches the billing outcome to the client error.
// Settled and pending usage both skip retries; a settlement failure becomes a
// service error that requires reconciliation.
func finalizeFailedUsageError(apiErr *types.NewAPIError, outcome failedUsageOutcome) *types.NewAPIError {
	if apiErr == nil {
		return nil
	}
	switch outcome {
	case failedUsageSettled, failedUsagePending:
		return types.NewError(apiErr, apiErr.GetErrorCode(), types.ErrOptionWithSkipRetry())
	case failedUsageSettleError:
		return types.NewErrorWithStatusCode(
			apiErr,
			apiErr.GetErrorCode(),
			http.StatusServiceUnavailable,
			types.ErrOptionWithSkipRetry(),
		)
	default:
		return apiErr
	}
}
