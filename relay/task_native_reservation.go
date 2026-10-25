package relay

import (
	"errors"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/metering"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/setting/billing_setting"
	"github.com/gin-gonic/gin"
	"os"
	"strconv"
)

// NativeVideoReservation is a server policy bound. Provider caps are configured
// per public model AND policy via LLMROUTER_NATIVE_VIDEO_PROVIDER_CAPS, e.g.
// {"model":{"policy":"kling_final_units_v1","quantity":{"value":"10","scale":0}}}.
// This is an admission cap, never final usage. No provider-unit guess is made.
func NativeVideoReservation(c *gin.Context, modelName string, price metering.Price) (metering.Usage, error) {
	u := metering.Usage{Version: "metered_v2", Operation: "video", State: "incomplete", Source: "reservation_bound"}
	if price.Policy == "veo_output_probe_v1" {
		return u, errors.New("native Veo output probing is not enabled")
	}
	req, err := relaycommon.GetTaskRequest(c)
	if err != nil {
		return u, err
	}
	seconds := req.Duration
	if req.Seconds != "" {
		n, e := strconv.Atoi(req.Seconds)
		if e != nil || (seconds != 0 && seconds != n) {
			return u, errors.New("invalid/conflicting duration")
		}
		seconds = n
	}
	// Explicit duration is required for a native tariff; defaults vary by model.
	if seconds <= 0 || seconds > relaycommon.MaxTaskDurationSeconds {
		return u, errors.New("native video requires bounded explicit seconds")
	}
	q := metering.Quantity{Value: strconv.Itoa(seconds)}
	if len(price.Terms) != 1 {
		return u, errors.New("unsupported video dimensions")
	}
	term := price.Terms[0]
	if term.Unit == "provider_unit" {
		if configured, ok := billing_setting.ResolveModelNativeReservationLimit(modelName, "video"); ok {
			q = configured
		} else {
			var caps map[string]struct {
				Policy   string            `json:"policy"`
				Quantity metering.Quantity `json:"quantity"`
			}
			if common.UnmarshalUnique([]byte(os.Getenv("LLMROUTER_NATIVE_VIDEO_PROVIDER_CAPS")), &caps) != nil {
				return u, errors.New("native provider-unit reservation cap not configured")
			}
			cap, ok := caps[modelName]
			if !ok || cap.Policy != price.Policy || cap.Quantity.Value == "0" {
				return u, errors.New("native provider-unit reservation policy mismatch")
			}
			q = cap.Quantity
		}
	}
	u.Measurements = []metering.Measurement{{Key: term.Key, Unit: term.Unit, ProviderUnit: term.ProviderUnit, Quantity: q}}
	check := u
	check.State = "confirmed"
	if err = metering.ValidateUsage(price, check); err != nil {
		return u, err
	}
	return u, nil
}
