package billing_setting

import (
	"errors"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/metering"
	"math"
	"strconv"
	"strings"
	"sync"
)

// ModelNativePricing is an explicit opt-in catalog. Legacy ModelPrice and
// ModelRatio remain independent until a model+operation appears here.
type ModelNativePricingConfig struct {
	ModelNativePricing map[string]map[string]metering.Price `json:"ModelNativePricing"`
}

var nativePricing = ModelNativePricingConfig{ModelNativePricing: map[string]map[string]metering.Price{}}
var nativePricingMu sync.RWMutex

// ModelNativeReservationLimits contains explicit admission ceilings for
// supplier-reported quantities whose final value is only known in the
// provider response. Values are frozen into the native billing journal by
// the reservation usage; they are never used as final usage.
// The key is public model -> operation -> integer quantity. A missing limit
// disables admission for that provider-reported operation rather than making
// an unsafe estimate.
type ModelNativeReservationLimitsConfig struct {
	ModelNativeReservationLimits map[string]map[string]metering.Quantity `json:"ModelNativeReservationLimits"`
}

var nativeReservationLimits = ModelNativeReservationLimitsConfig{ModelNativeReservationLimits: map[string]map[string]metering.Quantity{}}
var nativeReservationLimitsMu sync.RWMutex

func ValidateModelNativeReservationLimits(c map[string]map[string]metering.Quantity) error {
	if len(c) > 10000 {
		return errors.New("too many native reservation limit models")
	}
	for model, ops := range c {
		if strings.TrimSpace(model) == "" || len(model) > 128 || len(ops) > 16 {
			return errors.New("invalid native reservation limit model")
		}
		for operation, q := range ops {
			if operation != "speech" && operation != "video" {
				return errors.New("unsupported native reservation limit operation")
			}
			if q.Scale != 0 || q.Value == "" || len(q.Value) > 10 || (len(q.Value) > 1 && q.Value[0] == '0') || strings.Trim(q.Value, "0123456789") != "" {
				return errors.New("native reservation limit must be a non-negative integer")
			}
			n, err := strconv.ParseUint(q.Value, 10, 32)
			if err != nil || n == 0 || n > math.MaxInt32 {
				return errors.New("native reservation limit out of bounds")
			}
		}
	}
	return nil
}

func SetModelNativeReservationLimits(c map[string]map[string]metering.Quantity) error {
	if err := ValidateModelNativeReservationLimits(c); err != nil {
		return err
	}
	b, err := common.Marshal(c)
	if err != nil {
		return err
	}
	var copy map[string]map[string]metering.Quantity
	if err = common.Unmarshal(b, &copy); err != nil {
		return err
	}
	nativeReservationLimitsMu.Lock()
	nativeReservationLimits.ModelNativeReservationLimits = copy
	nativeReservationLimitsMu.Unlock()
	return nil
}

func ResolveModelNativeReservationLimit(model, operation string) (metering.Quantity, bool) {
	nativeReservationLimitsMu.RLock()
	defer nativeReservationLimitsMu.RUnlock()
	q, ok := nativeReservationLimits.ModelNativeReservationLimits[model][operation]
	return q, ok
}

func GetModelNativeReservationLimitsCopy() map[string]map[string]metering.Quantity {
	nativeReservationLimitsMu.RLock()
	defer nativeReservationLimitsMu.RUnlock()
	b, _ := common.Marshal(nativeReservationLimits.ModelNativeReservationLimits)
	var out map[string]map[string]metering.Quantity
	_ = common.Unmarshal(b, &out)
	return out
}

func UpdateModelNativeReservationLimitsByJSONString(raw string) error {
	var c map[string]map[string]metering.Quantity
	if err := common.UnmarshalUnique([]byte(raw), &c); err != nil {
		return err
	}
	return SetModelNativeReservationLimits(c)
}

func ModelNativeReservationLimitsJSONString() string {
	b, _ := common.Marshal(GetModelNativeReservationLimitsCopy())
	return string(b)
}

