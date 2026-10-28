package metering

import (
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestSpeechProviderUnitExactDecimal(t *testing.T) {
	p := Price{Version: "metered_v2", Operation: "speech", Policy: "elevenlabs_character_cost_v1", Terms: []Term{{Key: "provider", Unit: "provider_unit", ProviderUnit: "elevenlabs.character_cost.multilingual_v2.v1", Price: Rational{Numerator: "3", Denominator: "1"}}}}
	for _, tc := range []struct {
		value string
		scale int
		want  string
	}{{"0", 0, "0"}, {"50", 2, "3/2"}} {
		u := Usage{Version: "metered_v2", Operation: "speech", State: "confirmed", Measurements: []Measurement{{Key: "provider", Unit: "provider_unit", ProviderUnit: p.Terms[0].ProviderUnit, Quantity: Quantity{Value: tc.value, Scale: tc.scale}}}}
		actual, err := Calculate(p, u)
		require.NoError(t, err)
		assert.Equal(t, tc.want, actual.RatString())
	}
	p.Terms[0].ProviderUnit = "guessed.credits"
	assert.Error(t, ValidatePrice(p))
}
