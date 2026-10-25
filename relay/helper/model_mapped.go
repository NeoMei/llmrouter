package helper

import (
	"fmt"
	"strings"

	basecommon "github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/gin-gonic/gin"
)

func ModelMappedHelper(c *gin.Context, info *common.RelayInfo, request dto.Request) error {
	if info.ChannelMeta == nil {
		info.ChannelMeta = &common.ChannelMeta{}
	}

	// The catalog has already applied its channel mapping and validated this
	// exact provider model. Mapping it again would change the authorized target.
	if len(info.RouteCandidates) > 0 && info.ResolvedModelName != "" {
		info.UpstreamModelName = info.ResolvedModelName
		info.IsModelMapped = info.UpstreamModelName != info.OriginModelName
		if request != nil {
			request.SetModelName(info.UpstreamModelName)
		}
		return nil
	}

	isResponsesCompact := info.RelayMode == relayconstant.RelayModeResponsesCompact
	originModelName := info.OriginModelName
	mappingModelName := originModelName
	if isResponsesCompact && strings.HasSuffix(originModelName, ratio_setting.CompactModelSuffix) {
		mappingModelName = strings.TrimSuffix(originModelName, ratio_setting.CompactModelSuffix)
	}

	// map model name
	modelMapping := c.GetString("model_mapping")
	if modelMapping != "" && modelMapping != "{}" {
		modelMap := make(map[string]string)
		err := basecommon.UnmarshalJsonStr(modelMapping, &modelMap)
		if err != nil {
			return fmt.Errorf("unmarshal_model_mapping_failed")
		}

		currentModel, err := basecommon.ResolveModelMapping(mappingModelName, modelMap)
		if err != nil {
			return err
		}
		// Keep the legacy direct self-map short circuit, including compact mode.
		if modelMap[mappingModelName] == mappingModelName && mappingModelName == info.OriginModelName {
			info.IsModelMapped = false
			return nil
		}
		info.IsModelMapped = currentModel != mappingModelName || (modelMap[currentModel] == currentModel && currentModel != info.OriginModelName)
		if info.IsModelMapped {
			info.UpstreamModelName = currentModel
		}
	}

	if isResponsesCompact {
		finalUpstreamModelName := mappingModelName
		if info.IsModelMapped && info.UpstreamModelName != "" {
			finalUpstreamModelName = info.UpstreamModelName
		}
		info.UpstreamModelName = finalUpstreamModelName
		info.OriginModelName = ratio_setting.WithCompactModelSuffix(finalUpstreamModelName)
	}
	if request != nil {
		request.SetModelName(info.UpstreamModelName)
	}
	return nil
}
