package router

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"reflect"
	"slices"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"

	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/internal/platformbilling"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service/authz"
	"github.com/QuantumNous/new-api/service/modelcatalog"
	"github.com/QuantumNous/new-api/setting/model_setting"
	"github.com/gin-gonic/gin"
)

type deploymentCatalogAuthorizer struct {
	userID        int
	group, client string
}

func (a deploymentCatalogAuthorizer) Authorize(_ context.Context, userID int, groups []string, client string) (modelcatalog.AuthorizedRuntimeSnapshot, error) {
	if userID != a.userID || !slices.Equal(groups, []string{a.group}) || client != a.client {
		return modelcatalog.AuthorizedRuntimeSnapshot{}, errors.New("catalog scope mismatch")
	}
	return modelcatalog.NewAuthorizedRuntimeSnapshot(userID, groups, client), nil
}
func readTechnicalCatalog(ctx context.Context, adapter *platformbilling.Adapter, product string) (platformbilling.TechnicalCatalog, error) {
	if !adapter.Enabled() || model.DB == nil {
		return platformbilling.TechnicalCatalog{}, errors.New("catalog unavailable")
	}
	var routes []model.ClientModelRoute
	var targets []model.ClientModelTarget
	if err := model.DB.WithContext(ctx).Where("enabled = ?", true).Order("id").Find(&routes).Error; err != nil {
		return platformbilling.TechnicalCatalog{}, err
	}
	if err := model.DB.WithContext(ctx).Where("enabled = ?", true).Order("id").Find(&targets).Error; err != nil {
		return platformbilling.TechnicalCatalog{}, err
	}
	user, group := adapter.Principal()
	runtime := modelcatalog.RuntimeContext{UserID: user, Groups: []string{group}, Client: product}
	catalog, err := modelcatalog.AssembleManagedRuntimeCatalog(ctx, runtime, routes, targets, modelcatalog.NewGormRuntimeSources(model.DB), deploymentCatalogAuthorizer{user, group, product})
	if err != nil {
		return platformbilling.TechnicalCatalog{}, err
	}
	models, err := catalog.List(ctx, user, runtime.Groups, product)
	if err != nil {
		return platformbilling.TechnicalCatalog{}, err
	}
	sources := make([]platformbilling.CatalogSourceModel, 0, len(models))
	for _, m := range models {
		blocked := map[string]string{}
		channelTypes := []int{}
		contextWindow := m.ContextWindow
		presetTargets := map[string]bool{}
		for _, operation := range m.Operations {
			presetTargets[operation] = true
		}
		candidates, e := catalog.ResolveCandidates(ctx, user, runtime.Groups, m.PublicID, product)
		if e != nil {
			return platformbilling.TechnicalCatalog{}, e
		}
		for _, candidate := range candidates {
			if candidate.ContextWindow > 0 && (contextWindow == 0 || candidate.ContextWindow < contextWindow) {
				contextWindow = candidate.ContextWindow
			}
			var channel model.Channel
			if e = model.DB.WithContext(ctx).Select("id", "type", "key", "channel_info", "base_url", "setting", "settings", "param_override", "header_override").First(&channel, candidate.ChannelID).Error; e != nil {
				return platformbilling.TechnicalCatalog{}, e
			}
			channelTypes = append(channelTypes, channel.Type)
			var settings dto.ChannelSettings
			if channel.Setting != nil && *channel.Setting != "" {
				if e = common.UnmarshalJsonStr(*channel.Setting, &settings); e != nil {
					return platformbilling.TechnicalCatalog{}, e
				}
			}
			changed := settings.SystemPrompt != "" || settings.PassThroughBodyEnabled || model_setting.GetGlobalSettings().PassThroughRequestEnabled
			for _, override := range []*string{channel.ParamOverride, channel.HeaderOverride} {
				if override != nil && *override != "" {
					var fields map[string]any
					if e = common.UnmarshalJsonStr(*override, &fields); e != nil {
						return platformbilling.TechnicalCatalog{}, e
					}
					changed = changed || len(fields) > 0
				}
			}
			for _, operation := range m.Operations {
				knowledgeOrigin := channel.GetBaseURL()
				if knowledgeOrigin == "" {
					knowledgeOrigin = constant.ChannelBaseURLs[channel.Type]
				}
				presetTargets[operation] = presetTargets[operation] && !channel.ChannelInfo.IsMultiKey && adapter.NativePresetTarget(m.PublicID, operation, candidate.ChannelID, channel.Type, candidate.UpstreamModel, channel.Key, channel.GetBaseURL())
				if (operation == "ocr.jobs.create" || operation == "dashscope.ocr.recognitions.create") && (channel.ChannelInfo.IsMultiKey || !adapter.OCRTarget(m.PublicID, operation, candidate.ChannelID, channel.Type, candidate.UpstreamModel, channel.Key, channel.GetBaseURL())) {
					blocked[operation] = "certified_ocr_target_unavailable"
				}
				if platformbilling.IsImageWire(operation) && (channel.ChannelInfo.IsMultiKey || !adapter.ImageTarget(m.PublicID, operation, candidate.ChannelID, channel.Type, candidate.UpstreamModel, channel.Key, channel.GetBaseURL())) {
					blocked[operation] = "certified_image_target_unavailable"
				}
				if platformbilling.IsAudioWire(operation) && (channel.ChannelInfo.IsMultiKey || !adapter.AudioTarget(m.PublicID, operation, candidate.ChannelID, channel.Type, candidate.UpstreamModel, channel.Key, channel.GetBaseURL())) {
					blocked[operation] = "certified_audio_target_unavailable"
				}
				if (operation == "embeddings.create" || operation == "rerank.create" || operation == "multimodal.embeddings.create") && (channel.ChannelInfo.IsMultiKey || !adapter.KnowledgeTarget(m.PublicID, operation, candidate.ChannelID, channel.Type, candidate.UpstreamModel, channel.Key, knowledgeOrigin)) {
					blocked[operation] = "certified_knowledge_target_unavailable"
				}

				if operation == "responses.compact" && channel.BaseURL != nil && *channel.BaseURL != "" {
					target, e := url.Parse(*channel.BaseURL + "/v1/responses/compact")
					if e != nil || target.Path != "/v1/responses/compact" || target.RawQuery != "" || target.Fragment != "" || target.User != nil {
						blocked[operation] = "certified_compact_endpoint_unavailable"
					}
				}
				if operation == "responses.compact" && !adapter.CompactTarget(m.PublicID, candidate.ChannelID, channel.Type, candidate.UpstreamModel, candidate.ContextWindow) {
					blocked[operation] = "certified_compact_target_unavailable"
				}
				if operation == "embeddings.create" || operation == "multimodal.embeddings.create" {
					if !reflect.DeepEqual(adapter.KnowledgeSpace(m.PublicID, operation), m.EmbeddingSpace) {
						blocked[operation] = "certified_embedding_space_mismatch"
					}
				}
				if (operation == "rerank.create" || operation == "multimodal.embeddings.create") && channel.BaseURL != nil && *channel.BaseURL != "" {
					path := "/api/v1/services/rerank/text-rerank/text-rerank"
					if operation == "multimodal.embeddings.create" {
						path = "/api/v1/services/embeddings/multimodal-embedding/multimodal-embedding"
					}
					endpoint, e := url.Parse(*channel.BaseURL + path)
					if e != nil || endpoint.Path != path || endpoint.RawQuery != "" || endpoint.Fragment != "" || endpoint.User != nil {
						blocked[operation] = "certified_knowledge_endpoint_unavailable"
					}
				}
				if operation == "claude.messages.create" && (len(model_setting.GetClaudeSettings().HeadersSettings[m.PublicID]) > 0 || channel.GetOtherSettings().ClaudeBetaQuery) {
					blocked[operation] = "validated_request_mutation_configured"
				}
				if format, proven := platformbilling.TokenProducer(operation, channel.Type); format != "" && !proven {
					blocked[operation] = "managed_producer_unavailable"
				}
				if format, _ := platformbilling.TokenProducer(operation, channel.Type); format != "" && operation != "embeddings.create" && operation != "rerank.create" && operation != "multimodal.embeddings.create" {
					for _, modality := range m.InputModalities {
						if modality != "text" && ((modality != "image" && modality != "document") || !adapter.NativeInputTarget(operation, m.PublicID, channel.Type, candidate.UpstreamModel, modality)) {
							blocked[operation] = "certified_media_reservation_unavailable"
						}
					}
				}
				if changed {
					blocked[operation] = "validated_request_mutation_configured"
				}
			}
		}
		sources = append(sources, platformbilling.CatalogSourceModel{NativePresetTargets: presetTargets, SelectableChannelTypes: channelTypes, OperationDisabledReasons: blocked, ModelCapabilities: m.ModelCapabilities, PublicID: m.PublicID, DisplayName: m.DisplayName, ContextWindow: contextWindow, SupportsTools: m.SupportsTools, SupportsReasoningSummary: m.SupportsReasoningSummary, ReasoningEfforts: m.ReasoningEfforts})
	}
	return adapter.ProjectCatalog(product, sources)
}

