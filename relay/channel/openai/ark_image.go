package openai

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/tidwall/gjson"
)

func ValidateArkImageBilling(info *relaycommon.RelayInfo, request *dto.ImageRequest) error {
	if info.ChannelType != constant.ChannelTypeVolcEngine {
		return nil
	}
	if info.IsStream {
		return fmt.Errorf("Ark streaming image billing has not been verified")
	}
	if request != nil && request.Extra != nil {
		if enabled, exists := request.Extra["layer_decomposition"]; exists && string(enabled) == "true" {
			return fmt.Errorf("Ark layer_decomposition requires a separately verified per-layer price contract")
		}
	}
	if common.IsSeedreamModel(info.UpstreamModelName) && !info.PriceData.UsePrice {
		return fmt.Errorf("Ark image models require per-image ModelPrice configuration")
	}
	return nil
}

func applyArkImageBilling(info *relaycommon.RelayInfo, responseBody []byte) error {
	if info.ChannelType != constant.ChannelTypeVolcEngine {
		return nil
	}
	outputCount := gjson.GetBytes(responseBody, "data.#").Int()
	if outputCount <= 0 || outputCount > int64(dto.MaxImageN) {
		return fmt.Errorf("Ark image response has an invalid output count: %d", outputCount)
	}
	if err := ValidateArkImageBilling(info, nil); err != nil {
		return err
	}

	model := info.UpstreamModelName
	outputPrice := 0.0
	inputPrice := 0.0
	tiered := false
	switch {
	case strings.Contains(model, "seedream-5-5"):
		outputPrice, inputPrice, tiered = 0.13, 0.05, true
	case strings.Contains(model, "seedream-5-0-pro"):
		outputPrice, inputPrice, tiered = 0.30, 0.02, true
	case strings.Contains(model, "seedream-5-0-flash"):
		outputPrice = 0.12
	case strings.Contains(model, "seedream-5-0"):
		outputPrice = 0.22
	case strings.Contains(model, "seedream-4-5"):
		outputPrice = 0.25
	case strings.Contains(model, "seedream-4-0"):
		outputPrice = 0.20
	default:
		return fmt.Errorf("Ark image model has no verified per-image price: %s", model)
	}

	inputCount := gjson.GetBytes(responseBody, "usage.input_images").Int()
	if inputCount < 0 || inputCount > int64(dto.MaxImageN) {
		return fmt.Errorf("Ark image response has an invalid input count: %d", inputCount)
	}
	if strings.Contains(model, "seedream-5-0-pro") && inputCount > 0 {
		inputCount--
	}
	if inputCount > 0 && inputPrice > 0 {
		inputRatio := 1 + float64(inputCount)*inputPrice/(float64(outputCount)*outputPrice)
		info.PriceData.AddOtherRatio("ark_input_images", inputRatio)
	}

	if tiered {
		dimensions := strings.Split(gjson.GetBytes(responseBody, "size").String(), "x")
		if len(dimensions) != 2 {
			return fmt.Errorf("Ark tiered image price requires the actual output dimensions")
		}
		width, widthErr := strconv.ParseInt(dimensions[0], 10, 64)
		height, heightErr := strconv.ParseInt(dimensions[1], 10, 64)
		if widthErr != nil || heightErr != nil || width <= 0 || height <= 0 || width > 32768 || height > 32768 {
			return fmt.Errorf("Ark image response has invalid output dimensions")
		}
		if width*height > 2610000 {
			highPrice := 0.23
			if strings.Contains(model, "seedream-5-0-pro") {
				highPrice = 0.60
			}
			info.PriceData.AddOtherRatio("ark_resolution", highPrice/outputPrice)
		}
	}
	return nil
}
