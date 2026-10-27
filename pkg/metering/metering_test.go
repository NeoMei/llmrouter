package metering

import (
	"encoding/json"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"os"
	"testing"
)

func qty(v string, s int) Quantity { return Quantity{Value: v, Scale: s} }
func TestCalculateFamiliesAndValidation(t *testing.T) {
	tests := []struct {
		name string
		p    Price
		u    Usage
		want string
		bad  bool
	}{
		{"characters", Price{Version: "metered_v2", Operation: "speech", Policy: "sum", Terms: []Term{{Key: "characters", Unit: "character", Price: Rational{"1", "1000"}}}}, Usage{Version: "metered_v2", Operation: "speech", State: "confirmed", Measurements: []Measurement{{Key: "characters", Unit: "character", Quantity: qty("1500", 0)}}}, "3/2", false},
		{"seconds", Price{Version: "metered_v2", Operation: "transcription", Policy: "sum", Terms: []Term{{Key: "audio", Unit: "audio_second", Price: Rational{"2", "1"}}}}, Usage{Version: "metered_v2", Operation: "transcription", State: "confirmed", Measurements: []Measurement{{Key: "audio", Unit: "audio_second", Quantity: qty("1001", 3)}}}, "1001/500", false},
		{"zero", Price{Version: "metered_v2", Operation: "speech", Policy: "sum", Terms: []Term{{Key: "characters", Unit: "character", Price: Rational{"1", "1"}}}}, Usage{Version: "metered_v2", Operation: "speech", State: "confirmed", Measurements: []Measurement{{Key: "characters", Unit: "character", Quantity: qty("0", 0)}}}, "0", false},
		{"missing", Price{Version: "metered_v2", Operation: "chat", Policy: "sum", Terms: []Term{{Key: "input", Unit: "token", Price: Rational{"1", "1"}}, {Key: "output", Unit: "token", Price: Rational{"1", "1"}}}}, Usage{Version: "metered_v2", Operation: "chat", State: "confirmed", Measurements: []Measurement{{Key: "input", Unit: "token", Quantity: qty("1", 0)}}}, "", true},
		{"duplicate", Price{Version: "metered_v2", Operation: "chat", Policy: "sum", Terms: []Term{{Key: "input", Unit: "token", Price: Rational{"1", "1"}}, {Key: "input", Unit: "token", Price: Rational{"1", "1"}}}}, Usage{}, "", true},
		{"unknown", Price{Version: "metered_v2", Operation: "chat", Policy: "sum", Terms: []Term{{Key: "input", Unit: "token", Price: Rational{"1", "1"}}}}, Usage{Version: "metered_v2", Operation: "chat", State: "confirmed", Measurements: []Measurement{{Key: "x", Unit: "token", Quantity: qty("1", 0)}}}, "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidatePrice(tt.p)
			if tt.bad {
				if tt.name == "missing" || tt.name == "unknown" {
					require.NoError(t, err)
				}
				_, err = Calculate(tt.p, tt.u)
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			got, err := Calculate(tt.p, tt.u)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got.RatString())
		})
	}
}
func TestInvalidQuantityAndHuge(t *testing.T) {
	p := Price{Version: "metered_v2", Operation: "speech", Policy: "sum", Terms: []Term{{Key: "characters", Unit: "character", Price: Rational{"1", "0"}}}}
	require.Error(t, ValidatePrice(p))
	q := qty("123456789012345678901234567890123456789", 0)
	p.Terms[0].Price = Rational{"1", "1"}
	u := Usage{Version: "metered_v2", Operation: "speech", State: "confirmed", Measurements: []Measurement{{Key: "characters", Unit: "character", Quantity: q}}}
	_, err := Calculate(p, u)
	require.Error(t, err)
}