type chatCatalogModel struct {
	ID      string            `json:"id"`
	Policy  string            `json:"policy"`
	RateIDs map[string]string `json:"rate_ids"`
}

func projectChatCatalog(catalog platformbilling.TechnicalCatalog, mode string, pins map[string]map[string]string) []chatCatalogModel {
	result := []chatCatalogModel{}
	for _, model := range catalog.Models {
		lowered := strings.ToLower(model.ID)
		if mode == "legacy" && (strings.Contains(lowered, "search") || strings.Contains(lowered, "audio")) {
			continue
		}
		for _, op := range model.Operations {
			legacyAvailable := mode == "legacy" && (op.DisabledReason == "metered_admission_unconfigured" || op.DisabledReason == "usage_policy_unconfigured")
			if op.ID == "chat.completions.create" && (op.Available || legacyAvailable) {
				rates := map[string]string{}
				for asset, models := range pins {
					if id := models[model.ID]; id != "" {
						rates[asset] = id
					}
				}
				result = append(result, chatCatalogModel{ID: model.ID, Policy: op.Policy, RateIDs: rates})
			}
		}
	}
	return result
}

func SetModelCatalogRouter(engine *gin.Engine, adapter *platformbilling.Adapter, config platformbilling.CatalogConfig) {
	middleware.RegisterUniHubManagementRoute(http.MethodGet, "/api/unihub/chat-models", authz.Permission{Resource: "product", Action: "use"})
	engine.GET("/api/unihub/chat-models", middleware.UserAuth(), func(c *gin.Context) {
		c.Header("Cache-Control", "no-store")
		principal, ok := authz.ProductPrincipal(c)
		if !ok || !principal.HasPermission("product:use") || principal.Access.ProductID == "" || principal.Access.InstanceID == "" {
			c.Status(http.StatusForbidden)
			return
		}
		catalog, err := readTechnicalCatalog(c.Request.Context(), adapter, principal.Access.ProductID)
		if err != nil || catalog.ProductID != principal.Access.ProductID || catalog.InstanceID != principal.Access.InstanceID {
			c.Status(http.StatusServiceUnavailable)
			return
		}
		mode, pins := adapter.ChatAdmissionPolicy()
		c.JSON(http.StatusOK, gin.H{"product_id": catalog.ProductID, "instance_id": catalog.InstanceID, "admission_mode": mode, "models": projectChatCatalog(catalog, mode, pins)})
	})

	engine.GET("/internal/v1/model-catalog", gin.WrapH(platformbilling.CatalogHandler(config, func(ctx context.Context) (platformbilling.TechnicalCatalog, error) {
		return readTechnicalCatalog(ctx, adapter, config.ProductID)
	})))
}
