package service

import relaycommon "github.com/QuantumNous/new-api/relay/common"

// billingModelFor returns the canonical model name that must be used for
// price lookup, pre-consumption, and settlement. It preserves the requested
// alias in OriginModelName for audit logs while preventing missing-price
// failures when clients use public aliases or display names.
func billingModelFor(info *relaycommon.RelayInfo) string {
	if info == nil {
		return ""
	}
	if info.BillingModelName != "" {
		return info.BillingModelName
	}
	if info.ResolvedModelName != "" {
		return info.ResolvedModelName
	}
	return info.OriginModelName
}