func TestExactFamilies(t *testing.T) {
	for _, tc := range []struct{ op, key, unit string }{{"chat", "input", "token"}, {"image_generation", "small", "image"}, {"ocr", "requests", "request"}, {"video", "video", "video_second"}, {"video", "provider", "provider_unit"}} {
		t.Run(tc.unit, func(t *testing.T) {
			p := Price{Version: "metered_v2", Operation: tc.op, Policy: "sum", Terms: []Term{{Key: tc.key, Unit: tc.unit, ProviderUnit: "", Price: Rational{"2", "3"}, Step: &Quantity{"2", 0}, Minimum: &Quantity{"3", 0}}}}
			u := Usage{Version: "metered_v2", Operation: tc.op, State: "confirmed", Measurements: []Measurement{{Key: tc.key, Unit: tc.unit, ProviderUnit: "", Quantity: qty("1", 0)}}}
			if tc.unit == "provider_unit" {
				p.Terms[0].ProviderUnit = "vendor:unit"
				u.Measurements[0].ProviderUnit = "vendor:unit"
			}
			got, err := Calculate(p, u)
			require.NoError(t, err)
			assert.Equal(t, "8/3", got.RatString())
			u.Measurements[0].Quantity = qty("0", 0)
			got, err = Calculate(p, u)
			require.NoError(t, err)
			assert.Equal(t, "0", got.RatString())
		})
	}
}
func TestRejectedContracts(t *testing.T) {
	for _, name := range []string{"negative", "leading zero", "scale", "integer scale", "denominator", "price negative", "price huge", "unit", "operation", "mismatch", "duplicate usage", "pending", "namespace", "empty policy", "too many terms", "zero step", "mixed units"} {
		t.Run(name, func(t *testing.T) {
			p := Price{Version: "metered_v2", Operation: "speech", Policy: "sum", Terms: []Term{{Key: "characters", Unit: "character", Price: Rational{"1", "1000"}}}}
			u := Usage{Version: "metered_v2", Operation: "speech", State: "confirmed", Measurements: []Measurement{{Key: "characters", Unit: "character", Quantity: qty("1500", 0)}}}
			switch name {
			case "negative":
				u.Measurements[0].Quantity.Value = "-1"
			case "leading zero":
				u.Measurements[0].Quantity.Value = "01"
			case "scale":
				u.Measurements[0].Quantity.Scale = 10
			case "integer scale":
				u.Measurements[0].Quantity.Scale = 1
			case "denominator":
				p.Terms[0].Price.Denominator = "0"
			case "price negative":
				p.Terms[0].Price.Numerator = "-1"
			case "price huge":
				p.Terms[0].Price.Numerator = "123456789012345678901234567890123456789"
			case "unit":
				p.Terms[0].Unit = "page"
			case "operation":
				p.Operation = "unknown"
			case "mismatch":
				u.Operation = "chat"
			case "duplicate usage":
				u.Measurements = append(u.Measurements, u.Measurements[0])
			case "pending":
				u.State = "missing"
			case "namespace":
				p.Terms[0].Unit = "provider_unit"
			case "empty policy":
				p.Policy = ""
			case "too many terms":
				p.Terms = make([]Term, 65)
			case "zero step":
				p.Terms[0].Step = &Quantity{"0", 0}
			case "mixed units":
				p.Terms = append(p.Terms, Term{Key: "audio", Unit: "audio_second", Price: Rational{"1", "1"}})
			}
			got, err := Calculate(p, u)
			require.Error(t, err)
			assert.Nil(t, got)
		})
	}
}

func TestWireFixtures(t *testing.T) {
	data, err := os.ReadFile("testdata/v2.json")
	require.NoError(t, err)
	var fixtures []struct {
		Name    string
		Price   Price
		Usage   Usage
		Want    string
		Invalid bool
	}
	require.NoError(t, json.Unmarshal(data, &fixtures))
	for _, f := range fixtures {
		t.Run(f.Name, func(t *testing.T) {
			got, err := Calculate(f.Price, f.Usage)
			if f.Invalid {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, f.Want, got.RatString())
		})
	}
}

