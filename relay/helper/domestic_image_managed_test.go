package helper

import (
	"bytes"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/internal/platformbilling"
	"github.com/QuantumNous/new-api/pkg/metering"
	"github.com/QuantumNous/new-api/relay/capability"
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

func TestDomesticImageDeclaredOutputBoundsRequireCurrentTypedProof(t *testing.T) {
	old := ratio_setting.ModelPrice2JSONString()
	require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(`{"qwen-image-2.0":0.01}`))
	t.Cleanup(func() { require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(old)) })
	for _, tc := range []struct {
		name, size string
		channel    int
		maxPixels  int64
		maxImages  int
		n          uint
		wantError  string
	}{
		{"real route", "1024x1024", constant.ChannelTypeAli, 16777216, 4, 1, ""},
		{"declared smaller pixels", "1024x1024", constant.ChannelTypeAli, 262144, 4, 1, "exceeds declared max_pixels"},
		{"declared output count", "1024x1024", constant.ChannelTypeAli, 16777216, 1, 2, "exceeds declared max_images"},
		{"missing dimensions", "", constant.ChannelTypeAli, 16777216, 4, 1, "require verified media metadata"},
		{"unproven provider", "1024x1024", constant.ChannelTypeOpenAI, 16777216, 4, 1, "require verified media metadata"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := dto.ImageRequest{Model: "qwen-image-2.0", Prompt: "a small blue circle", Size: tc.size, N: &tc.n}
			raw, err := common.Marshal(request)
			require.NoError(t, err)
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest("POST", "/v1/images/generations", bytes.NewReader(raw))
			c.Request.Header.Set("Content-Type", "application/json")
			t.Cleanup(func() { common.CleanupBodyStorage(c) })
			info := &relaycommon.RelayInfo{Request: &request, RelayMode: relayconstant.RelayModeImagesGenerations}
			require.NoError(t, ValidateDomesticImage(c, info, tc.channel, request.Model, request.Model))
			contract := dto.ModelCapabilities{Operations: []string{"images.generations.create"}, InputModalities: []string{"text", "image"}, OutputModalities: []string{"image"}, MediaLimits: &dto.MediaLimits{MaxImages: &tc.maxImages, MaxPixels: &tc.maxPixels}}
			err = ValidateModelAdmission(c, info, contract, capability.OperationImageGeneration)
			if tc.wantError != "" {
				require.ErrorContains(t, err, tc.wantError)
			} else {
				require.NoError(t, err)
			}
		})
	}
}
