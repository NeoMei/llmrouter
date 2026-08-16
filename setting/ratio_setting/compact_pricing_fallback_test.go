package ratio_setting

import (
	"testing"
)

func TestCompactModelPricingFallsBackToBaseModel(t *testing.T) {
	UpdateModelRatioByJSONString(`{"test-base": 2.5}`)
	UpdateModelPriceByJSONString(`{"test-base": 0.03}`)

	ratio, ratioOK, _ := GetModelRatio("test-base" + CompactModelSuffix)
	if !ratioOK || ratio != 2.5 {
		t.Fatalf("compact ratio fallback: got (%v,%v), want 2.5", ratio, ratioOK)
	}
	price, priceOK := GetModelPrice("test-base"+CompactModelSuffix, false)
	if !priceOK || price != 0.03 {
		t.Fatalf("compact price fallback: got (%v,%v), want 0.03", price, priceOK)
	}

	UpdateModelRatioByJSONString(`{"*-openai-compact": 0.1}`)
	ratio, ratioOK, _ = GetModelRatio("test-base" + CompactModelSuffix)
	if !ratioOK || ratio != 0.1 {
		t.Fatalf("compact wildcard precedence: got (%v,%v), want 0.1", ratio, ratioOK)
	}
}
