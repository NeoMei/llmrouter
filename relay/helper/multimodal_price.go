package helper

import (
	"fmt"
	"math"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"
)

// Reserve configured policy ceilings, never an alleged actual frame/token count.
func PrepareMultimodalReservation(c *gin.Context, info *relaycommon.RelayInfo) error {
	if _, ok := info.Request.(*dto.MultimodalEmbeddingRequest); !ok {
		return nil
	}
	value, ok := c.Get(MultimodalPreparedKey)
	if !ok {
		return fmt.Errorf("multimodal inputs not prepared")
	}
	p := value.(*PreparedMultimodal)
	price := &info.PriceData
	for _, v := range []float64{price.ModelRatio, price.ImageRatio, price.GroupRatioInfo.GroupRatio} {
		if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
			return fmt.Errorf("invalid multimodal price ratio")
		}
	}
	if price.UsePrice {
		if math.IsNaN(price.ModelPrice) || math.IsInf(price.ModelPrice, 0) || price.ModelPrice < 0 {
			return fmt.Errorf("invalid multimodal per-request price")
		}
		if price.ModelPrice > 0 && price.GroupRatioInfo.GroupRatio > 0 && price.QuotaToPreConsume == 0 {
			return fmt.Errorf("multimodal per-request price is below quota precision")
		}
		return nil
	}
	amount := decimal.NewFromInt(MultimodalTextReserve).Add(decimal.NewFromInt(int64(p.VisualReserve)).Mul(decimal.NewFromFloat(price.ImageRatio))).Mul(decimal.NewFromFloat(price.ModelRatio)).Mul(decimal.NewFromFloat(price.GroupRatioInfo.GroupRatio))
	quota, clamp := common.QuotaFromDecimalChecked(amount)
	if clamp != nil {
		info.QuotaClamp = clamp
		return fmt.Errorf("multimodal reservation exceeds quota bounds")
	}
	if amount.IsPositive() && quota == 0 {
		quota = 1
	} // Settlement has the same nonzero-charge floor.
	price.QuotaToPreConsume = quota
	return nil
}
