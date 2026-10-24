package helper

import (
	"math"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMultimodalReservationBounds(t *testing.T) {
	for _, tc := range []struct {
		name                 string
		ratio, visual, group float64
		want                 int
		bad                  bool
	}{
		{"weighted", 2, 3, 1.5, 201000, false}, {"free-group", 2, 3, 0, 0, false}, {"tiny-ratio", 1e-20, 1, 1, 1, false}, {"overflow", 1e100, 3, 1, 0, true}, {"nan", math.NaN(), 3, 1, 0, true}, {"negative", 2, -1, 1, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Set(MultimodalPreparedKey, &PreparedMultimodal{VisualReserve: 1000})
			info := &relaycommon.RelayInfo{Request: &dto.MultimodalEmbeddingRequest{}, PriceData: types.PriceData{ModelRatio: tc.ratio, ImageRatio: tc.visual, GroupRatioInfo: types.GroupRatioInfo{GroupRatio: tc.group}}}
			err := PrepareMultimodalReservation(c, info)
			if tc.bad {
				assert.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tc.want, info.PriceData.QuotaToPreConsume)
			}
			if tc.name == "overflow" {
				assert.NotNil(t, info.QuotaClamp)
			}
		})
	}
}
