package model

import (
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/relay/capability"
	"github.com/QuantumNous/new-api/setting/billing_setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/QuantumNous/new-api/types"
)

type Pricing struct {
	ModelName              string                         `json:"model_name"`
	Description            string                         `json:"description,omitempty"`
	Icon                   string                         `json:"icon,omitempty"`
	Tags                   string                         `json:"tags,omitempty"`
	VendorID               int                            `json:"vendor_id,omitempty"`
	QuotaType              int                            `json:"quota_type"`
	ModelRatio             float64                        `json:"model_ratio"`
	ModelPrice             float64                        `json:"model_price"`
	OwnerBy                string                         `json:"owner_by"`
	CompletionRatio        float64                        `json:"completion_ratio"`
	CacheRatio             *float64                       `json:"cache_ratio,omitempty"`
	CreateCacheRatio       *float64                       `json:"create_cache_ratio,omitempty"`
	ImageRatio             *float64                       `json:"image_ratio,omitempty"`
	AudioRatio             *float64                       `json:"audio_ratio,omitempty"`
	AudioCompletionRatio   *float64                       `json:"audio_completion_ratio,omitempty"`
	EnableGroup            []string                       `json:"enable_groups"`
	SupportedEndpointTypes []constant.EndpointType        `json:"supported_endpoint_types"`
	EndpointInfo           map[string]common.EndpointInfo `json:"endpoint_info,omitempty"`
	BillingMode            string                         `json:"billing_mode,omitempty"`
	BillingExpr            string                         `json:"billing_expr,omitempty"`
	PricingVersion         string                         `json:"pricing_version,omitempty"`
}

