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

func TestModelNativeReservationLimitsAreBoundedAndCopied(t *testing.T) {
	old := GetModelNativeReservationLimitsCopy()
	t.Cleanup(func() { require.NoError(t, SetModelNativeReservationLimits(old)) })
	q := metering.Quantity{Value: "1200", Scale: 0}
	require.NoError(t, SetModelNativeReservationLimits(map[string]map[string]metering.Quantity{
		"speech-model": {"speech": q},
	}))
	got, ok := ResolveModelNativeReservationLimit("speech-model", "speech")
	require.True(t, ok)
	require.Equal(t, q, got)
	require.Error(t, ValidateModelNativeReservationLimits(map[string]map[string]metering.Quantity{
		"speech-model": {"speech": {Value: "01"}},
	}))
	require.Error(t, ValidateModelNativeReservationLimits(map[string]map[string]metering.Quantity{
		"speech-model": {"speech": {Value: "2147483648"}},
	}))
}

func TestNativeImageTiersRejectUnsupportedOrOverlappingClassifiers(t *testing.T) {
	for _, keys := range [][]string{{"bad"}, {"01024x1024"}, {"0x1024"}, {"images", "1024x1024"}} {
		p := metering.Price{Version: "metered_v2", Operation: "image_generation", Policy: "successful_outputs_v1"}
		for _, key := range keys {
			p.Terms = append(p.Terms, metering.Term{Key: key, Unit: "image", Price: metering.Rational{Numerator: "1", Denominator: "1"}})
		}
		require.Error(t, ValidateModelNativePricing(map[string]map[string]metering.Price{"image-model": {"image_generation": p}}))
	}
	p := metering.Price{Version: "metered_v2", Operation: "image_generation", Policy: "successful_outputs_v1", Terms: []metering.Term{{Key: "1024x1024", Unit: "image", Price: metering.Rational{Numerator: "1", Denominator: "1"}}, {Key: "1536x1024", Unit: "image", Price: metering.Rational{Numerator: "2", Denominator: "1"}}}}
	require.NoError(t, ValidateModelNativePricing(map[string]map[string]metering.Price{"image-model": {"image_generation": p}}))
}

func TestNativeVideoRequiresImplementedReceiptPolicyAndUnit(t *testing.T) {
	p := metering.Price{Version: "metered_v2", Operation: "video", Policy: "sora_completed_seconds_v1", Terms: []metering.Term{{Key: "video", Unit: "video_second", Price: metering.Rational{Numerator: "1", Denominator: "1"}}}}
	require.NoError(t, ValidateNativeVideoPrice(p))
	for _, policy := range []string{"provider_reported_v1", "successful_outputs_v1", "veo_output_probe_v1", "kling_final_units_v1"} {
		p.Policy = policy
		require.Error(t, ValidateNativeVideoPrice(p))
	}
	p.Terms[0].Key, p.Terms[0].Unit, p.Terms[0].ProviderUnit = "provider", "provider_unit", "kling.final_unit_deduction"
	require.NoError(t, ValidateNativeVideoPrice(p))
}

func TestNativeWanVideoOriginalPriceTiersAreClosed(t *testing.T) {
	p := metering.Price{Version: "metered_v2", Operation: "video", Policy: "ali_wan26_t2v_billable_duration_v1", Terms: []metering.Term{
		{Key: "video_720p", Unit: "video_second", Price: metering.Rational{Numerator: "3", Denominator: "5"}},
		{Key: "video_1080p", Unit: "video_second", Price: metering.Rational{Numerator: "1", Denominator: "1"}},
	}}
	require.NoError(t, ValidateNativeVideoPrice(p))
	p.Terms[0].Price = metering.Rational{Numerator: "1", Denominator: "1"}
	require.Error(t, ValidateNativeVideoPrice(p))
	p.Terms[0].Price = metering.Rational{Numerator: "3", Denominator: "5"}
	p.Terms = p.Terms[:1]
	require.Error(t, ValidateNativeVideoPrice(p))
}
