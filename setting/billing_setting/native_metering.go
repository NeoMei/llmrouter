package billing_setting

import (
	"errors"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/metering"
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
			case "chat", "responses", "embedding", "transcription":
				policyOK = p.Policy == "provider_reported_v1"
			case "speech":
				policyOK = p.Policy == "provider_reported_v1" || p.Policy == "unicode_scalar_v1"
			case "image_generation", "image_edit":
				policyOK = p.Policy == "successful_outputs_v1"
			case "ocr":
				policyOK = p.Policy == "success_request"
			case "video":
				policyOK = p.Policy == "provider_reported_v1" || p.Policy == "successful_outputs_v1" || p.Policy == "sora_completed_seconds_v1" || p.Policy == "kling_final_units_v1" || p.Policy == "doubao_completion_tokens_v1" || p.Policy == "ali_video_duration_v1" || p.Policy == "ali_wan26_t2v_billable_duration_v1" || p.Policy == "veo_output_probe_v1"
			}
			if !policyOK {
				return errors.New("unsupported native measurement policy")
			}
			if err := metering.ValidatePrice(p); err != nil {
				return err
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
