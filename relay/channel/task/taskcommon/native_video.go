package taskcommon

import (
	"encoding/json"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/metering"
	"strings"
)

// VideoNativeUsage accepts a pinned provider receipt policy, never request
// duration. Veo requires a bounded output probe and remains pending here.
func VideoNativeUsage(price metering.Price, body []byte, providerID string) metering.Usage {
	u := metering.Usage{Version: "metered_v2", Operation: "video", Source: price.Policy, ProviderRequestID: providerID, State: "missing"}
	var r struct {
		Status  string          `json:"status"`
		Seconds json.RawMessage `json:"seconds"`
		Videos  json.RawMessage `json:"videos"`
		Data    struct {
			Status string          `json:"task_status"`
			Units  json.RawMessage `json:"final_unit_deduction"`
			Result struct {
				Videos json.RawMessage `json:"videos"`
			} `json:"task_result"`
		} `json:"data"`
		Output struct {
			Status string `json:"task_status"`
		} `json:"output"`
		Usage struct {
			Tokens        json.RawMessage `json:"completion_tokens"`
			VideoDuration json.RawMessage `json:"video_duration"`
			Duration      json.RawMessage `json:"duration"`
		} `json:"usage"`
	}
	if common.UnmarshalUnique(body, &r) != nil {
		u.State = "invalid"
		return u
	}
	// A failure alone can also represent an unknown transport outcome. Release
	// only an explicit terminal receipt with an empty output collection.
	outputs := r.Videos
	if price.Policy == "kling_final_units_v1" {
		outputs = r.Data.Result.Videos
	}
	if (r.Status == "failed" || r.Data.Status == "failed" || r.Output.Status == "FAILED") && string(outputs) == "[]" {
		u.State = "incomplete"
		u.Source = "provider_confirmed_no_output"
		return u
	}
	var raw json.RawMessage
	unit, key, namespace := "video_second", "video", ""
	switch price.Policy {
	case "sora_completed_seconds_v1":
		if r.Status != "completed" {
			return u
		}
		raw = r.Seconds
	case "kling_final_units_v1":
		if r.Data.Status != "succeed" {
			return u
		}
		raw = r.Data.Units
		unit, key, namespace = "provider_unit", "provider", "kling.final_unit_deduction"
	case "doubao_completion_tokens_v1":
		if r.Status != "succeeded" {
			return u
		}
		raw = r.Usage.Tokens
		unit, key, namespace = "provider_unit", "provider", "doubao.completion_tokens"
	case "ali_video_duration_v1":
		if r.Output.Status != "SUCCEEDED" {
			return u
		}
		raw = r.Usage.VideoDuration
	// This policy is explicitly provider billable duration for Wan 2.6 t2v,
	// not independent measured output seconds or a generic Wan schema.
	case "ali_wan26_t2v_billable_duration_v1":
		if r.Output.Status != "SUCCEEDED" {
			return u
		}
		raw = r.Usage.Duration
	default:
		return u
	}
	if len(raw) == 0 || string(raw) == "null" {
		return u
	}
	s := string(raw)
	if raw[0] == '"' {
		if common.Unmarshal(raw, &s) != nil {
			u.State = "invalid"
			return u
		}
	}
	q, ok := videoDecimalQuantity(s)
	if !ok || (namespace == "doubao.completion_tokens" && q.Scale != 0) {
		u.State = "invalid"
		return u
	}
	u.State = "confirmed"
	u.Measurements = []metering.Measurement{{Key: key, Unit: unit, ProviderUnit: namespace, Quantity: q}}
	if metering.ValidateUsage(price, u) != nil {
		u.State = "invalid"
	}
	return u
}

// Decimal receipts are bounded, exact and canonical. Exponents, negatives,
// fractions and precision beyond nanounits are not provider receipt decimals.
func videoDecimalQuantity(s string) (metering.Quantity, bool) {
	if len(s) == 0 || len(s) > 48 {
		return metering.Quantity{}, false
	}
	parts := strings.Split(s, ".")
	if len(parts) > 2 || parts[0] == "" || strings.Trim(parts[0], "0123456789") != "" {
		return metering.Quantity{}, false
	}
	fraction := ""
	if len(parts) == 2 {
		fraction = parts[1]
		if fraction == "" || len(fraction) > 9 || strings.Trim(fraction, "0123456789") != "" {
			return metering.Quantity{}, false
		}
	}
	fraction = strings.TrimRight(fraction, "0")
	value := strings.TrimLeft(parts[0]+fraction, "0")
	if value == "" {
		value = "0"
	}
	if len(value) > 38 {
		return metering.Quantity{}, false
	}
	return metering.Quantity{Value: value, Scale: len(fraction)}, true
}
