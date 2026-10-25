package service

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/pkg/metering"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/setting/billing_setting"
)

// NativeSpeechUsage converts only the final outbound text or trusted supplier
// character receipt. Supplier-reported policies never fall back to estimates.
func NativeSpeechUsage(p metering.Price, text string, reported *int64, terminal bool) metering.Usage {
	u := metering.Usage{Version: "metered_v2", Operation: p.Operation, State: "missing", Source: "native_media"}
	if !terminal {
		u.State = "incomplete"
		return u
	}
	var q metering.Quantity
	switch p.Policy {
	case "unicode_scalar_v1":
		if !utf8.ValidString(text) {
			u.State = "invalid"
			return u
		}
		q = metering.Quantity{Value: strconv.Itoa(utf8.RuneCountInString(text))}
	case "provider_reported_v1":
		if reported == nil {
			return u
		}
		if *reported < 0 {
			u.State = "invalid"
			return u
		}
		q = metering.Quantity{Value: strconv.FormatInt(*reported, 10)}
	default:
		u.State = "invalid"
		return u
	}
	u.State = "confirmed"
	u.Measurements = []metering.Measurement{{Key: "characters", Unit: "character", Quantity: q}}
	return u
}
func NativeAudioUsage(p metering.Price, seconds *float64, terminal bool) metering.Usage {
	u := metering.Usage{Version: "metered_v2", Operation: p.Operation, State: "missing", Source: "native_media"}
	if p.Policy != "provider_reported_v1" && p.Policy != "server_audio_measurement_v1" {
		u.State = "invalid"
		return u
	}
	if !terminal {
		u.State = "incomplete"
		return u
	}
	if seconds == nil {
		return u
	}
	if math.IsNaN(*seconds) || math.IsInf(*seconds, 0) || *seconds < 0 || *seconds > 86400 {
		u.State = "invalid"
		return u
	}
	s := strconv.FormatFloat(*seconds, 'f', -1, 64)
	if strings.Contains(s, ".") && len(strings.Split(s, ".")[1]) > 9 {
		u.State = "invalid"
		return u
	}
	if strings.Contains(s, ".") {
		s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
	}
	if s == "" {
		s = "0"
	}
	parts := strings.Split(s, ".")
	scale := 0
	val := s
	if len(parts) == 2 {
		scale = len(parts[1])
		val = strings.TrimLeft(parts[0]+parts[1], "0")
		if val == "" {
			val = "0"
		}
	}
	u.State = "confirmed"
	u.Measurements = []metering.Measurement{{Key: "audio", Unit: "audio_second", Quantity: metering.Quantity{Value: val, Scale: scale}}}
	return u
}

type NativeImageOutput struct {
	ID            string
	Width, Height int
}

func NativeImageUsage(p metering.Price, outputs []NativeImageOutput, terminal bool) metering.Usage {
	u := metering.Usage{Version: "metered_v2", Operation: p.Operation, State: "missing", Source: "native_media"}
	if !terminal {
		u.State = "incomplete"
		return u
	}
	counts := map[string]int{}
	for _, t := range p.Terms {
		if t.Key != "images" {
			parts := strings.Split(t.Key, "x")
			if len(parts) != 2 {
				u.State = "invalid"
				return u
			}
			w, e := strconv.Atoi(parts[0])
			h, e2 := strconv.Atoi(parts[1])
			if e != nil || e2 != nil || w <= 0 || h <= 0 {
				u.State = "invalid"
				return u
			}
		}
		counts[t.Key] = 0
	}
	seen := map[string]NativeImageOutput{}
	for _, o := range outputs {
		if o.ID == "" {
			return u
		}
		if old, ok := seen[o.ID]; ok {
			if old.Width != o.Width || old.Height != o.Height {
				u.State = "invalid"
				return u
			}
			continue
		}
		seen[o.ID] = o
		key := "images"
		if _, ok := counts[key]; !ok {
			if o.Width <= 0 || o.Height <= 0 {
				return u
			}
			key = fmt.Sprintf("%dx%d", o.Width, o.Height)
		}
		if _, ok := counts[key]; !ok {
			u.State = "invalid"
			return u
		}
		counts[key]++
	}
	u.State = "confirmed"
	for _, t := range p.Terms {
		u.Measurements = append(u.Measurements, metering.Measurement{Key: t.Key, Unit: "image", Quantity: metering.Quantity{Value: strconv.Itoa(counts[t.Key])}})
	}
	return u
}

// EstimateNativeMediaUsage provides bounded reservation quantities only.
func EstimateNativeMediaUsage(info *relaycommon.RelayInfo, request any, price metering.Price) (metering.Usage, error) {
	if info == nil {
		return metering.Usage{}, fmt.Errorf("relay info required")
	}
	u := metering.Usage{Version: "metered_v2", Operation: price.Operation, State: "incomplete", Source: "local_admission"}
	switch r := request.(type) {
	case *dto.AudioRequest:
		request = *r
	case *dto.ImageRequest:
		request = *r
	}

	switch info.RelayMode {
	case relayconstant.RelayModeAudioSpeech:
		r, ok := request.(dto.AudioRequest)
		if !ok {
			return u, fmt.Errorf("audio request required")
		}
		if price.Policy != "unicode_scalar_v1" && price.Policy != "provider_reported_v1" {
			return u, fmt.Errorf("unsupported character policy")
		}
		if !utf8.ValidString(r.Input) {
			return u, fmt.Errorf("invalid speech UTF8")
		}
		n := utf8.RuneCountInString(r.Input)
		// Supplier character rules can differ from Unicode scalars. The explicit
		// server cap is a reservation only; settlement still requires its receipt.
		if price.Policy == "provider_reported_v1" {
			q, configured := billing_setting.ResolveModelNativeReservationLimit(info.BillingModelNameOrOrigin(), "speech")
			if !configured {
				return u, fmt.Errorf("provider-reported speech requires a configured reservation limit")
			}
			n, _ = strconv.Atoi(q.Value)
			if utf8.RuneCountInString(r.Input) > n {
				return u, fmt.Errorf("speech input exceeds reservation limit")
			}
		}
		u.Measurements = []metering.Measurement{{Key: "characters", Unit: "character", Quantity: metering.Quantity{Value: strconv.Itoa(n)}}}
	case relayconstant.RelayModeAudioTranscription, relayconstant.RelayModeAudioTranslation:
		if info.ActualAudioSeconds == nil {
			return u, fmt.Errorf("audio duration unavailable")
		}
		x := NativeAudioUsage(price, info.ActualAudioSeconds, true)
		u.Measurements = x.Measurements
	case relayconstant.RelayModeImagesGenerations, relayconstant.RelayModeImagesEdits, relayconstant.RelayModeImagesVariations:
		r, ok := request.(dto.ImageRequest)
		if !ok {
			return u, fmt.Errorf("image request required")
		}
		n := uint(1)
		if r.N != nil && *r.N > 0 {
			n = *r.N
		}
		if n > dto.MaxImageN {
			return u, fmt.Errorf("image count exceeds bound")
		}
		// Reserve every possible output against every tier, so auto/mismatched
		// dimensions cannot exceed the frozen reservation.
		for _, t := range price.Terms {
			u.Measurements = append(u.Measurements, metering.Measurement{Key: t.Key, Unit: "image", Quantity: metering.Quantity{Value: strconv.FormatUint(uint64(n), 10)}})
		}
	default:
		return u, fmt.Errorf("unsupported native media operation")
	}
	return u, nil
}