func TestIntegerTariffQuantities(t *testing.T) {
	for _, tc := range []struct{ op, unit, key string }{{"chat", "token", "input"}, {"speech", "character", "characters"}, {"image_generation", "image", "large"}, {"ocr", "request", "requests"}} {
		for _, field := range []string{"step", "minimum"} {
			t.Run(tc.unit+field, func(t *testing.T) {
				term := Term{Key: tc.key, Unit: tc.unit, Price: Rational{"1", "1"}}
				fractional := Quantity{"1", 1}
				if field == "step" {
					term.Step = &fractional
				} else {
					term.Minimum = &fractional
				}
				require.Error(t, ValidatePrice(Price{Version: "metered_v2", Operation: tc.op, Policy: "sum", Terms: []Term{term}}))
			})
		}
	}
}
func TestUnknownNonTokenDimensions(t *testing.T) {
	for _, tc := range []struct{ op, unit string }{{"speech", "character"}, {"transcription", "audio_second"}, {"ocr", "request"}, {"video", "video_second"}, {"video", "provider_unit"}} {
		t.Run(tc.unit, func(t *testing.T) {
			require.Error(t, ValidatePrice(Price{Version: "metered_v2", Operation: tc.op, Policy: "sum", Terms: []Term{{Key: "bogus", Unit: tc.unit, ProviderUnit: "", Price: Rational{"1", "1"}}}}))
		})
	}
}
func TestExactSumAndBoundaries(t *testing.T) {
	p := Price{Version: "metered_v2", Operation: "chat", Policy: "sum", Terms: []Term{{Key: "input", Unit: "token", Price: Rational{"1", "3"}}, {Key: "output", Unit: "token", Price: Rational{"1", "2"}}}}
	u := Usage{Version: "metered_v2", Operation: "chat", State: "confirmed", Measurements: []Measurement{{Key: "output", Unit: "token", Quantity: Quantity{"1", 0}}, {Key: "input", Unit: "token", Quantity: Quantity{"1", 0}}}}
	got, err := Calculate(p, u)
	require.NoError(t, err)
	assert.Equal(t, "5/6", got.RatString())
	p = Price{Version: "metered_v2", Operation: "video", Policy: "sum", Terms: []Term{{Key: "video", Unit: "video_second", Price: Rational{"1", "1"}, Step: &Quantity{"1", 9}, Minimum: &Quantity{"1", 9}}}}
	u = Usage{Version: "metered_v2", Operation: "video", State: "confirmed", Measurements: []Measurement{{Key: "video", Unit: "video_second", Quantity: Quantity{"99999999999999999999999999999999999999", 9}}}}
	got, err = Calculate(p, u)
	require.NoError(t, err)
	assert.Equal(t, "99999999999999999999999999999999999999/1000000000", got.RatString())
	u.Measurements[0].Quantity = Quantity{"0", 9}
	got, err = Calculate(p, u)
	require.NoError(t, err)
	assert.Equal(t, "0", got.RatString())
	u.Measurements[0].Quantity.Scale = -1
	_, err = Calculate(p, u)
	require.Error(t, err)
}

func TestOperationUnitMatrix(t *testing.T) {
	for _, tc := range []struct{ op, unit, key string }{{"chat", "request", "requests"}, {"responses", "provider_unit", "provider"}, {"embedding", "character", "characters"}, {"speech", "token", "input"}, {"speech", "audio_second", "audio"}, {"transcription", "token", "input"}, {"ocr", "provider_unit", "provider"}, {"image_generation", "request", "requests"}, {"image_edit", "video_second", "video"}, {"video", "image", "images"}} {
		t.Run(tc.op+tc.unit, func(t *testing.T) {
			p := Price{Version: "metered_v2", Operation: tc.op, Policy: "sum", Terms: []Term{{Key: tc.key, Unit: tc.unit, Price: Rational{"1", "1"}}}}
			if tc.unit == "provider_unit" {
				p.Terms[0].ProviderUnit = "vendor:unit"
			}
			require.Error(t, ValidatePrice(p))
		})
	}
	p := Price{Version: "metered_v2", Operation: "speech", Policy: "sum", Terms: []Term{{Key: "characters", Unit: "character", ProviderUnit: "stray", Price: Rational{"1", "1"}}}}
	require.Error(t, ValidatePrice(p))
}
