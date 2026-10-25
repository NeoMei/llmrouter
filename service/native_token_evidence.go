package service

import (
	"context"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
)

// NativeTokenObserver captures raw upstream evidence and persists one cumulative
// observation even when downstream validation or delivery fails afterwards.
func NativeTokenObserver(ctx context.Context, info *relaycommon.RelayInfo, source string) (*dto.NativeTokenEvidence, func()) {
	evidence := &dto.NativeTokenEvidence{Source: source}
	return evidence, func() {
		if info == nil || info.NativeBilling == nil {
			return
		}
		if err := RecordNativeUsageForRelay(ctx, info, evidence.Usage(info.NativeBilling.Price)); err != nil {
			common.SysError("record native token evidence: " + err.Error())
		}
	}
}
