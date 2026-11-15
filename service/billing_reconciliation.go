package service

import relaycommon "github.com/QuantumNous/new-api/relay/common"

// markBillingReconciliationPending keeps a failed settlement from turning into
// a silent refund. The pre-consume remains held until recovery or an operator
// reconciles the local ledger against the supplier bill.
func markBillingReconciliationPending(relayInfo *relaycommon.RelayInfo, err error) {
	if relayInfo == nil {
		return
	}
	relayInfo.BillingSettlementFailed = true
	if relayInfo.Billing == nil {
		return
	}
	relayInfo.Billing.MarkReconciliationPending("settlement failed: " + err.Error())
}