type PricingVendor struct {
	ID          int    `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Icon        string `json:"icon,omitempty"`
}

var (
	pricingMap           []Pricing
	vendorsList          []PricingVendor
	supportedEndpointMap map[string]common.EndpointInfo
	lastGetPricingTime   time.Time
	updatePricingLock    sync.Mutex

	// 缓存映射：模型名 -> 启用分组 / 计费类型
	modelEnableGroups     = make(map[string][]string)
	modelQuotaTypeMap     = make(map[string]int)
	modelEnableGroupsLock = sync.RWMutex{}
)

var (
	modelSupportEndpointTypes = make(map[string][]constant.EndpointType)
	modelSupportEndpointsLock = sync.RWMutex{}
)

func GetPricing() []Pricing {
	pricing, _, _ := GetPricingCatalog()
	return pricing
}

// GetPricingCatalog returns a consistent snapshot of the public pricing data.
func GetPricingCatalog() ([]Pricing, []PricingVendor, map[string]common.EndpointInfo) {
	updatePricingLock.Lock()
	defer updatePricingLock.Unlock()
	if time.Since(lastGetPricingTime) > time.Minute {
		modelSupportEndpointsLock.Lock()
		defer modelSupportEndpointsLock.Unlock()
		updatePricing()
	}
	return pricingMap, vendorsList, supportedEndpointMap
}

func InvalidatePricingCache() {
	updatePricingLock.Lock()
	defer updatePricingLock.Unlock()

	pricingMap = nil
	vendorsList = nil
	supportedEndpointMap = nil
	lastGetPricingTime = time.Time{}
}

func GetVendors() []PricingVendor {
	_, vendors, _ := GetPricingCatalog()
	return vendors
}

func GetModelSupportEndpointTypes(model string) []constant.EndpointType {
	if model == "" {
		return make([]constant.EndpointType, 0)
	}
	GetPricing()
	modelSupportEndpointsLock.RLock()
	defer modelSupportEndpointsLock.RUnlock()
	if endpoints, ok := modelSupportEndpointTypes[model]; ok {
		return endpoints
	}
	return make([]constant.EndpointType, 0)
}

func getPricingEndpointTypesForAbility(ability AbilityWithChannel, advancedCustomConfigs map[int]*dto.AdvancedCustomConfig) []constant.EndpointType {
	if ability.ChannelType != constant.ChannelTypeAdvancedCustom {
		return common.GetEndpointTypesByChannelType(ability.ChannelType, ability.Model)
	}
	if config := advancedCustomConfigs[ability.ChannelId]; config != nil {
		return config.SupportedEndpointTypesForModel(ability.Model)
	}
	return common.GetEndpointTypesByChannelType(ability.ChannelType, ability.Model)
}

// loadPricingAdvancedCustomConfigs runs inside updatePricing while
// updatePricingLock is held, and nests channelSyncLock.RLock. This defines the
// global lock order updatePricingLock -> channelSyncLock: any code path holding
// channelSyncLock must release it before touching the pricing cache (see
// InitChannelCache / CacheUpdateChannel), otherwise it deadlocks.
// The returned configs are pointers shared with the channel cache; they are
// replaced wholesale on update and never mutated in place, so reading them after
// RUnlock is safe.
func loadPricingAdvancedCustomConfigs(enableAbilities []AbilityWithChannel) map[int]*dto.AdvancedCustomConfig {
	channelIDs := make([]int, 0)
	seen := make(map[int]struct{})
	for _, ability := range enableAbilities {
		if ability.ChannelType != constant.ChannelTypeAdvancedCustom {
			continue
		}
		if _, exists := seen[ability.ChannelId]; exists {
			continue
		}
		seen[ability.ChannelId] = struct{}{}
		channelIDs = append(channelIDs, ability.ChannelId)
	}
	if len(channelIDs) == 0 {
		return nil
	}

	configs := make(map[int]*dto.AdvancedCustomConfig, len(channelIDs))
	if common.MemoryCacheEnabled {
		channelSyncLock.RLock()
		defer channelSyncLock.RUnlock()
		for _, channelID := range channelIDs {
			if config := channel2advancedCustomConfig[channelID]; config != nil {
				configs[channelID] = config
			}
		}
		return configs
	}

	for _, channelID := range channelIDs {
		channel, err := CacheGetChannel(channelID)
		if err != nil {
			common.SysLog(fmt.Sprintf("load advanced custom channel settings error: channel_id=%d, error=%v", channelID, err))
			continue
		}
		if channel.Type != constant.ChannelTypeAdvancedCustom {
			continue
		}
		if config := channel.GetOtherSettings().AdvancedCustom; config != nil {
			configs[channelID] = config
		}
	}
	return configs
}

func updatePricing() {
	enableAbilities, err := GetAllEnableAbilityWithChannels()
	if err != nil {
		common.SysLog(fmt.Sprintf("load pricing abilities: %v", err))
		return
	}
	var allMeta []Model
	if err := DB.Order("id").Find(&allMeta).Error; err != nil {
		common.SysLog(fmt.Sprintf("load pricing model metadata: %v", err))
		return
	}
	var vendors []Vendor
	if err := DB.Find(&vendors).Error; err != nil {
		common.SysLog(fmt.Sprintf("load pricing vendors: %v", err))
		return
	}

	modelGroupsMap := make(map[string]*types.Set[string])
	for _, ability := range enableAbilities {
		groups := modelGroupsMap[ability.Model]
		if groups == nil {
			groups = types.NewSet[string]()
			modelGroupsMap[ability.Model] = groups
		}
		groups.Add(ability.Group)
	}
	modelNames := make([]string, 0, len(modelGroupsMap))
	for name := range modelGroupsMap {
		modelNames = append(modelNames, name)
	}
	sort.Strings(modelNames)

	metaMap := resolveModelMetadata(allMeta, modelNames)
	vendorMap := make(map[int]*Vendor, len(vendors))
	for i := range vendors {
		vendorMap[vendors[i].Id] = &vendors[i]
	}
	setPricingVendorFallbacks(metaMap, vendorMap, enableAbilities)

	vendorsList = make([]PricingVendor, 0, len(vendorMap))
	for _, v := range vendorMap {
		vendorsList = append(vendorsList, PricingVendor{
			ID: v.Id, Name: v.Name, Description: v.Description, Icon: v.Icon,
		})
	}
	sort.Slice(vendorsList, func(i, j int) bool { return vendorsList[i].ID < vendorsList[j].ID })

	modelSupportEndpointTypes = make(map[string][]constant.EndpointType)
	endpointInfoByModel := make(map[string]map[string]common.EndpointInfo)
	supportedEndpointMap = make(map[string]common.EndpointInfo)
	advancedCustomConfigs := loadPricingAdvancedCustomConfigs(enableAbilities)
	arkChatModels := make(map[string]bool)
	videoModels := make(map[string]bool)
	videoOperation := capability.OperationVideoCreate
	videoProtocol := capability.ProtocolForOperation(videoOperation)
	registry := capability.DefaultRegistry()

	// Keep adapter preference order and deduplicate across groups and channels.
	for _, ability := range enableAbilities {
		if ability.ChannelType == constant.ChannelTypeVolcEngine {
			arkChatModels[ability.Model] = true
		}
		if registry.Supports(ability.ChannelType, videoProtocol, videoOperation) {
			videoModels[ability.Model] = true
		}

		endpoints := modelSupportEndpointTypes[ability.Model]
		for _, endpoint := range getPricingEndpointTypesForAbility(ability, advancedCustomConfigs) {
			if !slices.Contains(endpoints, endpoint) {
				endpoints = append(endpoints, endpoint)
			}
		}
		modelSupportEndpointTypes[ability.Model] = endpoints
	}

	for _, name := range modelNames {
		endpoints := modelSupportEndpointTypes[name]
		info := make(map[string]common.EndpointInfo)
		for _, endpoint := range endpoints {
			if value, ok := common.GetDefaultEndpointInfo(endpoint); ok {
				info[string(endpoint)] = value
				supportedEndpointMap[string(endpoint)] = value
			}
		}
		if meta := metaMap[name]; meta != nil {
			overrides := meta.EndpointOverrides()
			keys := make([]string, 0, len(overrides))
			for key := range overrides {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			for _, key := range keys {
				endpoint := constant.EndpointType(key)
				// Type 45 cannot supply video; an override can customize a
				// real video route for the same model through another adapter.
				if endpoint == constant.EndpointTypeOpenAIVideo && arkChatModels[name] && !videoModels[name] {
					continue
				}
				if !slices.Contains(endpoints, endpoint) {
					endpoints = append(endpoints, endpoint)
				}
				info[key] = overrides[key]
			}
		}
		modelSupportEndpointTypes[name] = endpoints
		endpointInfoByModel[name] = info
	}

	pricingMap = make([]Pricing, 0)
	for _, model := range modelNames {
		groups := modelGroupsMap[model].Items()
		sort.Strings(groups)
		pricing := Pricing{
			ModelName:              model,
			EnableGroup:            groups,
			EndpointInfo:           endpointInfoByModel[model],
			SupportedEndpointTypes: modelSupportEndpointTypes[model],
		}

		// 补充模型元数据（描述、标签、供应商、状态）
		if meta, ok := metaMap[model]; ok {
			// 若模型被禁用(status!=1)，则直接跳过，不返回给前端
			if meta.Status != 1 {
				continue
			}
			pricing.Description = meta.Description
			pricing.Icon = meta.Icon
			pricing.Tags = meta.Tags
			pricing.VendorID = meta.VendorID
		}
		modelPrice, findPrice := ratio_setting.GetModelPrice(model, false)
		if findPrice {
			pricing.ModelPrice = modelPrice
			pricing.QuotaType = 1
		} else {
			modelRatio, _, _ := ratio_setting.GetModelRatio(model)
			pricing.ModelRatio = modelRatio
			pricing.CompletionRatio = ratio_setting.GetCompletionRatio(model)
			pricing.QuotaType = 0
		}
		if cacheRatio, ok := ratio_setting.GetCacheRatio(model); ok {
			pricing.CacheRatio = &cacheRatio
		}
		if createCacheRatio, ok := ratio_setting.GetCreateCacheRatio(model); ok {
			pricing.CreateCacheRatio = &createCacheRatio
		}
		if imageRatio, ok := ratio_setting.GetImageRatio(model); ok {
			pricing.ImageRatio = &imageRatio
		}
		if ratio_setting.ContainsAudioRatio(model) {
			audioRatio := ratio_setting.GetAudioRatio(model)
			pricing.AudioRatio = &audioRatio
		}
		if ratio_setting.ContainsAudioCompletionRatio(model) {
			audioCompletionRatio := ratio_setting.GetAudioCompletionRatio(model)
			pricing.AudioCompletionRatio = &audioCompletionRatio
		}
		if billingMode := billing_setting.GetBillingMode(model); billingMode == "tiered_expr" {
			if expr, ok := billing_setting.GetBillingExpr(model); ok && strings.TrimSpace(expr) != "" {
				pricing.BillingMode = billingMode
				pricing.BillingExpr = expr
			}
		}
		pricingMap = append(pricingMap, pricing)
	}

	// 防止大更新后数据不通用
	if len(pricingMap) > 0 {
		pricingMap[0].PricingVersion = "5a90f2b86c08bd983a9a2e6d66c255f4eaef9c4bc934386d2b6ae84ef0ff1f1f"
	}

	// 刷新缓存映射，供高并发快速查询
	modelEnableGroupsLock.Lock()
	modelEnableGroups = make(map[string][]string)
	modelQuotaTypeMap = make(map[string]int)
	for _, p := range pricingMap {
		modelEnableGroups[p.ModelName] = p.EnableGroup
		modelQuotaTypeMap[p.ModelName] = p.QuotaType
	}
	modelEnableGroupsLock.Unlock()

	lastGetPricingTime = time.Now()
}

// GetSupportedEndpointMap 返回全局端点到路径的映射
func GetSupportedEndpointMap() map[string]common.EndpointInfo {
	_, _, endpointMap := GetPricingCatalog()
	return endpointMap
}
