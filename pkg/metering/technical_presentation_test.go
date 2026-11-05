package metering

import (
	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"os"
	"testing"
)

// The UI consumes this same finite matrix; ValidateUsage is the producer authority.
func TestTechnicalPresentationProducerParity(t *testing.T) {
	raw, err := os.ReadFile("testdata/technical-presentation.json")
	require.NoError(t, err)
	var cases []struct {
		Name  string `json:"name"`
		Valid bool   `json:"valid"`
		Price Price  `json:"price"`
		Usage Usage  `json:"usage"`
	}
	require.NoError(t, common.Unmarshal(raw, &cases))
	for _, tc := range cases {
		t.Run(tc.Name, func(t *testing.T) {
			err := ValidateUsage(tc.Price, tc.Usage)
			if tc.Valid {
				assert.NoError(t, err)
			} else {
				assert.Error(t, err)
			}
		})
	}
}
