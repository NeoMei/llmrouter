package metering

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Fixtures express the provider-reported aggregate and distinct Qwen counters;
// neither fixture treats an estimated DTO count as provider evidence.
func knowledgeOperationFixture(operation string) (Price, Usage) {
	p := Price{Version: "metered_v2", Operation: operation, Policy: "provider_reported_v1"}
	u := Usage{Version: "metered_v2", Operation: operation, State: "confirmed", Source: "provider", ProviderRequestID: "actual-request"}
	if operation == "rerank" {
		p.Terms = []Term{{Key: "input", Unit: "token", Price: Rational{"1", "2"}}}
		u.Measurements = []Measurement{{Key: "input", Unit: "token", Quantity: Quantity{"3", 0}}}
	} else {
		p.Terms = []Term{
			{Key: "text", Unit: "token", Price: Rational{"1", "3"}},
			{Key: "visual", Unit: "token", Price: Rational{"2", "3"}},
		}
		// Measurement order must not change the association with the tariff.
		u.Measurements = []Measurement{
			{Key: "visual", Unit: "token", Quantity: Quantity{"3", 0}},
			{Key: "text", Unit: "token", Quantity: Quantity{"2", 0}},
		}
	}
	return p, u
}

func TestKnowledgeOperationsExactCalculation(t *testing.T) {
	for _, tc := range []struct{ operation, want string }{
		{"rerank", "3/2"},
		{"multimodal_embedding", "8/3"},
	} {
		t.Run(tc.operation, func(t *testing.T) {
			p, u := knowledgeOperationFixture(tc.operation)
			require.NoError(t, ValidatePrice(p))
			require.NoError(t, ValidateUsage(p, u))
			got, err := Calculate(p, u)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got.RatString())
		})
	}
}

func TestKnowledgeOperationsZeroAndExactBoundaries(t *testing.T) {
	for _, operation := range []string{"rerank", "multimodal_embedding"} {
		for _, scenario := range []string{"observed zero", "zero price", "large exact", "step and minimum"} {
			t.Run(operation+"/"+scenario, func(t *testing.T) {
				p, u := knowledgeOperationFixture(operation)
				want := "0"
				switch scenario {
				case "observed zero":
					for i := range u.Measurements {
						u.Measurements[i].Quantity.Value = "0"
					}
					for i := range p.Terms {
						p.Terms[i].Minimum = &Quantity{"10", 0}
						p.Terms[i].Step = &Quantity{"4", 0}
					}
				case "zero price":
					for i := range p.Terms {
						p.Terms[i].Price.Numerator = "0"
					}
				case "large exact":
					for i := range u.Measurements {
						u.Measurements[i].Quantity.Value = "99999999999999999999999999999999999999"
					}
					if operation == "rerank" {
						want = "99999999999999999999999999999999999999/2"
					} else {
						want = "99999999999999999999999999999999999999"
					}
				case "step and minimum":
					for i := range p.Terms {
						p.Terms[i].Minimum = &Quantity{"4", 0}
						p.Terms[i].Step = &Quantity{"3", 0}
					}
					if operation == "rerank" {
						want = "3"
					} else {
						want = "6"
					}
				}
				require.NoError(t, ValidatePrice(p))
				got, err := Calculate(p, u)
				require.NoError(t, err)
				assert.Equal(t, want, got.RatString())
			})
		}
	}
}