func ValidateModelNativePricing(c map[string]map[string]metering.Price) error {
	if len(c) > 10000 {
		return errors.New("too many native pricing models")
	}
	for model, ops := range c {
		if strings.TrimSpace(model) == "" || len(model) > 128 {
			return errors.New("invalid native pricing model")
		}
		if len(ops) > 16 {
			return errors.New("too many native pricing operations")
		}
		for operation, p := range ops {
			if operation != p.Operation {
				return errors.New("native pricing operation key mismatch")
			}
			policyOK := false
			switch operation {
			case "chat", "responses", "embedding":
				policyOK = p.Policy == "provider_reported_v1"
			case "transcription":
				policyOK = p.Policy == "provider_reported_v1" || p.Policy == "server_audio_measurement_v1"
			case "speech":
				policyOK = p.Policy == "provider_reported_v1" || p.Policy == "unicode_scalar_v1"
			case "image_generation", "image_edit":
				policyOK = p.Policy == "successful_outputs_v1"
			case "ocr":
				policyOK = p.Policy == "success_request"
			case "video":
				policyOK = ValidateNativeVideoPrice(p) == nil
			}
			if !policyOK {
				return errors.New("unsupported native measurement policy")
			}
			if err := metering.ValidatePrice(p); err != nil {
				return err
			}
			if operation == "image_generation" || operation == "image_edit" {
				if err := ValidateNativeImageTiers(p); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// ValidateNativeVideoPrice only enables receipt contracts implemented by the
// video producers. Generic or unmeasured policies cannot reserve user funds.
func ValidateNativeVideoPrice(p metering.Price) error {
	if p.Operation != "video" || metering.ValidatePrice(p) != nil {
		return errors.New("unsupported native video tariff")
	}
	if p.Policy == "ali_wan26_t2v_billable_duration_v1" {
		if len(p.Terms) != 2 {
			return errors.New("unsupported native video tariff")
		}
		expected := map[string]metering.Rational{
			"video_720p":  {Numerator: "3", Denominator: "5"},
			"video_1080p": {Numerator: "1", Denominator: "1"},
		}
		for _, term := range p.Terms {
			if term.Unit != "video_second" || expected[term.Key] != term.Price {
				return errors.New("unsupported native video tariff")
			}
		}
		return nil
	}
	if len(p.Terms) != 1 {
		return errors.New("unsupported native video tariff")
	}
	term := p.Terms[0]
	switch p.Policy {
	case "sora_completed_seconds_v1", "ali_video_duration_v1", "ali_wan26_t2v_billable_duration_v1":
		if term.Unit == "video_second" {
			return nil
		}
	case "kling_final_units_v1":
		if term.Unit == "provider_unit" && term.ProviderUnit == "kling.final_unit_deduction" {
			return nil
		}
	case "doubao_completion_tokens_v1":
		if term.Unit == "provider_unit" && term.ProviderUnit == "doubao.completion_tokens" {
			return nil
		}
	}
	return errors.New("unsupported native video receipt policy or unit")
}

// ValidateNativeImageTiers matches the output classifier used by native image
// adapters. An aggregate tier cannot overlap explicit dimensions.
func ValidateNativeImageTiers(p metering.Price) error {
	for _, term := range p.Terms {
		if term.Key == "images" {
			if len(p.Terms) != 1 {
				return errors.New("aggregate image tier cannot overlap size tiers")
			}
			continue
		}
		dimensions := strings.Split(term.Key, "x")
		if len(dimensions) != 2 {
			return errors.New("image tier requires canonical width x height")
		}
		for _, dimension := range dimensions {
			n, err := strconv.ParseUint(dimension, 10, 31)
			if err != nil || n == 0 || strconv.FormatUint(n, 10) != dimension {
				return errors.New("invalid image tier dimension")
			}
		}
	}
	return nil
}
func SetModelNativePricing(c map[string]map[string]metering.Price) error {
	if err := ValidateModelNativePricing(c); err != nil {
		return err
	}
	b, err := common.Marshal(c)
	if err != nil {
		return err
	}
	var copy map[string]map[string]metering.Price
	if err = common.Unmarshal(b, &copy); err != nil {
		return err
	}
	nativePricingMu.Lock()
	nativePricing.ModelNativePricing = copy
	nativePricingMu.Unlock()
	return nil
}
func GetModelNativePricingCopy() map[string]map[string]metering.Price {
	nativePricingMu.RLock()
	defer nativePricingMu.RUnlock()
	b, _ := common.Marshal(nativePricing.ModelNativePricing)
	var out map[string]map[string]metering.Price
	_ = common.Unmarshal(b, &out)
	return out
}
func ResolveModelNativePrice(model, operation string) (metering.Price, bool) {
	nativePricingMu.RLock()
	defer nativePricingMu.RUnlock()
	p, ok := nativePricing.ModelNativePricing[model][operation]
	if !ok {
		return metering.Price{}, false
	}
	b, _ := common.Marshal(p)
	var out metering.Price
	_ = common.Unmarshal(b, &out)
	return out, true
}
func UpdateModelNativePricingByJSONString(raw string) error {
	var c map[string]map[string]metering.Price
	if err := common.UnmarshalUnique([]byte(raw), &c); err != nil {
		return err
	}
	return SetModelNativePricing(c)
}
func ModelNativePricingJSONString() string {
	b, _ := common.Marshal(GetModelNativePricingCopy())
	return string(b)
}
