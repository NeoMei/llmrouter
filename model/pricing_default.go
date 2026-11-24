package model

import (
	"sort"
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

// Ark channels can serve models from several suppliers. Type 54 is the
// dedicated Doubao video adapter, and Seedream/Seedance are ByteDance families.
func pricingVendorName(modelName string, arkChannelType int) string {
	if arkChannelType == constant.ChannelTypeDoubaoVideo ||
		(arkChannelType == constant.ChannelTypeVolcEngine && (common.IsSeedreamModel(modelName) || common.IsSeedanceModel(modelName))) {
		return "字节跳动"
	}

	modelLower := strings.ToLower(modelName)
	var bestMatch string
	for pattern := range defaultVendorRules {
		if strings.Contains(modelLower, pattern) && (len(pattern) > len(bestMatch) ||
			(len(pattern) == len(bestMatch) && pattern < bestMatch)) {
			bestMatch = pattern
		}
	}
	return defaultVendorRules[bestMatch]
}

func isArkChannelType(channelType int) bool {
	return channelType == constant.ChannelTypeVolcEngine || channelType == constant.ChannelTypeDoubaoVideo
}

// Temporary supplier IDs are negative and derived from the static name list.
// They are used only in the pricing response and never in persisted metadata.
var pricingFallbackVendorIDs = func() map[string]int {
	names := make([]string, 0, len(defaultVendorIcons))
	for name := range defaultVendorIcons {
		names = append(names, name)
	}
	sort.Strings(names)
	ids := make(map[string]int, len(names))
	for i, name := range names {
		ids[name] = -1 - i
	}
	return ids
}()

// setPricingVendorFallbacks populates presentation defaults without writing
// either table. Non-Ark ability-only models retain their legacy presentation.
func setPricingVendorFallbacks(metaMap map[string]*Model, vendorMap map[int]*Vendor, abilities []AbilityWithChannel) {
	vendorsByName := make(map[string]*Vendor, len(vendorMap))
	for _, vendor := range vendorMap {
		if current := vendorsByName[vendor.Name]; current == nil || vendor.Id < current.Id {
			vendorsByName[vendor.Name] = vendor
		}
	}
	arkTypes := make(map[string]int)
	for _, ability := range abilities {
		if isArkChannelType(ability.ChannelType) && arkTypes[ability.Model] != constant.ChannelTypeDoubaoVideo {
			arkTypes[ability.Model] = ability.ChannelType
		}
	}
	for _, ability := range abilities {
		if metaMap[ability.Model] != nil {
			continue
		}
		channelType := arkTypes[ability.Model]
		vendorName := pricingVendorName(ability.Model, channelType)
		vendor := vendorsByName[vendorName]
		if vendor == nil && isArkChannelType(channelType) && vendorName != "" {
			vendor = &Vendor{Id: pricingFallbackVendorIDs[vendorName], Name: vendorName, Icon: defaultVendorIcons[vendorName]}
			vendorsByName[vendorName] = vendor
			vendorMap[vendor.Id] = vendor
		}

		meta := &Model{ModelName: ability.Model, Status: 1}
		if vendor != nil && (vendor.Id > 0 || isArkChannelType(channelType)) {
			meta.VendorID = vendor.Id
			if isArkChannelType(channelType) {
				meta.Icon = vendor.Icon
				if meta.Icon == "" {
					meta.Icon = defaultVendorIcons[vendorName]
				}
			}
		}
		metaMap[ability.Model] = meta
	}
}

// provisionArkModelMetadata runs only from explicit channel configuration
// writes. It respects curated rows (including disabled rows and name rules).
func (channel *Channel) provisionArkModelMetadata(tx *gorm.DB) error {
	if !isArkChannelType(channel.Type) || channel.Status != common.ChannelStatusEnabled {
		return nil
	}

	names := normalizeLookupValues(channel.GetModels())
	if len(names) == 0 {
		return nil
	}
	var existing []Model
	if err := tx.Order("id").Find(&existing).Error; err != nil {
		return err
	}
	metadata := resolveModelMetadata(existing, names)
	for _, name := range names {
		if metadata[name] != nil || (channel.Type == constant.ChannelTypeVolcEngine && common.IsSeedanceModel(name)) {
			continue
		}

		item := &Model{ModelName: name, Status: 1}
		vendorName := pricingVendorName(name, channel.Type)
		if vendorName != "" {
			vendor := Vendor{Name: vendorName, Status: 1, Icon: defaultVendorIcons[vendorName]}
			if _, err := vendor.FindOrInsertWithDB(tx); err != nil {
				return err
			}
			item.VendorID = vendor.Id
			item.Icon = vendor.Icon
			if item.Icon == "" {
				item.Icon = defaultVendorIcons[vendorName]
			}
		}
		if err := item.InsertWithDB(tx); err != nil {
			return err
		}
	}
	return nil
}
