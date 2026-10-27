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
	if info == nil || info.Request == nil {
		return metering.Usage{}, errors.New("native reservation request unavailable")
	}
	if err := metering.ValidatePrice(price); err != nil {
		return metering.Usage{}, err
	}
	if price.Operation != "chat" && price.Operation != "responses" && price.Operation != "embedding" {
		return metering.Usage{}, errors.New("not a token operation")
	}
	input := info.GetEstimatePromptTokens()
	if input < 0 || input > common.MaxQuota {
		return metering.Usage{}, errors.New("prompt estimate out of bounds")
	}
	output := 0
	if price.Operation != "embedding" {
		meta := info.Request.GetTokenCountMeta()
		if meta == nil || meta.MaxTokens <= 0 || meta.MaxTokens > common.MaxQuota {
			return metering.Usage{}, errors.New("native token billing requires explicit bounded max tokens")
		}
		output = meta.MaxTokens
	}
	u := metering.Usage{Version: "metered_v2", Operation: price.Operation, State: "incomplete", Source: "reservation_bound_v1"}
	// Each exclusive input/output dimension gets its conservative upper bound.
	// Over-reservation is deliberate; only actual raw provider evidence settles.
	for _, term := range price.Terms {
		value := input
		switch term.Key {
		case "output", "reasoning":
			value = output
		case "input", "cache_read", "cache_write":
		default:
			return metering.Usage{}, errors.New("unknown token reservation dimension")
		}
		u.Measurements = append(u.Measurements, metering.Measurement{Key: term.Key, Unit: term.Unit, Quantity: metering.Quantity{Value: strconv.Itoa(value)}})
	}
	return u, nil
}
