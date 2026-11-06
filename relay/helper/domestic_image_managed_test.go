package helper

import (
	"bytes"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/internal/platformbilling"
	"github.com/QuantumNous/new-api/pkg/metering"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/setting/billing_setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"net/http/httptest"
	"testing"
)

func TestManagedDomesticImagePriceGatePreservesNativePriceRequirement(t *testing.T) {
	old := ratio_setting.ModelPrice2JSONString()
	require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(`{}`))
	t.Cleanup(func() { require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(old)) })
	for _, managed := range []bool{false, true} {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest("POST", "/v1/images/generations", bytes.NewBufferString(`{"model":"qwen-image-2.0","prompt":"slide","n":1,"seed":0,"watermark":false}`))
		c.Request.Header.Set("Content-Type", "application/json")
		info := &relaycommon.RelayInfo{Request: &dto.ImageRequest{Prompt: "slide"}, RelayMode: relayconstant.RelayModeImagesGenerations, PlatformMode: managed}
		if managed {
			info.PlatformBilling = &platformbilling.Session{}
		}
		e := ValidateDomesticImage(c, info, constant.ChannelTypeAli, "qwen-image-2.0", "qwen-image-2.0")
		if managed {
			require.NoError(t, e)
		} else {
			require.ErrorContains(t, e, "per-image price")
		}
	}
}

func TestDomesticImageAcceptsOnlyMatchingNativePrice(t *testing.T) {
	oldPrices, oldNative := ratio_setting.ModelPrice2JSONString(), billing_setting.GetModelNativePricingCopy()
	t.Cleanup(func() {
		require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(oldPrices))
		require.NoError(t, billing_setting.SetModelNativePricing(oldNative))
	})
	require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(`{}`))
	price := metering.Price{Version: "metered_v2", Operation: "image_generation", Policy: "successful_outputs_v1", Terms: []metering.Term{{Key: "images", Unit: "image", Price: metering.Rational{Numerator: "1", Denominator: "50"}}}}
	require.NoError(t, billing_setting.SetModelNativePricing(map[string]map[string]metering.Price{"qwen-image-2.0": {"image_generation": price}}))
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1/images/generations", bytes.NewBufferString(`{"model":"qwen-image-2.0","prompt":"slide","n":1}`))
	c.Request.Header.Set("Content-Type", "application/json")
	info := &relaycommon.RelayInfo{Request: &dto.ImageRequest{Prompt: "slide"}, RelayMode: relayconstant.RelayModeImagesGenerations}
	require.NoError(t, ValidateDomesticImage(c, info, constant.ChannelTypeAli, "qwen-image-2.0", "qwen-image-2.0"))
	info.RelayMode = relayconstant.RelayModeImagesEdits
	require.ErrorContains(t, ValidateDomesticImage(c, info, constant.ChannelTypeAli, "qwen-image-2.0", "qwen-image-2.0"), "per-image price")
}
