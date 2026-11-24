package openai

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/tidwall/gjson"
)

type arkImageTariff struct {
	outputPrice float64
	inputPrice  float64
	tiered      bool
}

func arkImageTariffForModel(model string) (arkImageTariff, bool) {
	switch {
	case strings.Contains(model, "seedream-5-5"):
		return arkImageTariff{outputPrice: 0.13, inputPrice: 0.05, tiered: true}, true
	case strings.Contains(model, "seedream-5-0-pro"):
		return arkImageTariff{outputPrice: 0.30, inputPrice: 0.02, tiered: true}, true
	case strings.Contains(model, "seedream-5-0-flash"):
		return arkImageTariff{outputPrice: 0.12}, true
	case strings.Contains(model, "seedream-5-0"):
		return arkImageTariff{outputPrice: 0.22}, true
	case strings.Contains(model, "seedream-4-5"):
		return arkImageTariff{outputPrice: 0.25}, true
	case strings.Contains(model, "seedream-4-0"):
		return arkImageTariff{outputPrice: 0.20}, true
	default:
		return arkImageTariff{}, false
	}
}

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
	if _, ok := arkImageTariffForModel(info.UpstreamModelName); !ok {
		return fmt.Errorf("Ark image model has no verified per-image price: %s", info.UpstreamModelName)
	}
	if !info.PriceData.UsePrice {
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

	tariff, _ := arkImageTariffForModel(info.UpstreamModelName)
	outputSubtotal := float64(outputCount) * tariff.outputPrice
	if tariff.tiered {
		highPrice := 0.23
		if strings.Contains(info.UpstreamModelName, "seedream-5-0-pro") {
			highPrice = 0.60
		}
		for _, item := range gjson.GetBytes(responseBody, "data").Array() {
			dimensions := strings.Split(item.Get("size").String(), "x")
			if len(dimensions) != 2 {
				return fmt.Errorf("Ark tiered image price requires each output image's actual dimensions")
			}
			width, widthErr := strconv.ParseInt(dimensions[0], 10, 64)
			height, heightErr := strconv.ParseInt(dimensions[1], 10, 64)
			if widthErr != nil || heightErr != nil || width <= 0 || height <= 0 || width > 32768 || height > 32768 {
				return fmt.Errorf("Ark image response has invalid output dimensions")
			}
			if width*height > 2610000 {
				outputSubtotal += highPrice - tariff.outputPrice
			}
		}
	}

	inputCount := gjson.GetBytes(responseBody, "usage.input_images").Int()
	if inputCount < 0 || inputCount > int64(dto.MaxImageN) {
		return fmt.Errorf("Ark image response has an invalid input count: %d", inputCount)
	}
	if strings.Contains(info.UpstreamModelName, "seedream-5-0-pro") && inputCount > 0 {
		inputCount--
	}
	totalPrice := outputSubtotal + float64(inputCount)*tariff.inputPrice
	basePrice := float64(outputCount) * tariff.outputPrice
	if totalPrice != basePrice {
		info.PriceData.AddOtherRatio("ark_actual_charges", totalPrice/basePrice)
	}
	return nil
}
