package helper

import "strings"

type ArkImageTariff struct {
	OutputPrice float64
	InputPrice  float64
	Tiered      bool
}

func ArkImageTariffForModel(model string) (ArkImageTariff, bool) {
	switch {
	case strings.Contains(model, "seedream-5-5"):
		return ArkImageTariff{OutputPrice: 0.13, InputPrice: 0.05, Tiered: true}, true
	case strings.Contains(model, "seedream-5-0-pro"):
		return ArkImageTariff{OutputPrice: 0.30, InputPrice: 0.02, Tiered: true}, true
	case strings.Contains(model, "seedream-5-0-flash"):
		return ArkImageTariff{OutputPrice: 0.12}, true
	case strings.Contains(model, "seedream-5-0"):
		return ArkImageTariff{OutputPrice: 0.22}, true
	case strings.Contains(model, "seedream-4-5"):
		return ArkImageTariff{OutputPrice: 0.25}, true
	case strings.Contains(model, "seedream-4-0"):
		return ArkImageTariff{OutputPrice: 0.20}, true
	default:
		return ArkImageTariff{}, false
	}
}
