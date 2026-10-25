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
	if !terminal {
		u.State = "incomplete"
		return u
	}
	if seconds == nil || math.IsNaN(*seconds) || math.IsInf(*seconds, 0) || *seconds < 0 || *seconds > 86400 {
		u.State = "invalid"
		return u
	}
	s := strconv.FormatFloat(*seconds, 'f', 9, 64)
	s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
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
	if len(p.Terms) != 1 {
		u.State = "invalid"
		return u
	}
	term := p.Terms[0]
	seen := map[string]bool{}
	count := 0
	for _, o := range outputs {
		if o.ID == "" || seen[o.ID] {
			continue
		}
		seen[o.ID] = true
		if term.Key != "images" {
			want := strings.Split(term.Key, "x")
			if len(want) != 2 {
				u.State = "missing"
				return u
			}
			w, e1 := strconv.Atoi(want[0])
			h, e2 := strconv.Atoi(want[1])
			if e1 != nil || e2 != nil {
				u.State = "invalid"
				return u
			}
			if o.Width == 0 || o.Height == 0 {
				u.State = "missing"
				return u
			}
			if o.Width != w || o.Height != h {
				u.State = "invalid"
				return u
			}
		}
		count++
	}
	u.State = "confirmed"
	u.Measurements = []metering.Measurement{{Key: term.Key, Unit: "image", Quantity: metering.Quantity{Value: strconv.Itoa(count)}}}
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
		if price.Policy != "unicode_scalar_v1" {
			return u, fmt.Errorf("supplier character reservation bound unavailable")
		}
		if !utf8.ValidString(r.Input) {
			return u, fmt.Errorf("invalid speech UTF8")
		}
		n := utf8.RuneCountInString(r.Input)
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
		key := "images"
		if len(price.Terms) == 1 {
			key = price.Terms[0].Key
		}
		if n > dto.MaxImageN {
			return u, fmt.Errorf("image count exceeds bound")
		}
		if key != "images" && key != r.Size {
			return u, fmt.Errorf("image size admission bound unavailable")
		}
		u.Measurements = []metering.Measurement{{Key: key, Unit: "image", Quantity: metering.Quantity{Value: strconv.FormatUint(uint64(n), 10)}}}
	default:
		return u, fmt.Errorf("unsupported native media operation")
	}
	return u, nil
}