func TestKnowledgeOperationsClosedTariffDimensions(t *testing.T) {
	for _, operation := range []string{"rerank", "multimodal_embedding"} {
		for _, key := range []string{"input", "output", "cache_read", "cache_write", "reasoning", "text", "visual", "unknown", strings.Repeat("x", 129)} {
			if (operation == "rerank" && key == "input") || (operation == "multimodal_embedding" && (key == "text" || key == "visual")) {
				continue
			}
			t.Run(operation+"/"+key, func(t *testing.T) {
				p, _ := knowledgeOperationFixture(operation)
				require.NoError(t, ValidatePrice(p))
				p.Terms = append(p.Terms, Term{Key: key, Unit: "token", Price: Rational{"0", "1"}})
				require.Error(t, ValidatePrice(p))
			})
		}
		for _, scenario := range []string{"duplicate", "wrong unit", "negative price", "zero denominator", "nondecimal price", "huge price", "provider namespace", "fractional step", "fractional minimum"} {
			t.Run(operation+"/"+scenario, func(t *testing.T) {
				p, _ := knowledgeOperationFixture(operation)
				require.NoError(t, ValidatePrice(p))
				switch scenario {
				case "duplicate":
					p.Terms = append(p.Terms, p.Terms[0])
				case "wrong unit":
					p.Terms[0].Unit = "request"
				case "negative price":
					p.Terms[0].Price.Numerator = "-1"
				case "zero denominator":
					p.Terms[0].Price.Denominator = "0"
				case "nondecimal price":
					p.Terms[0].Price.Numerator = "1.5"
				case "huge price":
					p.Terms[0].Price.Numerator = "100000000000000000000000000000000000000"
				case "provider namespace":
					p.Terms[0].ProviderUnit = "vendor:unit"
				case "fractional step":
					p.Terms[0].Step = &Quantity{"1", 1}
				case "fractional minimum":
					p.Terms[0].Minimum = &Quantity{"1", 1}
				}
				require.Error(t, ValidatePrice(p))
			})
		}
	}
	for _, missing := range []string{"text", "visual"} {
		t.Run("multimodal_embedding/missing "+missing+" tariff", func(t *testing.T) {
			p, _ := knowledgeOperationFixture("multimodal_embedding")
			require.NoError(t, ValidatePrice(p))
			if missing == "text" {
				p.Terms = p.Terms[1:]
			} else {
				p.Terms = p.Terms[:1]
			}
			// A missing dimension cannot silently become free, even if the
			// remaining price or the eventual observed quantity is zero.
			p.Terms[0].Price.Numerator = "0"
			require.Error(t, ValidatePrice(p))
		})
	}
}

func TestKnowledgeOperationsRejectInvalidUsage(t *testing.T) {
	for _, operation := range []string{"rerank", "multimodal_embedding"} {
		for _, scenario := range []string{"missing", "missing zero", "duplicate", "extra", "wrong unit", "namespace", "fractional", "negative scale", "negative", "nondecimal", "leading zero", "absurd", "pending", "operation", "version"} {
			t.Run(operation+"/"+scenario, func(t *testing.T) {
				p, u := knowledgeOperationFixture(operation)
				require.NoError(t, ValidateUsage(p, u))
				switch scenario {
				case "missing":
					u.Measurements = u.Measurements[1:]
				case "missing zero":
					u.Measurements = u.Measurements[1:]
					for i := range u.Measurements {
						u.Measurements[i].Quantity.Value = "0"
					}
				case "duplicate":
					u.Measurements = append(u.Measurements, u.Measurements[0])
				case "extra":
					u.Measurements = append(u.Measurements, Measurement{Key: "output", Unit: "token", Quantity: Quantity{"0", 0}})
				case "wrong unit":
					u.Measurements[0].Unit = "image"
				case "namespace":
					u.Measurements[0].ProviderUnit = "vendor:unit"
				case "fractional":
					u.Measurements[0].Quantity = Quantity{"10", 1}
				case "negative scale":
					u.Measurements[0].Quantity.Scale = -1
				case "negative":
					u.Measurements[0].Quantity.Value = "-1"
				case "nondecimal":
					u.Measurements[0].Quantity.Value = "1e3"
				case "leading zero":
					u.Measurements[0].Quantity.Value = "01"
				case "absurd":
					u.Measurements[0].Quantity.Value = "100000000000000000000000000000000000000"
				case "pending":
					u.State = "pending"
				case "operation":
					u.Operation = "embedding"
				case "version":
					u.Version = "metered_v1"
				}
				require.Error(t, ValidateUsage(p, u))
				got, err := Calculate(p, u)
				require.Error(t, err)
				assert.Nil(t, got)
			})
		}
	}
}

func TestKnowledgeOperationsDoNotWidenExistingTokenDimensions(t *testing.T) {
	for _, operation := range []string{"chat", "responses", "embedding"} {
		for _, key := range []string{"text", "visual"} {
			t.Run(operation+"/"+key, func(t *testing.T) {
				p := Price{Version: "metered_v2", Operation: operation, Policy: "sum", Terms: []Term{{Key: key, Unit: "token", Price: Rational{"1", "1"}}}}
				require.Error(t, ValidatePrice(p))
			})
		}
	}
	p, _ := knowledgeOperationFixture("rerank")
	p.Operation = "unknown"
	require.Error(t, ValidatePrice(p))
}
