package service

import (
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/setting/config"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"math"
	"net/http/httptest"
	"testing"
)

func TestManagedSupplementalPricesSurviveOperatorPriceChanges(t *testing.T) {
	previousUnit := common.QuotaPerUnit
	toolSetting := config.GlobalConfig.Get("tool_price_setting").(*operation_setting.ToolPriceSetting)
	previousTools := toolSetting.Prices
	t.Cleanup(func() {
		common.QuotaPerUnit = previousUnit
		toolSetting.Prices = previousTools
		operation_setting.RebuildToolPriceIndex()
	})
	common.QuotaPerUnit = 1000000
	toolSetting.Prices = map[string]float64{"web_search_preview": 10, "web_search": 11, "file_search": 2}
	operation_setting.RebuildToolPriceIndex()
	info := &relaycommon.RelayInfo{OriginModelName: "frozen-model", PriceData: types.PriceData{UsePrice: true, ModelPrice: 0.01, GroupRatioInfo: types.GroupRatioInfo{GroupRatio: 1}}, ResponsesUsageInfo: &relaycommon.ResponsesUsageInfo{BuiltInTools: map[string]*relaycommon.BuildInToolInfo{dto.BuildInToolWebSearchPreview: {CallCount: 1}, dto.BuildInToolFileSearch: {CallCount: 1}}}}
	require.NoError(t, FreezeManagedSupplementalPrices(info))
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Set("claude_web_search_requests", 1)
	usage := &dto.Usage{PromptTokens: 1, TotalTokens: 1}
	before := calculateTextQuotaSummary(ctx, info, usage)
	require.Equal(t, 33000, before.Quota)
	common.QuotaPerUnit = 2000000
	toolSetting.Prices = map[string]float64{"web_search_preview": 100, "web_search": 110, "file_search": 20}
	operation_setting.RebuildToolPriceIndex()
	after := calculateTextQuotaSummary(ctx, info, usage)
	require.Equal(t, before.Quota, after.Quota, "settlement must use the admitted native price snapshot")
	// A fresh ordinary native request retains the existing live price behavior.
	ordinary := *info
	ordinary.ManagedNativeSupplemental = nil
	require.Equal(t, 480000, calculateTextQuotaSummary(ctx, &ordinary, usage).Quota)
}

func TestManagedOpenRouterCachePriceUsesAdmittedUnit(t *testing.T) {
	previousUnit := common.QuotaPerUnit
	t.Cleanup(func() { common.QuotaPerUnit = previousUnit })
	common.QuotaPerUnit = 1000000
	info := &relaycommon.RelayInfo{
		OriginModelName: "claude-3-5-sonnet-20240620",
		ChannelMeta:     &relaycommon.ChannelMeta{ChannelType: constant.ChannelTypeOpenRouter},
		PriceData:       types.PriceData{ModelRatio: 1.5, CompletionRatio: 1, CacheRatio: 0.1, CacheCreationRatio: 1.25, GroupRatioInfo: types.GroupRatioInfo{GroupRatio: 1}},
	}
	require.NoError(t, FreezeManagedSupplementalPrices(info))
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	usage := &dto.Usage{PromptTokens: 100, TotalTokens: 100, UsageSemantic: "anthropic", Cost: 0.00016875}
	before := calculateTextQuotaSummary(ctx, info, usage)
	require.Equal(t, 50, before.CacheCreationTokens)
	require.Equal(t, 169, before.Quota)
	common.QuotaPerUnit = 2000000
	after := calculateTextQuotaSummary(ctx, info, usage)
	require.Equal(t, before.CacheCreationTokens, after.CacheCreationTokens)
	require.Equal(t, before.Quota, after.Quota)
}

func TestManagedAudioPriceUsesAdmittedUnitAndRatios(t *testing.T) {
	previousUnit := common.QuotaPerUnit
	previousCompletion := ratio_setting.CompletionRatio2JSONString()
	previousAudio := ratio_setting.AudioRatio2JSONString()
	previousAudioCompletion := ratio_setting.AudioCompletionRatio2JSONString()
	t.Cleanup(func() {
		common.QuotaPerUnit = previousUnit
		require.NoError(t, ratio_setting.UpdateCompletionRatioByJSONString(previousCompletion))
		require.NoError(t, ratio_setting.UpdateAudioRatioByJSONString(previousAudio))
		require.NoError(t, ratio_setting.UpdateAudioCompletionRatioByJSONString(previousAudioCompletion))
	})
	common.QuotaPerUnit = 1000000
	relay := &relaycommon.RelayInfo{OriginModelName: "frozen-audio", PriceData: types.PriceData{CompletionRatio: 2, AudioRatio: 3, AudioCompletionRatio: 4}}
	require.NoError(t, FreezeManagedSupplementalPrices(relay))
	info := QuotaInfo{ModelName: "frozen-audio", ProductPriceSnapshot: &relay.PriceData, SupplementalPriceSnapshot: relay.ManagedNativeSupplemental, ModelPrice: 0.01, ModelRatio: 2, GroupRatio: 1.5, InputDetails: TokenDetails{TextTokens: 10, AudioTokens: 30}, OutputDetails: TokenDetails{TextTokens: 20, AudioTokens: 40}}
	common.QuotaPerUnit = 2000000
	require.NoError(t, ratio_setting.UpdateCompletionRatioByJSONString(`{"frozen-audio":20}`))
	require.NoError(t, ratio_setting.UpdateAudioRatioByJSONString(`{"frozen-audio":30}`))
	require.NoError(t, ratio_setting.UpdateAudioCompletionRatioByJSONString(`{"frozen-audio":40}`))
	quota, clamp := calculateAudioQuota(info)
	require.Nil(t, clamp)
	require.Equal(t, 1860, quota)
	info.UsePrice = true
	quota, clamp = calculateAudioQuota(info)
	require.Nil(t, clamp)
	require.Equal(t, 15000, quota)
	info.SupplementalPriceSnapshot = nil
	quota, clamp = calculateAudioQuota(info)
	require.Nil(t, clamp)
	require.Equal(t, 30000, quota, "ordinary native fixed prices keep their existing unit behavior")
}

func TestManagedSupplementalPricesRejectInvalidInputsWithoutSnapshot(t *testing.T) {
	previousUnit := common.QuotaPerUnit
	t.Cleanup(func() { common.QuotaPerUnit = previousUnit })
	for _, unit := range []float64{0, -1, math.NaN(), math.Inf(1)} {
		common.QuotaPerUnit = unit
		info := &relaycommon.RelayInfo{OriginModelName: "frozen-model"}
		require.Error(t, FreezeManagedSupplementalPrices(info))
		require.Nil(t, info.ManagedNativeSupplemental)
	}
	common.QuotaPerUnit = 1000000
	info := &relaycommon.RelayInfo{OriginModelName: "frozen-model"}
	require.NoError(t, FreezeManagedSupplementalPrices(info))
	common.QuotaPerUnit = 2000000
	require.NoError(t, FreezeManagedSupplementalPrices(info))
	require.Equal(t, float64(1000000), info.ManagedNativeSupplemental.QuotaPerUnit, "retry cannot refresh an admitted price")
}
