package billing_setting

import (
	"github.com/QuantumNous/new-api/pkg/metering"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestModelNativePricingValidationAndSnapshot(t *testing.T) {
	p := metering.Price{Version: "metered_v2", Operation: "ocr", Policy: "success_request", Terms: []metering.Term{{Key: "requests", Unit: "request", Price: metering.Rational{Numerator: "1", Denominator: "1"}}}}
	require.NoError(t, SetModelNativePricing(map[string]map[string]metering.Price{"m": {"ocr": p}}))
	got, ok := ResolveModelNativePrice("m", "ocr")
	require.True(t, ok)
	require.Equal(t, p, got)
	p.Terms[0].Price.Numerator = "99"
	got2, _ := ResolveModelNativePrice("m", "ocr")
	require.Equal(t, "1", got2.Terms[0].Price.Numerator)
	require.Error(t, ValidateModelNativePricing(map[string]map[string]metering.Price{"m": {"chat": p}}))
}
