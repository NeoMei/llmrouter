package controller

import (
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relay/capability"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/service/modelcatalog"
	"github.com/QuantumNous/new-api/setting/billing_setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type clientModelRoutePayload struct {
	ID          int                       `json:"id"`
	PublicID    string                    `json:"public_id"`
	DisplayName string                    `json:"display_name"`
	Client      string                    `json:"client"`
	Aliases     []string                  `json:"aliases"`
	Metadata    string                    `json:"metadata"`
	Enabled     bool                      `json:"enabled"`
	Targets     []model.ClientModelTarget `json:"targets"`
}

type clientModelRouteResponse struct {
	KnowledgeGeneration string                    `json:"knowledge_generation,omitempty"`
	ID                  int                       `json:"id"`
	PublicID            string                    `json:"public_id"`
	DisplayName         string                    `json:"display_name"`
	Client              string                    `json:"client"`
	Aliases             []string                  `json:"aliases"`
	Metadata            string                    `json:"metadata,omitempty"`
	Enabled             bool                      `json:"enabled"`
	CreatedTime         int64                     `json:"created_time"`
	UpdatedTime         int64                     `json:"updated_time"`
	Targets             []model.ClientModelTarget `json:"targets"`
}

type clientModelRouteChannelOption struct {
	ID     int      `json:"id"`
	Name   string   `json:"name"`
	Type   int      `json:"type"`
	Models []string `json:"models"`
	Wires  []string `json:"wires"`
}

type clientModelRouteOptionsResponse struct {
	Channels      []clientModelRouteChannelOption `json:"channels"`
	BillingModels []string                        `json:"billing_models"`
}

func ListClientModelRoutes(c *gin.Context) {
	var routes []model.ClientModelRoute
	if err := model.DB.Order("id ASC").Find(&routes).Error; err != nil {
		common.ApiError(c, err)
		return
	}
	var targets []model.ClientModelTarget
	if err := model.DB.Order("priority DESC").Order("id ASC").Find(&targets).Error; err != nil {
		common.ApiError(c, err)
		return
	}
	byRoute := make(map[int][]model.ClientModelTarget, len(routes))
	for _, target := range targets {
		byRoute[target.RouteId] = append(byRoute[target.RouteId], target)
	}
	result := make([]clientModelRouteResponse, 0, len(routes))
	for _, route := range routes {
		aliases := decodeRouteAliases(route.Aliases)
		result = append(result, clientModelRouteResponse{
			ID: route.Id, PublicID: route.PublicId, DisplayName: route.DisplayName, KnowledgeGeneration: route.KnowledgeGeneration,
			Client: route.Client, Aliases: aliases, Metadata: route.Metadata,
			Enabled: route.Enabled, CreatedTime: route.CreatedTime, UpdatedTime: route.UpdatedTime,
			Targets: byRoute[route.Id],
		})
	}
	common.ApiSuccess(c, result)
}

// ListClientModelRouteOptions returns only the non-sensitive channel fields
// needed by the route editor. Credentials, URLs, headers and overrides are
// deliberately excluded from this response.
func ListClientModelRouteOptions(c *gin.Context) {
	var channels []model.Channel
	if err := model.DB.Select("id", "name", "type", "models").Where("status = ?", common.ChannelStatusEnabled).Order("id ASC").Find(&channels).Error; err != nil {
		common.ApiError(c, err)
		return
	}
	channelOptions := make([]clientModelRouteChannelOption, 0, len(channels))
	registry := capability.DefaultRegistry()
	for _, channel := range channels {
		models := normalizeClientModelOptionNames(channel.GetModels())
		wires := make([]string, 0, 4)
		if registry.Supports(channel.Type, capability.ProtocolOpenAI, capability.OperationMultimodalEmbedding) {
			wires = append(wires, string(modelcatalog.WireMultimodalEmbedding))
		}
		if registry.Supports(channel.Type, capability.ProtocolPaddleOCR, capability.OperationOCRCreate) {
			wires = append(wires, string(modelcatalog.WirePaddleOCR))
		}
		if registry.Supports(channel.Type, capability.ProtocolOpenAI, capability.OperationResponseCreate) {
			wires = append(wires, string(modelcatalog.WireResponses))
		}
		if registry.Supports(channel.Type, capability.ProtocolOpenAI, capability.OperationChatCreate) {
			wires = append(wires, string(modelcatalog.WireOpenAIChat))
		}
		if registry.Supports(channel.Type, capability.ProtocolClaude, capability.OperationClaudeMessageCreate) {
			wires = append(wires, string(modelcatalog.WireAnthropic))
		}
		channelOptions = append(channelOptions, clientModelRouteChannelOption{ID: channel.Id, Name: channel.Name, Type: channel.Type, Models: models, Wires: wires})
	}
	billingCandidates := make(map[string]struct{})
	for name := range ratio_setting.GetModelPriceCopy() {
		billingCandidates[name] = struct{}{}
	}
	for name := range ratio_setting.GetModelRatioCopy() {
		billingCandidates[name] = struct{}{}
	}
	for name := range billing_setting.GetBillingExprCopy() {
		billingCandidates[name] = struct{}{}
	}
	billingModels := make([]string, 0, len(billingCandidates))
	for name := range billingCandidates {
		if helper.HasModelBillingConfig(name) {
			billingModels = append(billingModels, name)
		}
	}
	common.ApiSuccess(c, clientModelRouteOptionsResponse{Channels: channelOptions, BillingModels: normalizeClientModelOptionNames(billingModels)})
}

