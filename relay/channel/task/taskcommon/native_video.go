package taskcommon

import (
	"encoding/json"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/metering"
	"strconv"
	"strings"
)

// VideoNativeUsage accepts a pinned provider receipt policy, never request
// duration. Veo requires a bounded output probe and remains pending here.
func VideoNativeUsage(price metering.Price, body []byte, providerID, resolution string) metering.Usage {
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
			Status   string `json:"task_status"`
			TaskID   string `json:"task_id"`
			VideoURL string `json:"video_url"`
			Results  []struct {
				URL string `json:"url"`
			} `json:"results"`
		} `json:"output"`
		Usage struct {
			Tokens              json.RawMessage `json:"completion_tokens"`
			VideoDuration       json.RawMessage `json:"video_duration"`
			Duration            json.RawMessage `json:"duration"`
			VideoCount          json.RawMessage `json:"video_count"`
			OutputVideoDuration json.RawMessage `json:"output_video_duration"`
			SR                  json.RawMessage `json:"SR"`
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
	if price.Policy != "ali_wan26_t2v_billable_duration_v1" && !metering.WanCashTierPolicy(price.Policy) && (r.Status == "failed" || r.Data.Status == "failed" || r.Output.Status == "FAILED") && string(outputs) == "[]" {
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
	case "ali_wan26_t2v_billable_duration_v1", "ali_wan26_t2v_cny6_v2", "ali_wan27_t2v_cny6_v1":
		if providerID == "" || r.Output.TaskID != providerID {
			u.State = "invalid"
			return u
		}
		if resolution != "video_720p" && resolution != "video_1080p" {
			return u
		}
		if (r.Output.Status == "FAILED" || r.Output.Status == "CANCELED") && r.Output.VideoURL == "" && len(r.Output.Results) == 0 && (len(r.Videos) == 0 || string(r.Videos) == "[]") {
			// Failure/cancellation cannot prove no output when the provider's
			// usage contradicts it or contains malformed values. UNKNOWN is
			// not terminal evidence: a completed task can have expired.
			var envelope struct {
				Usage json.RawMessage `json:"usage"`
			}
			if common.UnmarshalUnique(body, &envelope) != nil {
				u.State = "invalid"
				return u
			}
			if len(envelope.Usage) != 0 {
				var fields map[string]json.RawMessage
				if common.UnmarshalUnique(envelope.Usage, &fields) != nil || fields == nil {
					u.State = "invalid"
					return u
				}
				for key, value := range fields {
					switch key {
					case "duration", "video_duration", "input_video_duration", "output_video_duration", "video_count", "SR":
						q, ok := videoDecimalQuantity(string(value))
						if !ok || q.Value != "0" {
							u.State = "invalid"
							return u
						}
					case "ratio", "video_ratio", "size":
						var field string
						if common.Unmarshal(value, &field) != nil || field != "" {
							u.State = "invalid"
							return u
						}
					default:
						u.State = "invalid"
						return u
					}
				}
			}
			u.State = "incomplete"
			u.Source = "provider_confirmed_no_output"
			return u
		}
		if r.Output.Status != "SUCCEEDED" {
			return u
		}
		outputs := 0
		if r.Output.VideoURL != "" {
			outputs++
		}
		for _, result := range r.Output.Results {
			if result.URL == "" {
				u.State = "invalid"
				return u
			}
			outputs++
		}
		var count int
		if len(r.Usage.VideoCount) == 0 || string(r.Usage.VideoCount) == "null" || r.Usage.VideoCount[0] == '"' || common.Unmarshal(r.Usage.VideoCount, &count) != nil || count != 1 || outputs != 1 {
			u.State = "incomplete"
			return u
		}
		if metering.WanCashTierPolicy(price.Policy) {
			var envelope struct {
				Usage json.RawMessage `json:"usage"`
			}
			var fields map[string]json.RawMessage
			if common.UnmarshalUnique(body, &envelope) != nil || common.UnmarshalUnique(envelope.Usage, &fields) != nil || len(fields) != 6 {
				u.State = "invalid"
				return u
			}
			dimension := "size"
			if price.Policy == "ali_wan27_t2v_cny6_v1" {
				dimension = "ratio"
			}
			for _, field := range []string{"duration", "input_video_duration", "output_video_duration", "video_count", "SR", dimension} {
				if _, exists := fields[field]; !exists {
					u.State = "invalid"
					return u
				}
			}
			input, ok := videoDecimalQuantity(string(fields["input_video_duration"]))
			if !ok || input.Value != "0" {
				u.State = "invalid"
				return u
			}
			var actualDimension string
			if common.Unmarshal(fields[dimension], &actualDimension) != nil {
				u.State = "invalid"
				return u
			}
			expectedDimension := "1280*720"
			if resolution == "video_1080p" {
				expectedDimension = "1920*1080"
			}
			if dimension == "ratio" {
				expectedDimension = "16:9"
			}
			if actualDimension != expectedDimension {
				u.State = "invalid"
				return u
			}
			var sr int
			if common.Unmarshal(r.Usage.SR, &sr) != nil || (resolution == "video_720p" && sr != 720) || (resolution == "video_1080p" && sr != 1080) {
				u.State = "invalid"
				return u
			}
			actual, ok := videoDecimalQuantity(string(r.Usage.Duration))
			seconds, err := strconv.Atoi(actual.Value)
			if !ok || err != nil || actual.Scale != 0 || seconds < 2 || seconds > 15 {
				u.State = "invalid"
				return u
			}
			output, ok := videoDecimalQuantity(string(r.Usage.OutputVideoDuration))
			if !ok || output != actual {
				u.State = "invalid"
				return u
			}
		}
		raw = r.Usage.Duration
		key = resolution
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
