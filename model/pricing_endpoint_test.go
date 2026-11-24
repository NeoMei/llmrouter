package model

import (
	"fmt"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func resetPricingEndpointTestTables(t *testing.T) {
	t.Helper()
	originalMemoryCacheEnabled := common.MemoryCacheEnabled
	common.MemoryCacheEnabled = true
	require.NoError(t, DB.AutoMigrate(&Channel{}, &Ability{}, &Model{}, &Vendor{}, &ClientModelRoute{}, &ClientModelTarget{}))
	for _, table := range []string{"abilities", "channels", "models", "vendors", "client_model_targets", "client_model_routes"} {
		require.NoError(t, DB.Exec("DELETE FROM "+table).Error)
	}
	InitChannelCache()
	InvalidatePricingCache()
	t.Cleanup(func() {
		for _, table := range []string{"abilities", "channels", "models", "vendors", "client_model_targets", "client_model_routes"} {
			require.NoError(t, DB.Exec("DELETE FROM "+table).Error)
		}
		InitChannelCache()
		InvalidatePricingCache()
		common.MemoryCacheEnabled = originalMemoryCacheEnabled
	})
}

func insertPricingEndpointChannel(t *testing.T, channelID int, channelType int, settings dto.ChannelOtherSettings) {
	t.Helper()
	channel := &Channel{
		Id:     channelID,
		Type:   channelType,
		Key:    fmt.Sprintf("key-%d", channelID),
		Status: common.ChannelStatusEnabled,
		Name:   fmt.Sprintf("channel-%d", channelID),
	}
	if settings.AdvancedCustom != nil {
		channel.SetOtherSettings(settings)
	}
	require.NoError(t, DB.Create(channel).Error)
}

func insertPricingEndpointAbility(t *testing.T, channelID int, modelName string) {
	t.Helper()
	require.NoError(t, DB.Create(&Ability{
		Group:     "default",
		Model:     modelName,
		ChannelId: channelID,
		Enabled:   true,
	}).Error)
}

func pricingEndpointAdvancedCustomConfig(routes ...dto.AdvancedCustomRoute) dto.ChannelOtherSettings {
	return dto.ChannelOtherSettings{
		AdvancedCustom: &dto.AdvancedCustomConfig{
			Routes: routes,
		},
	}
}

func pricingEndpointTypesByModel(t *testing.T) map[string][]constant.EndpointType {
	t.Helper()
	InitChannelCache()
	return pricingEndpointTypesFromPricing(GetPricing())
}

func pricingEndpointTypesFromPricing(pricings []Pricing) map[string][]constant.EndpointType {
	byModel := make(map[string][]constant.EndpointType)
	for _, pricing := range pricings {
		byModel[pricing.ModelName] = pricing.SupportedEndpointTypes
	}
	return byModel
}

func TestPricingAdvancedCustomUsesConfiguredEndpointTypes(t *testing.T) {
	resetPricingEndpointTestTables(t)

	insertPricingEndpointChannel(t, 101, constant.ChannelTypeAdvancedCustom, pricingEndpointAdvancedCustomConfig(
		dto.AdvancedCustomRoute{
			IncomingPath: "/v1/chat/completions",
			UpstreamPath: "/v1/chat/completions",
		},
		dto.AdvancedCustomRoute{
			IncomingPath: "/v1/responses",
			UpstreamPath: "/v1beta/models/{model}:generateContent",
			Converter:    "openai_responses_to_gemini_generate_content",
			Models:       []string{"re:^gemini-"},
		},
	))
	insertPricingEndpointAbility(t, 101, "gemini-2.5-flash")
	insertPricingEndpointAbility(t, 101, "gpt-4o")

	byModel := pricingEndpointTypesByModel(t)

	assert.Equal(t, []constant.EndpointType{
		constant.EndpointTypeOpenAI,
		constant.EndpointTypeOpenAIResponse,
	}, byModel["gemini-2.5-flash"])
	assert.Equal(t, []constant.EndpointType{
		constant.EndpointTypeOpenAI,
	}, byModel["gpt-4o"])
}

func TestPricingModelMetadataEndpointsMergeWithAdvancedCustomInference(t *testing.T) {
	resetPricingEndpointTestTables(t)

	insertPricingEndpointChannel(t, 103, constant.ChannelTypeAdvancedCustom, pricingEndpointAdvancedCustomConfig(
		dto.AdvancedCustomRoute{
			IncomingPath: "/v1/responses",
			UpstreamPath: "/v1beta/models/{model}:generateContent",
			Converter:    "openai_responses_to_gemini_generate_content",
			Models:       []string{"re:^gemini-"},
		},
	))
	insertPricingEndpointAbility(t, 103, "gemini-2.5-flash")
	require.NoError(t, DB.Create(&Model{
		ModelName: "gemini-2.5-flash",
		Endpoints: `{
			"openai": "/v1/chat/completions"
		}`,
		Status:   1,
		NameRule: NameRuleExact,
	}).Error)

	byModel := pricingEndpointTypesByModel(t)

	assert.Equal(t, []constant.EndpointType{
		constant.EndpointTypeOpenAIResponse,
		constant.EndpointTypeOpenAI,
	}, byModel["gemini-2.5-flash"])
}

func TestPricingModelMetadataEndpointsCanProvideEndpointWithoutChannelInference(t *testing.T) {
	resetPricingEndpointTestTables(t)

	insertPricingEndpointChannel(t, 104, constant.ChannelTypeAdvancedCustom, pricingEndpointAdvancedCustomConfig(
		dto.AdvancedCustomRoute{
			IncomingPath: "/v1/responses",
			UpstreamPath: "/v1beta/models/{model}:generateContent",
			Converter:    "openai_responses_to_gemini_generate_content",
			Models:       []string{"re:^gemini-"},
		},
	))
	insertPricingEndpointAbility(t, 104, "metadata-only-model")
	require.NoError(t, DB.Create(&Model{
		ModelName: "metadata-only-model",
		Endpoints: `{
			"openai": "/v1/chat/completions"
		}`,
		Status:   1,
		NameRule: NameRuleExact,
	}).Error)

	byModel := pricingEndpointTypesByModel(t)

	assert.Equal(t, []constant.EndpointType{constant.EndpointTypeOpenAI}, byModel["metadata-only-model"])
}

func TestPricingAdvancedCustomMissingConfigFallsBackToChannelType(t *testing.T) {
	resetPricingEndpointTestTables(t)

	insertPricingEndpointChannel(t, 102, constant.ChannelTypeAdvancedCustom, dto.ChannelOtherSettings{})
	insertPricingEndpointAbility(t, 102, "gpt-4o")

	byModel := pricingEndpointTypesByModel(t)

	assert.Equal(t, []constant.EndpointType{constant.EndpointTypeOpenAI}, byModel["gpt-4o"])
}

func TestPricingNativeChannelEndpointTypesUnchanged(t *testing.T) {
	resetPricingEndpointTestTables(t)

	insertPricingEndpointChannel(t, 201, constant.ChannelTypeOpenAI, dto.ChannelOtherSettings{})
	insertPricingEndpointChannel(t, 202, constant.ChannelTypeGemini, dto.ChannelOtherSettings{})
	insertPricingEndpointChannel(t, 203, constant.ChannelTypeAnthropic, dto.ChannelOtherSettings{})
	insertPricingEndpointAbility(t, 201, "gpt-4o")
	insertPricingEndpointAbility(t, 202, "gemini-2.5-flash")
	insertPricingEndpointAbility(t, 203, "claude-3-5-sonnet")

	byModel := pricingEndpointTypesByModel(t)

	assert.Equal(t, []constant.EndpointType{constant.EndpointTypeOpenAI}, byModel["gpt-4o"])
	assert.Equal(t, []constant.EndpointType{constant.EndpointTypeGemini, constant.EndpointTypeOpenAI}, byModel["gemini-2.5-flash"])
	assert.Equal(t, []constant.EndpointType{constant.EndpointTypeAnthropic, constant.EndpointTypeOpenAI}, byModel["claude-3-5-sonnet"])
}

func TestInitChannelCacheInvalidatesPricingCache(t *testing.T) {
	resetPricingEndpointTestTables(t)

	insertPricingEndpointChannel(t, 301, constant.ChannelTypeAdvancedCustom, pricingEndpointAdvancedCustomConfig(
		dto.AdvancedCustomRoute{
			IncomingPath: "/v1/chat/completions",
			UpstreamPath: "/v1/chat/completions",
		},
	))
	insertPricingEndpointAbility(t, 301, "gemini-3.5-flash")
	InitChannelCache()

	initial := pricingEndpointTypesByModel(t)
	require.Equal(t, []constant.EndpointType{constant.EndpointTypeOpenAI}, initial["gemini-3.5-flash"])

	var channel Channel
	require.NoError(t, DB.First(&channel, "id = ?", 301).Error)
	channel.SetOtherSettings(pricingEndpointAdvancedCustomConfig(
		dto.AdvancedCustomRoute{
			IncomingPath: "/v1/chat/completions",
			UpstreamPath: "/v1/chat/completions",
		},
		dto.AdvancedCustomRoute{
			IncomingPath: "/v1/responses",
			UpstreamPath: "/v1beta/models/{model}:generateContent",
			Converter:    "openai_responses_to_gemini_generate_content",
			Models:       []string{"re:^gemini-"},
		},
	))
	require.NoError(t, DB.Model(&Channel{}).Where("id = ?", 301).Update("settings", channel.OtherSettings).Error)
	InitChannelCache()

	updated := pricingEndpointTypesByModel(t)
	assert.Equal(t, []constant.EndpointType{
		constant.EndpointTypeOpenAI,
		constant.EndpointTypeOpenAIResponse,
	}, updated["gemini-3.5-flash"])
}

func TestInitChannelCacheInvalidatesStartupPricingBuiltBeforeChannelCache(t *testing.T) {
	resetPricingEndpointTestTables(t)

	insertPricingEndpointChannel(t, 302, constant.ChannelTypeAdvancedCustom, pricingEndpointAdvancedCustomConfig(
		dto.AdvancedCustomRoute{
			IncomingPath: "/v1/chat/completions",
			UpstreamPath: "/v1/chat/completions",
		},
		dto.AdvancedCustomRoute{
			IncomingPath: "/v1/responses",
			UpstreamPath: "/v1beta/models/{model}:generateContent",
			Converter:    "openai_responses_to_gemini_generate_content",
			Models:       []string{"re:^gemini-"},
		},
	))
	insertPricingEndpointAbility(t, 302, "gemini-3.5-flash")

	staleByModel := pricingEndpointTypesFromPricing(GetPricing())
	require.Equal(t, []constant.EndpointType{constant.EndpointTypeOpenAI}, staleByModel["gemini-3.5-flash"])

	InitChannelCache()

	rebuiltByModel := pricingEndpointTypesFromPricing(GetPricing())
	assert.Equal(t, []constant.EndpointType{
		constant.EndpointTypeOpenAI,
		constant.EndpointTypeOpenAIResponse,
	}, rebuiltByModel["gemini-3.5-flash"])
}

func TestCacheUpdateChannelSyncsAdvancedCustomConfig(t *testing.T) {
	resetPricingEndpointTestTables(t)

	channel := &Channel{
		Id:     401,
		Type:   constant.ChannelTypeAdvancedCustom,
		Key:    "key-401",
		Status: common.ChannelStatusEnabled,
		Name:   "channel-401",
	}
	channel.SetOtherSettings(pricingEndpointAdvancedCustomConfig(dto.AdvancedCustomRoute{
		IncomingPath: "/v1/responses",
		UpstreamPath: "/v1beta/models/{model}:generateContent",
		Converter:    "openai_responses_to_gemini_generate_content",
	}))
	CacheUpdateChannel(channel)

	require.NotNil(t, channel2advancedCustomConfig[401])
	assert.Equal(t, []constant.EndpointType{constant.EndpointTypeOpenAIResponse}, channel2advancedCustomConfig[401].SupportedEndpointTypesForModel("gemini-3.5-flash"))

	channel.SetOtherSettings(pricingEndpointAdvancedCustomConfig(dto.AdvancedCustomRoute{
		IncomingPath: "/v1/chat/completions",
		UpstreamPath: "/v1/chat/completions",
	}))
	CacheUpdateChannel(channel)

	require.NotNil(t, channel2advancedCustomConfig[401])
	assert.Equal(t, []constant.EndpointType{constant.EndpointTypeOpenAI}, channel2advancedCustomConfig[401].SupportedEndpointTypesForModel("gemini-3.5-flash"))

	channel.Type = constant.ChannelTypeOpenAI
	CacheUpdateChannel(channel)

	assert.Nil(t, channel2advancedCustomConfig[401])
}

func TestPricingArkFallbackIsReadOnlyAndReflectsChannelModality(t *testing.T) {
	resetPricingEndpointTestTables(t)
	insertPricingEndpointChannel(t, 145, constant.ChannelTypeVolcEngine, dto.ChannelOtherSettings{})
	insertPricingEndpointChannel(t, 154, constant.ChannelTypeDoubaoVideo, dto.ChannelOtherSettings{})
	insertPricingEndpointAbility(t, 145, "doubao-seedream-5-5-261028")
	insertPricingEndpointAbility(t, 145, "doubao-seedance-misconfigured")
	insertPricingEndpointAbility(t, 154, "doubao-seedance-2-5-260628")
	require.NoError(t, (&Model{
		ModelName: "doubao-seedance-misconfigured",
		Status:    1,
		Endpoints: `{"openai-video":"/v1/videos"}`,
	}).Insert())

	for _, refresh := range []func(){func() { GetPricing() }, RefreshPricing} {
		refresh()
		var modelCount, vendorCount int64
		require.NoError(t, DB.Model(&Model{}).Count(&modelCount).Error)
		require.NoError(t, DB.Model(&Vendor{}).Count(&vendorCount).Error)
		assert.EqualValues(t, 1, modelCount)
		assert.Zero(t, vendorCount)
	}

	prices := make(map[string]Pricing)
	for _, row := range GetPricing() {
		prices[row.ModelName] = row
	}
	vendors := make(map[int]PricingVendor)
	for _, vendor := range GetVendors() {
		vendors[vendor.ID] = vendor
	}
	for _, name := range []string{"doubao-seedream-5-5-261028", "doubao-seedance-2-5-260628"} {
		assert.Equal(t, "字节跳动", vendors[prices[name].VendorID].Name)
		assert.Equal(t, "Doubao.Color", prices[name].Icon)
	}
	assert.Equal(t, []constant.EndpointType{constant.EndpointTypeImageGeneration}, prices["doubao-seedream-5-5-261028"].SupportedEndpointTypes)
	assert.Empty(t, prices["doubao-seedance-misconfigured"].SupportedEndpointTypes)
	assert.Equal(t, []constant.EndpointType{constant.EndpointTypeOpenAIVideo}, prices["doubao-seedance-2-5-260628"].SupportedEndpointTypes)
	assert.Equal(t, common.EndpointInfo{Path: "/v1/videos", Method: "POST"}, GetSupportedEndpointMap()["openai-video"])
}

func TestPricingEndpointInfoIsScopedToEachModel(t *testing.T) {
	resetPricingEndpointTestTables(t)
	insertPricingEndpointChannel(t, 1, constant.ChannelTypeOpenAI, dto.ChannelOtherSettings{})
	for _, name := range []string{"custom-chat", "ordinary-chat"} {
		insertPricingEndpointAbility(t, 1, name)
	}
	require.NoError(t, (&Model{
		ModelName: "custom-chat",
		Status:    1,
		Endpoints: `{"openai":"/v1/chat/custom","custom":{"path":"/v1/custom","method":"get"}}`,
	}).Insert())

	body, err := common.Marshal(GetPricing())
	require.NoError(t, err)
	var rows []struct {
		Name         string                         `json:"model_name"`
		EndpointInfo map[string]common.EndpointInfo `json:"endpoint_info"`
	}
	require.NoError(t, common.Unmarshal(body, &rows))
	require.Len(t, rows, 2)
	for _, row := range rows {
		switch row.Name {
		case "custom-chat":
			assert.Equal(t, map[string]common.EndpointInfo{
				"openai": {Path: "/v1/chat/custom", Method: "POST"},
				"custom": {Path: "/v1/custom", Method: "GET"},
			}, row.EndpointInfo)
		case "ordinary-chat":
			assert.Equal(t, map[string]common.EndpointInfo{
				"openai": {Path: "/v1/chat/completions", Method: "POST"},
			}, row.EndpointInfo)
		default:
			t.Fatalf("unexpected model %s", row.Name)
		}
	}
	assert.Equal(t, map[string]common.EndpointInfo{
		"openai": {Path: "/v1/chat/completions", Method: "POST"},
	}, GetSupportedEndpointMap())
}

func TestPricingFallbackLeavesUncoveredNonArkMetadataReadOnly(t *testing.T) {
	resetPricingEndpointTestTables(t)
	insertPricingEndpointChannel(t, 146, constant.ChannelTypeOpenAI, dto.ChannelOtherSettings{})
	insertPricingEndpointChannel(t, 147, constant.ChannelTypeVolcEngine, dto.ChannelOtherSettings{})
	require.NoError(t, DB.Model(&Channel{}).Where("id = ?", 147).Update("status", common.ChannelStatusManuallyDisabled).Error)
	insertPricingEndpointAbility(t, 146, "qwen-legacy-uncovered")
	insertPricingEndpointAbility(t, 146, "doubao-seedream-on-other-channel")
	insertPricingEndpointAbility(t, 147, "doubao-seedream-on-disabled-channel")

	byModel := pricingEndpointTypesByModel(t)
	require.Equal(t, []constant.EndpointType{constant.EndpointTypeOpenAI}, byModel["qwen-legacy-uncovered"])
	require.Contains(t, byModel, "doubao-seedream-on-other-channel")

	var models, vendors int64
	require.NoError(t, DB.Model(&Model{}).Count(&models).Error)
	require.NoError(t, DB.Model(&Vendor{}).Count(&vendors).Error)
	assert.Zero(t, models)
	assert.Zero(t, vendors)
}

func TestPricingFallbackKeepsLegacyVendorPresentation(t *testing.T) {
	resetPricingEndpointTestTables(t)
	insertPricingEndpointChannel(t, 148, constant.ChannelTypeOpenAI, dto.ChannelOtherSettings{})
	vendor := &Vendor{Name: "字节跳动", Icon: "Doubao.Color"}
	require.NoError(t, vendor.Insert())
	insertPricingEndpointAbility(t, 148, "doubao-legacy-uncovered")
	insertPricingEndpointAbility(t, 148, "seedream-legacy-uncovered")
	prices := make(map[string]Pricing)
	for _, item := range GetPricing() {
		prices[item.ModelName] = item
	}
	require.Contains(t, prices, "doubao-legacy-uncovered")
	require.Contains(t, prices, "seedream-legacy-uncovered")
	assert.Equal(t, vendor.Id, prices["doubao-legacy-uncovered"].VendorID)
	assert.Empty(t, prices["doubao-legacy-uncovered"].Icon)
	assert.Zero(t, prices["seedream-legacy-uncovered"].VendorID)

	var models, vendors int64
	require.NoError(t, DB.Model(&Model{}).Count(&models).Error)
	require.NoError(t, DB.Model(&Vendor{}).Count(&vendors).Error)
	assert.Zero(t, models)
	assert.EqualValues(t, 1, vendors)
}

func TestPricingReadLeavesInvalidAdvancedChannelSettingsUntouched(t *testing.T) {
	resetPricingEndpointTestTables(t)
	common.MemoryCacheEnabled = false
	insertPricingEndpointChannel(t, 58, constant.ChannelTypeAdvancedCustom, dto.ChannelOtherSettings{})
	insertPricingEndpointAbility(t, 58, "advanced-fallback")
	const invalid = "{bad json"
	require.NoError(t, DB.Model(&Channel{}).Where("id = ?", 58).Update("settings", invalid).Error)

	result := pricingEndpointTypesFromPricing(GetPricing())
	assert.Equal(t, []constant.EndpointType{constant.EndpointTypeOpenAI}, result["advanced-fallback"])
	channel, err := GetChannelById(58, true)
	require.NoError(t, err)
	assert.Equal(t, invalid, channel.OtherSettings)
}