func CreateClientModelRoute(c *gin.Context) {
	var payload clientModelRoutePayload
	if err := c.ShouldBindJSON(&payload); err != nil {
		clientModelRouteError(c, http.StatusBadRequest, err)
		return
	}
	route := payload.routeRecord()
	err := model.DB.Transaction(func(tx *gorm.DB) error {
		if err := validateClientModelRoutePayload(tx, payload); err != nil {
			return err
		}
		if err := ensureRouteNameAndAliasesAvailable(tx, 0, payload); err != nil {
			return err
		}
		if err := tx.Create(&route).Error; err != nil {
			return err
		}
		for i := range payload.Targets {
			target := payload.Targets[i]
			target.Id = 0
			target.RouteId = route.Id
			if err := tx.Create(&target).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		clientModelRouteError(c, routeValidationStatus(err), err)
		return
	}
	response, err := loadClientModelRouteResponse(model.DB, route.Id)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, response)
}

func UpdateClientModelRoute(c *gin.Context) {
	var payload clientModelRoutePayload
	if err := c.ShouldBindJSON(&payload); err != nil {
		clientModelRouteError(c, http.StatusBadRequest, err)
		return
	}
	if payload.ID <= 0 {
		clientModelRouteError(c, http.StatusBadRequest, errors.New("缺少模型路由 ID"))
		return
	}
	route := payload.routeRecord()
	err := model.DB.Transaction(func(tx *gorm.DB) error {
		var existing model.ClientModelRoute
		if err := tx.First(&existing, payload.ID).Error; err != nil {
			return err
		}
		if err := validateClientModelRoutePayload(tx, payload); err != nil {
			return err
		}
		previous, err := modelcatalog.NormalizeModelCapabilities(existing.Metadata)
		if err != nil {
			return err
		}
		updated, err := modelcatalog.NormalizeModelCapabilities(route.Metadata)
		if err != nil {
			return err
		}
		if previous.EmbeddingSpace != nil && (updated.EmbeddingSpace == nil || *previous.EmbeddingSpace != *updated.EmbeddingSpace) {
			oldNames := append(decodeRouteAliases(existing.Aliases), existing.PublicId)
			newNames := append(append([]string(nil), payload.Aliases...), route.PublicId)
			for _, oldName := range oldNames {
				for _, newName := range newNames {
					if strings.EqualFold(strings.TrimSpace(oldName), strings.TrimSpace(newName)) {
						return fmt.Errorf("fixed embedding_space cannot change under an existing public model id or alias; use disjoint new names")
					}
				}
			}
		}

		if err := ensureRouteNameAndAliasesAvailable(tx, payload.ID, payload); err != nil {
			return err
		}
		route.CreatedTime = existing.CreatedTime
		route.UpdatedTime = common.GetTimestamp()
		if err := tx.Model(&model.ClientModelRoute{}).Where("id = ?", payload.ID).Select("public_id", "display_name", "client", "aliases", "metadata", "enabled", "updated_time").Updates(&route).Error; err != nil {
			return err
		}
		if err := tx.Where("route_id = ?", payload.ID).Delete(&model.ClientModelTarget{}).Error; err != nil {
			return err
		}
		for i := range payload.Targets {
			target := payload.Targets[i]
			target.Id = 0
			target.RouteId = payload.ID
			if err := tx.Create(&target).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		clientModelRouteError(c, routeValidationStatus(err), err)
		return
	}
	response, err := loadClientModelRouteResponse(model.DB, route.Id)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, response)
}

func DeleteClientModelRoute(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil || id <= 0 {
		clientModelRouteError(c, http.StatusBadRequest, errors.New("无效的模型路由 ID"))
		return
	}
	err = model.DB.Transaction(func(tx *gorm.DB) error {
		var count int64
		if err := tx.Model(&model.ClientModelRoute{}).Where("id = ?", id).Count(&count).Error; err != nil {
			return err
		}
		if count == 0 {
			return gorm.ErrRecordNotFound
		}
		if err := tx.Where("route_id = ?", id).Delete(&model.ClientModelTarget{}).Error; err != nil {
			return err
		}
		return tx.Delete(&model.ClientModelRoute{}, id).Error
	})
	if err != nil {
		clientModelRouteError(c, http.StatusBadRequest, err)
		return
	}
	common.ApiSuccess(c, nil)
}

func validateClientModelRoutePayload(tx *gorm.DB, payload clientModelRoutePayload) error {
	if tx == nil {
		return gorm.ErrInvalidDB
	}
	if strings.TrimSpace(payload.PublicID) == "" {
		return errors.New("public_id is required")
	}
	if len(payload.Targets) == 0 {
		return errors.New("at least one target is required")
	}
	route := payload.routeRecord()
	if route.Id == 0 {
		route.Id = 1
	}
	if route.Client == "" {
		route.Client = "all"
	}
	metadata, err := modelcatalog.NormalizeModelCapabilities(route.Metadata)
	if err != nil {
		return err
	}
	targets := make([]model.ClientModelTarget, 0, len(payload.Targets))
	for i, target := range payload.Targets {
		if target.Id == 0 {
			target.Id = i + 1
		}
		target.RouteId = route.Id
		var channel model.Channel
		if err := tx.Select("id", "type", "status", "models").First(&channel, target.ChannelId).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return fmt.Errorf("target %d references nonexistent channel %d", target.Id, target.ChannelId)
			}
			return err
		}
		if channel.Status != common.ChannelStatusEnabled {
			return fmt.Errorf("target %d references disabled channel %d", target.Id, target.ChannelId)
		}
		if !containsClientModelOption(channel.GetModels(), target.UpstreamModel) {
			return fmt.Errorf("target %d model %q is not enabled on channel %d", target.Id, target.UpstreamModel, target.ChannelId)
		}
		if !helper.HasModelBillingConfig(strings.TrimSpace(target.BillingModel)) {
			return fmt.Errorf("target %d billing model %q has no billing configuration", target.Id, target.BillingModel)
		}
		if err := modelcatalog.ValidateChannelOperations(metadata, channel.Type); err != nil {
			return err
		}
		if err := validateTargetWiresForChannel(target, channel.Type); err != nil {
			return err
		}
		if target.Enabled {
			targets = append(targets, target)
		}
	}
	if payload.Enabled && len(targets) == 0 {
		return errors.New("an enabled route requires at least one enabled target")
	}
	if _, err := modelcatalog.NewCatalog([]model.ClientModelRoute{route}, targets); err != nil {
		return err
	}
	return nil
}

