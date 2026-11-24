package model

import (
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"gorm.io/gorm"
)

// 简化的供应商映射规则
var defaultVendorRules = map[string]string{
	"gpt":      "OpenAI",
	"dall-e":   "OpenAI",
	"whisper":  "OpenAI",
	"o1":       "OpenAI",
	"o3":       "OpenAI",
	"claude":   "Anthropic",
	"gemini":   "Google",
	"moonshot": "Moonshot",
	"kimi":     "Moonshot",
	"chatglm":  "智谱",
	"glm-":     "智谱",
	"qwen":     "阿里巴巴",
	"deepseek": "DeepSeek",
	"abab":     "MiniMax",
	"minimax":  "MiniMax",
	"ernie":    "百度",
	"spark":    "讯飞",
	"hunyuan":  "腾讯",
	"command":  "Cohere",
	"@cf/":     "Cloudflare",
	"360":      "360",
	"yi":       "零一万物",
	"jina":     "Jina",
	"mistral":  "Mistral",
	"grok":     "xAI",
	"llama":    "Meta",
	"doubao":   "字节跳动",
	"kling":    "快手",
	"jimeng":   "即梦",
	"vidu":     "Vidu",
}

// 供应商默认图标映射
var defaultVendorIcons = map[string]string{
	"OpenAI":     "OpenAI",
	"Anthropic":  "Claude.Color",
	"Google":     "Gemini.Color",
	"Moonshot":   "Moonshot",
	"智谱":         "Zhipu.Color",
	"阿里巴巴":       "Qwen.Color",
	"DeepSeek":   "DeepSeek.Color",
	"MiniMax":    "Minimax.Color",
	"百度":         "Wenxin.Color",
	"讯飞":         "Spark.Color",
	"腾讯":         "Hunyuan.Color",
	"Cohere":     "Cohere.Color",
	"Cloudflare": "Cloudflare.Color",
	"360":        "Ai360.Color",
	"零一万物":       "Yi.Color",
	"Jina":       "Jina",
	"Mistral":    "Mistral.Color",
	"xAI":        "XAI",
	"Meta":       "Ollama",
	"字节跳动":       "Doubao.Color",
	"快手":         "Kling.Color",
	"即梦":         "Jimeng.Color",
	"Vidu":       "Vidu",
	"微软":         "AzureAI",
	"Microsoft":  "AzureAI",
	"Azure":      "AzureAI",
}

// initDefaultVendorMapping 简化的默认供应商映射
func initDefaultVendorMapping(metaMap map[string]*Model, vendorMap map[int]*Vendor, enableAbilities []AbilityWithChannel) {
	arkModels := make(map[string]bool)
	for _, ability := range enableAbilities {
		if ability.Enabled && ability.ChannelType == constant.ChannelTypeVolcEngine && ability.ChannelStatus == common.ChannelStatusEnabled {
			arkModels[ability.Model] = true
		}
	}
	for _, ability := range enableAbilities {
		modelName := ability.Model
		if _, exists := metaMap[modelName]; exists {
			continue
		}

		persist := arkModels[modelName]
		vendorID := 0
		icon := ""
		if persist && (common.IsSeedreamModel(modelName) || common.IsSeedanceModel(modelName)) {
			vendorID = pricingVendorID("字节跳动", vendorMap, true)
		} else {
			modelLower := strings.ToLower(modelName)
			for pattern, vendorName := range defaultVendorRules {
				if strings.Contains(modelLower, pattern) {
					vendorID = pricingVendorID(vendorName, vendorMap, persist)
					break
				}
			}
		}
		if persist && vendorID != 0 {
			icon = vendorMap[vendorID].Icon
		}

		meta := &Model{
			ModelName: modelName,
			VendorID:  vendorID,
			Status:    1,
			NameRule:  NameRuleExact,
			Icon:      icon,
		}
		// Pricing and catalog reads can synthesize legacy metadata in memory.
		// Only active Ark abilities opt into persistent discovery.
		if !persist {
			metaMap[modelName] = meta
			continue
		}
		var existing Model
		if err := DB.Where("model_name = ?", modelName).First(&existing).Error; err == nil {
			metaMap[modelName] = &existing
			continue
		} else if err != gorm.ErrRecordNotFound {
			common.SysLog("lookup model metadata failed: " + err.Error())
			continue
		}
		if err := meta.Insert(); err != nil {
			common.SysLog("create model metadata failed: " + err.Error())
			continue
		}
		metaMap[modelName] = meta
	}
}

func pricingVendorID(vendorName string, vendorMap map[int]*Vendor, persist bool) int {
	// 查找现有供应商
	for id, vendor := range vendorMap {
		if vendor.Name == vendorName {
			return id
		}
	}
	if !persist {
		return 0
	}

	// 创建新供应商
	newVendor := &Vendor{
		Name:   vendorName,
		Status: 1,
		Icon:   getDefaultVendorIcon(vendorName),
	}

	if err := newVendor.Insert(); err != nil {
		return 0
	}

	vendorMap[newVendor.Id] = newVendor
	return newVendor.Id
}

// 获取供应商默认图标
func getDefaultVendorIcon(vendorName string) string {
	if icon, exists := defaultVendorIcons[vendorName]; exists {
		return icon
	}
	return ""
}
