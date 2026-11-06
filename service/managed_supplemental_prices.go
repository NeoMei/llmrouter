package service

import (
	"errors"
	"github.com/QuantumNous/new-api/common"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/QuantumNous/new-api/types"
	"math"
)

func FreezeManagedSupplementalPrices(info *relaycommon.RelayInfo) error {
	if info == nil {
		return errors.New("native price context missing")
	}
	if info.ManagedNativeSupplemental != nil {
		return nil
	}
	snapshot := &types.SupplementalPriceSnapshot{
		QuotaPerUnit:          common.QuotaPerUnit,
		WebSearchPreviewPrice: operation_setting.GetToolPriceForModel("web_search_preview", info.OriginModelName),
		WebSearchPrice:        operation_setting.GetToolPrice("web_search"),
		FileSearchPrice:       operation_setting.GetToolPrice("file_search"),
		GeminiInputAudioPrice: operation_setting.GetGeminiInputAudioPricePerMillionTokens(info.OriginModelName),
		GPTImageCalls:         map[string]map[string]float64{},
	}
	values := []float64{snapshot.QuotaPerUnit, snapshot.WebSearchPreviewPrice, snapshot.WebSearchPrice, snapshot.FileSearchPrice, snapshot.GeminiInputAudioPrice}
	for _, quality := range []string{"low", "medium", "high"} {
		snapshot.GPTImageCalls[quality] = map[string]float64{}
		for _, size := range []string{"1024x1024", "1024x1536", "1536x1024"} {
			price := operation_setting.GetGPTImage1PriceOnceCall(quality, size)
			snapshot.GPTImageCalls[quality][size] = price
			values = append(values, price)
		}
	}
	for _, value := range values {
		if value < 0 || math.IsNaN(value) || math.IsInf(value, 0) {
			return errors.New("native supplemental price invalid")
		}
	}
	if snapshot.QuotaPerUnit == 0 {
		return errors.New("native quota unit missing")
	}
	info.ManagedNativeSupplemental = snapshot
	return nil
}

func relayQuotaPerUnit(info *relaycommon.RelayInfo) float64 {
	if info.ManagedNativeSupplemental != nil {
		return info.ManagedNativeSupplemental.QuotaPerUnit
	}
	return common.QuotaPerUnit
}

func relayImageCallPrice(info *relaycommon.RelayInfo, quality, size string) float64 {
	if info.ManagedNativeSupplemental == nil {
		return operation_setting.GetGPTImage1PriceOnceCall(quality, size)
	}
	prices := info.ManagedNativeSupplemental.GPTImageCalls
	if price, ok := prices[quality][size]; ok {
		return price
	}
	return prices["high"]["1024x1024"]
}