func loadClientModelRouteResponse(tx *gorm.DB, id int) (clientModelRouteResponse, error) {
	var route model.ClientModelRoute
	if err := tx.First(&route, id).Error; err != nil {
		return clientModelRouteResponse{}, err
	}
	var targets []model.ClientModelTarget
	if err := tx.Where("route_id = ?", id).Order("priority DESC").Order("id ASC").Find(&targets).Error; err != nil {
		return clientModelRouteResponse{}, err
	}
	return clientModelRouteResponse{
		ID: route.Id, PublicID: route.PublicId, DisplayName: route.DisplayName, KnowledgeGeneration: route.KnowledgeGeneration,
		Client: route.Client, Aliases: decodeRouteAliases(route.Aliases), Metadata: route.Metadata,
		Enabled: route.Enabled, CreatedTime: route.CreatedTime, UpdatedTime: route.UpdatedTime, Targets: targets,
	}, nil
}

func validateTargetWiresForChannel(target model.ClientModelTarget, channelType int) error {
	var wires []string
	if err := common.Unmarshal([]byte(target.PreferredWires), &wires); err != nil {
		return fmt.Errorf("target %d preferred wires must be a JSON array", target.Id)
	}
	if len(wires) == 0 {
		return fmt.Errorf("target %d preferred wires are required", target.Id)
	}
	registry := capability.DefaultRegistry()
	for _, raw := range wires {
		switch modelcatalog.WireProtocol(strings.ToLower(strings.TrimSpace(raw))) {
		case modelcatalog.WireMultimodalEmbedding:
			if !registry.Supports(channelType, capability.ProtocolOpenAI, capability.OperationMultimodalEmbedding) {
				return fmt.Errorf("target %d channel does not support multimodal embeddings", target.Id)
			}
		case modelcatalog.WirePaddleOCR:
			if !registry.Supports(channelType, capability.ProtocolPaddleOCR, capability.OperationOCRCreate) {
				return fmt.Errorf("target %d channel does not support PaddleOCR jobs", target.Id)
			}
		case modelcatalog.WireResponses:
			if !registry.Supports(channelType, capability.ProtocolOpenAI, capability.OperationResponseCreate) {
				return fmt.Errorf("target %d channel does not support responses", target.Id)
			}
		case modelcatalog.WireOpenAIChat:
			if !registry.Supports(channelType, capability.ProtocolOpenAI, capability.OperationChatCreate) {
				return fmt.Errorf("target %d channel does not support OpenAI chat", target.Id)
			}
		case modelcatalog.WireAnthropic:
			if !registry.Supports(channelType, capability.ProtocolClaude, capability.OperationClaudeMessageCreate) {
				return fmt.Errorf("target %d channel does not support Anthropic messages", target.Id)
			}
		default:
			return fmt.Errorf("target %d has unknown wire protocol %q", target.Id, raw)
		}
	}
	return nil
}

