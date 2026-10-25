package service

import (
	"context"
	"errors"
	"strconv"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/pkg/metering"
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
		if info.IsStream && info.StreamStatus != nil {
			clean := info.StreamStatus.EndError == nil && info.StreamStatus.EndReason == relaycommon.StreamEndReasonDone
			if source == dto.BillingUsageSourceGeminiChat {
				clean = info.StreamStatus.EndError == nil && (info.StreamStatus.EndReason == relaycommon.StreamEndReasonDone || info.StreamStatus.EndReason == relaycommon.StreamEndReasonEOF)
			}
			evidence.CompleteStream(clean)
		}
		if err := RecordNativeUsageForRelay(ctx, info, evidence.Usage(info.NativeBilling.Price)); err != nil {
			common.SysError("record native token evidence: " + err.Error())
		}
	}
}

// NativeTokenReservation builds a bounded reservation vector from the request's
// tokenizer estimate. It is deliberately marked incomplete: only provider
// evidence may confirm native billing.
func NativeTokenReservation(info *relaycommon.RelayInfo, price metering.Price) (metering.Usage, error) {
	if info == nil {
		return metering.Usage{}, errors.New("relay info unavailable")
	}
	input := info.GetEstimatePromptTokens()
	if input < 0 || input > 1<<30 {
		return metering.Usage{}, errors.New("prompt estimate out of bounds")
	}
	u := metering.Usage{Version: "metered_v2", Operation: price.Operation, State: "incomplete", Source: "reservation"}
	for _, term := range price.Terms {
		value := "0"
		if term.Key == "input" {
			value = strconv.Itoa(input)
		}
		u.Measurements = append(u.Measurements, metering.Measurement{Key: term.Key, Unit: term.Unit, Quantity: metering.Quantity{Value: value}})
	}
	return u, nil
}