func containsClientModelOption(models []string, requested string) bool {
	requested = strings.TrimSpace(requested)
	for _, candidate := range models {
		if strings.EqualFold(strings.TrimSpace(candidate), requested) {
			return true
		}
	}
	return false
}

func normalizeClientModelOptionNames(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		key := strings.ToLower(value)
		if value == "" {
			continue
		}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func ensureRouteNameAndAliasesAvailable(tx *gorm.DB, id int, payload clientModelRoutePayload) error {
	var routes []model.ClientModelRoute
	if err := tx.Find(&routes).Error; err != nil {
		return err
	}
	requested := make(map[string]struct{})
	requested[strings.ToLower(strings.TrimSpace(payload.PublicID))] = struct{}{}
	for _, alias := range payload.Aliases {
		key := strings.ToLower(strings.TrimSpace(alias))
		if key != "" {
			if _, ok := requested[key]; ok {
				return fmt.Errorf("alias %q collides with public model id", alias)
			}
			requested[key] = struct{}{}
		}
	}
	for _, route := range routes {
		if route.Id == id {
			continue
		}
		keys := append([]string{route.PublicId}, decodeRouteAliases(route.Aliases)...)
		for _, key := range keys {
			if _, ok := requested[strings.ToLower(strings.TrimSpace(key))]; ok {
				return fmt.Errorf("alias %q is already assigned", key)
			}
		}
	}
	return nil
}

func (payload clientModelRoutePayload) routeRecord() model.ClientModelRoute {
	aliases, _ := common.Marshal(payload.Aliases)
	client := strings.ToLower(strings.TrimSpace(payload.Client))
	if client == "" {
		client = "all"
	}
	return model.ClientModelRoute{Id: payload.ID, PublicId: strings.TrimSpace(payload.PublicID), DisplayName: strings.TrimSpace(payload.DisplayName), Client: client, Aliases: string(aliases), Metadata: payload.Metadata, Enabled: payload.Enabled}
}

func decodeRouteAliases(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return []string{}
	}
	var aliases []string
	if common.Unmarshal([]byte(raw), &aliases) == nil {
		return aliases
	}
	return strings.Split(raw, ",")
}

func routeValidationStatus(err error) int {
	if err == nil {
		return http.StatusOK
	}
	if strings.Contains(strings.ToLower(err.Error()), "alias") || strings.Contains(strings.ToLower(err.Error()), "already assigned") || strings.Contains(strings.ToLower(err.Error()), "public model id") {
		return http.StatusConflict
	}
	return http.StatusBadRequest
}

func clientModelRouteError(c *gin.Context, status int, err error) {
	if status == http.StatusOK {
		status = http.StatusBadRequest
	}
	c.JSON(status, gin.H{"success": false, "message": err.Error()})
}
