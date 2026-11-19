package router

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"io"
	"math/big"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/internal/platformbilling"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/metering"
	"github.com/QuantumNous/new-api/relay/capability"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/service/modelcatalog"
	"github.com/QuantumNous/new-api/service/ocr"
	"github.com/QuantumNous/new-api/setting/billing_setting"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"
)

// ProviderCatalog is read-only delegated discovery. The account fields bind the
// response to the authenticated caller; neither groups nor prices are supplied
// by that caller. Inference separately verifies the original credential.
type ProviderCatalog struct {
	CreditScope          string                 `json:"credit_scope,omitempty"`
	CreditScopes         []string               `json:"credit_scopes,omitempty"`
	Purpose              string                 `json:"purpose,omitempty"`
	NativeCreditProtocol string                 `json:"native_credit_protocol,omitempty"`
	SchemaVersion        int                    `json:"schema_version"`
	CatalogVersion       string                 `json:"catalog_version"`
	ModelScope           string                 `json:"model_scope"`
	PricingSource        string                 `json:"pricing_source"`
	OriginProductID      string                 `json:"origin_product_id"`
	InstanceID           string                 `json:"instance_id"`
	ActorID              string                 `json:"actor_id"`
	AccountID            string                 `json:"account_id"`
	Models               []ProviderCatalogModel `json:"models"`
}

type ProviderCatalogModel struct {
	ConfiguredUpstreamModels []string                     `json:"configured_upstream_models,omitempty"`
	ID                       string                       `json:"id"`
	Name                     string                       `json:"name"`
	Operations               []string                     `json:"operations"`
	InputModalities          []string                     `json:"input_modalities"`
	OutputModalities         []string                     `json:"output_modalities"`
	Context                  int64                        `json:"context"`
	MaxOutput                int64                        `json:"max_output"`
	Probe                    bool                         `json:"probe"`
	RateID                   string                       `json:"rate_id"`
	CapabilityMetadata       platformbilling.CatalogModel `json:"capability_metadata"`
}

type providerCatalogRequest struct {
	CreditScope     string `json:"credit_scope,omitempty"`
	Purpose         string `json:"purpose,omitempty"`
	OriginProductID string `json:"origin_product_id"`
	InstanceID      string `json:"instance_id"`
	ActorID         string `json:"actor_id"`
	AccountID       string `json:"account_id"`
}

var providerCatalogProductID = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)
var providerCatalogIdentity = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,191}$`)

func SetProviderCatalogRouter(engine *gin.Engine, adapter *platformbilling.Adapter, config platformbilling.CatalogConfig) {
	engine.POST("/internal/v1/provider-catalog", func(c *gin.Context) {
		c.Header("Cache-Control", "no-store")
		if !config.Valid() {
			c.Status(http.StatusServiceUnavailable)
			return
		}
		auth := c.Request.Header.Values("Authorization")
		if len(auth) != 1 || subtle.ConstantTimeCompare([]byte(auth[0]), []byte("Bearer "+config.Token)) != 1 {
			c.Status(http.StatusUnauthorized)
			return
		}
		if c.Request.URL.RawQuery != "" {
			c.Status(http.StatusForbidden)
			return
		}
		data, err := io.ReadAll(http.MaxBytesReader(c.Writer, c.Request.Body, 4096))
		if err != nil {
			c.Status(http.StatusBadRequest)
			return
		}
		var req providerCatalogRequest
		decoder := common.NewJsonDecoder(strings.NewReader(string(data)))
		decoder.DisallowUnknownFields()
		if common.UnmarshalUnique(data, &req) != nil || decoder.Decode(&req) != nil {
			c.Status(http.StatusForbidden)
			return
		}
		scoped := req.CreditScope == platformbilling.ProductCreditScope
		if (!scoped && (req.CreditScope != "" || req.OriginProductID != "superwagie")) ||
			(scoped && !providerCatalogProductID.MatchString(req.OriginProductID)) {
			c.Status(http.StatusForbidden)
			return
		}
		if req.Purpose == "native_credit_readiness" {
			if req.InstanceID != "" || req.ActorID != "" || req.AccountID != "" {
				c.Status(http.StatusForbidden)
				return
			}
			principal, group := adapter.NativePrincipal()
			if adapter == nil || !adapter.Enabled() || principal < 1 || group == "" {
				c.Status(http.StatusServiceUnavailable)
				return
			}
			pricing := "llmrouter_native"
			if scoped {
				grant := platformbilling.Claims{OriginProductID: req.OriginProductID, InstanceID: config.InstanceID}
				catalog, err := buildProviderCatalog(c.Request.Context(), adapter, grant, true)
				if err != nil {
					c.JSON(http.StatusServiceUnavailable, gin.H{"error": "provider_catalog_unavailable"})
					return
				}
				if !slices.Contains(catalog.CreditScopes, platformbilling.ProductCreditScope) {
					c.JSON(http.StatusServiceUnavailable, gin.H{"error": "product_credit_models_unavailable"})
					return
				}
				pricing = platformbilling.NativeCreditPricingSource
			}
			c.JSON(http.StatusOK, ProviderCatalog{CreditScope: req.CreditScope, Purpose: req.Purpose, CreditScopes: []string{platformbilling.ProductCreditScope}, NativeCreditProtocol: platformbilling.NativeCreditPricingVersion, SchemaVersion: 1, CatalogVersion: platformbilling.NativeCreditPricingVersion, ModelScope: "llmrouter_available", PricingSource: pricing, OriginProductID: req.OriginProductID, Models: []ProviderCatalogModel{}})
			return
		}
		if req.Purpose != "" || req.InstanceID != config.InstanceID || !providerCatalogIdentity.MatchString(req.ActorID) || !providerCatalogIdentity.MatchString(req.AccountID) {
			c.Status(http.StatusForbidden)
			return
		}
		grant := platformbilling.Claims{OriginProductID: req.OriginProductID, InstanceID: req.InstanceID, ActorID: req.ActorID, AccountID: req.AccountID}
		result, err := buildProviderCatalog(c.Request.Context(), adapter, grant, scoped)
		if err != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "provider_catalog_unavailable"})
			return
		}
		c.JSON(http.StatusOK, result)
	})
}

func readProviderCatalog(ctx context.Context, adapter *platformbilling.Adapter, grant platformbilling.Claims) (ProviderCatalog, error) {
	return buildProviderCatalog(ctx, adapter, grant, grant.ProductCredit())
}

// The scoped flag is either a verified Key claim or an explicit read-only
// delegation made by the authenticated UniHub catalog service.
func buildProviderCatalog(ctx context.Context, adapter *platformbilling.Adapter, grant platformbilling.Claims, scoped bool) (ProviderCatalog, error) {
	out := ProviderCatalog{CreditScopes: []string{platformbilling.ProductCreditScope}, NativeCreditProtocol: platformbilling.NativeCreditPricingVersion, SchemaVersion: 1, ModelScope: "llmrouter_available", PricingSource: "llmrouter_native", OriginProductID: grant.OriginProductID, InstanceID: grant.InstanceID, ActorID: grant.ActorID, AccountID: grant.AccountID, Models: []ProviderCatalogModel{}}
	if adapter == nil || !adapter.Enabled() || model.DB == nil || grant.OriginProductID != "superwagie" && !scoped {
		return out, errors.New("provider catalog unavailable")
	}
	user, group := adapter.NativePrincipal()
	if user <= 0 || strings.TrimSpace(group) == "" {
		return out, errors.New("native routing group unavailable")
	}
	var routes []model.ClientModelRoute
	var targets []model.ClientModelTarget
	if err := model.DB.WithContext(ctx).Where("enabled = ?", true).Order("id").Find(&routes).Error; err != nil {
		return out, err
	}
	if err := model.DB.WithContext(ctx).Where("enabled = ?", true).Order("id").Find(&targets).Error; err != nil {
		return out, err
	}
	runtime := modelcatalog.RuntimeContext{UserID: user, Groups: []string{group}, Client: grant.OriginProductID}
	assemble := modelcatalog.AssembleRuntimeCatalog
	if scoped {
		assemble = modelcatalog.AssembleManagedRuntimeCatalog
		out.CreditScope = platformbilling.ProductCreditScope
		out.CreditScopes = nil
		out.PricingSource = platformbilling.NativeCreditPricingSource
	}
	catalog, err := assemble(ctx, runtime, routes, targets, modelcatalog.NewGormRuntimeSources(model.DB), deploymentCatalogAuthorizer{user, group, grant.OriginProductID})
	if err != nil {
		return out, err
	}
	explicit, err := catalog.List(ctx, user, runtime.Groups, runtime.Client)
	if err != nil {
		return out, err
	}
	byID := map[string]ProviderCatalogModel{}
	reserved := map[string]bool{}
	// Public routes own their identifiers even when a configured target is
	// unavailable. Falling back to a legacy ability would bypass that contract.
	for _, route := range routes {
		reserved[strings.ToLower(route.PublicId)] = true
		var aliases []string
		if route.Aliases != "" {
			if err := common.UnmarshalJsonStr(route.Aliases, &aliases); err != nil {
				return out, err
			}
		}
		for _, alias := range aliases {
			reserved[strings.ToLower(alias)] = true
		}
	}
	var channels []model.Channel
	if err := model.DB.WithContext(ctx).Select("id", "type", "status", "settings", "setting", "key", "channel_info", "base_url", "param_override", "header_override", "model_mapping").Where("status = ?", common.ChannelStatusEnabled).Find(&channels).Error; err != nil {
		return out, err
	}
	channelByID := map[int]model.Channel{}
	for _, ch := range channels {
		channelByID[ch.Id] = ch
	}
	for _, m := range explicit {
		projected := projectProviderModel(platformbilling.CatalogModel{ID: m.PublicID, Name: m.DisplayName, Capabilities: m.ModelCapabilities, ContextWindow: m.ContextWindow, SupportsTools: m.SupportsTools, SupportsReasoningSummary: m.SupportsReasoningSummary, ReasoningEfforts: m.ReasoningEfforts})
		candidates, err := catalog.ResolveCandidates(ctx, user, runtime.Groups, m.PublicID, runtime.Client)
		if err != nil {
			return out, err
		}
		if strings.EqualFold(m.PublicID, "qwen3-vl-embedding") || slices.Contains(m.ConfiguredUpstreamModels, "qwen3-vl-embedding") {
			if !slices.Contains(m.Operations, "multimodal.embeddings.create") || helper.ValidateMultimodalEmbeddingSpace(&dto.MultimodalEmbeddingRequest{}, m.EmbeddingSpace) != nil || len(candidates) == 0 {
				continue
			}
			valid := true
			for _, candidate := range candidates {
				if candidate.UpstreamModel != "qwen3-vl-embedding" || channelByID[candidate.ChannelID].Type != constant.ChannelTypeAli {
					valid = false
					break
				}
			}
			if !valid {
				continue
			}
		}
		billingModels := make([]string, 0, len(candidates))
		for _, candidate := range candidates {
			billingModels = append(billingModels, candidate.BillingModel)
			if !scoped && dto.IsPublicConfiguredModelID(candidate.UpstreamModel) && !slices.Contains(projected.ConfiguredUpstreamModels, candidate.UpstreamModel) {
				projected.ConfiguredUpstreamModels = append(projected.ConfiguredUpstreamModels, candidate.UpstreamModel)
			}
		}
		for i, op := range projected.CapabilityMetadata.Operations {
			if op.ID == "bailian.audio.speech.create" && !slices.ContainsFunc(candidates, func(candidate modelcatalog.ResolvedModel) bool {
				if scoped {
					return managedBailianSpeechChannelSupported(candidate, channelByID[candidate.ChannelID])
				}
				_, _, err := managedBailianSpeechTariff(candidate.BillingModel)
				return err == nil && managedBailianSpeechChannelSupported(candidate, channelByID[candidate.ChannelID])
			}) {
				op.Available, op.Executable = false, false
				op.DisabledReason = "native_speech_target_unavailable"
				projected.CapabilityMetadata.Operations[i] = op
			}
			if op.ID == "ocr.jobs.create" && !slices.ContainsFunc(candidates, func(candidate modelcatalog.ResolvedModel) bool {
				return providerOCRChannelSupported(channelByID[candidate.ChannelID])
			}) {
				op.Available = false
				op.Executable = false
				op.DisabledReason = "native_ocr_target_unavailable"
				projected.CapabilityMetadata.Operations[i] = op
			}
			if op.ID == "dashscope.images.generate" && !slices.ContainsFunc(candidates, func(candidate modelcatalog.ResolvedModel) bool {
				return managedQwenImageChannelSupported(candidate, channelByID[candidate.ChannelID])
			}) {
				op.Available = false
				op.Executable = false
				op.DisabledReason = "native_image_target_unavailable"
				projected.CapabilityMetadata.Operations[i] = op
			}
		}
		if scoped {
			byID[m.PublicID] = productCreditPriceAvailability(projected, candidates, nil, channelByID, adapter)
		} else {
			byID[m.PublicID] = providerPriceAvailability(projected, billingModels)
		}
	}
	// Read enabled abilities directly, without the legacy model-count limit or
	// the price page's refresh cache. Channel credentials never enter this view.
	var abilities []model.Ability
	if err := model.DB.WithContext(ctx).Where(&model.Ability{Group: group, Enabled: true}).Order("model").Order("channel_id").Find(&abilities).Error; err != nil {
		return out, err
	}
	// Existing endpoint metadata augments the adapter's native defaults.
	model.GetPricing()
	chatAbilityTargets := map[string][]modelcatalog.ResolvedModel{}
	for _, ability := range abilities {
		if strings.EqualFold(ability.Model, "qwen3-vl-embedding") || reserved[strings.ToLower(ability.Model)] ||
			(!scoped && !helper.HasModelBillingConfig(ability.Model) && !billing_setting.HasModelNativePricing(ability.Model)) {
			continue
		}
		ch, ok := channelByID[ability.ChannelId]
		if !ok {
			continue
		}
		endpoints := common.GetEndpointTypesByChannelType(ch.Type, ability.Model)
		if ch.Type == constant.ChannelTypeCodex {
			// The subscription adapter implements Responses regardless of model ID.
			endpoints = []constant.EndpointType{constant.EndpointTypeOpenAIResponse, constant.EndpointTypeOpenAIResponseCompact}
		}
		if ch.Type != constant.ChannelTypeCodex {
			endpoints = append(endpoints, model.GetModelSupportEndpointTypes(ability.Model)...)
		}
		if ch.Type == constant.ChannelTypeAdvancedCustom {
			settings := ch.GetOtherSettings()
			if settings.AdvancedCustom == nil {
				continue
			}
			endpoints = settings.AdvancedCustom.SupportedEndpointTypesForModel(ability.Model)
		}
		m := byID[ability.Model].CapabilityMetadata
		if m.ID == "" {
			m = platformbilling.CatalogModel{ID: ability.Model, Name: ability.Model}
		}
		if scoped {
			for _, wire := range []string{"videos.create", "dashscope.audio.transcriptions.create", "gemini.images.generate", "images.edits.create", "images.generations.create"} {
				if !slices.Contains(m.Capabilities.Operations, wire) && catalogCertifiesProductTarget(adapter, ability.Model, wire, ch, "") {
					m.Capabilities.Operations = append(m.Capabilities.Operations, wire)
				}
			}
		}
		for _, endpoint := range endpoints {
			wire := providerEndpointOperation(endpoint)
			if wire == "ocr.jobs.create" && !providerOCRChannelSupported(ch) {
				continue
			}
			if scoped && wire == "chat.completions.create" && ch.Type == constant.ChannelTypeDeepSeek {
				// The scoped producer requires an explicit certified route target.
				continue
			}
			if scoped && (platformbilling.IsProductCertifiedOperation(wire) || !platformbilling.ProductCreditChannelSupported(wire, ch.Type)) {
				continue
			}
			if wire == "" || !capability.DefaultRegistry().Supports(ch.Type, capability.ProtocolForOperation(capability.Operation(wire)), capability.Operation(wire)) {
				continue
			}
			if scoped && wire == "chat.completions.create" && ch.Type == constant.ChannelTypeOpenAI {
				var mapping map[string]string
				rawMapping := ch.GetModelMapping()
				if rawMapping == "" || common.UnmarshalJsonStr(rawMapping, &mapping) == nil {
					if upstream, err := common.ResolveModelMapping(ability.Model, mapping); err == nil {
						chatAbilityTargets[ability.Model] = append(chatAbilityTargets[ability.Model], modelcatalog.ResolvedModel{
							ChannelID: ch.Id, UpstreamModel: upstream, Capabilities: dto.ModelCapabilities{Operations: []string{wire}},
						})
					}
				}
			}
			if !slices.Contains(m.Capabilities.Operations, wire) {
				m.Capabilities.Operations = append(m.Capabilities.Operations, wire)
			}
		}
		if scoped {
			byID[ability.Model] = projectProviderModel(m)
		} else {
			byID[ability.Model] = providerPriceAvailability(projectProviderModel(m), []string{ability.Model})
		}
	}
	if scoped {
		for id, item := range byID {
			if !reserved[strings.ToLower(id)] {
				byID[id] = productCreditPriceAvailability(item, nil, chatAbilityTargets[id], channelByID, adapter)
			}
		}
		projectProductKnowledgeAvailability(ctx, adapter, grant.OriginProductID, routes, byID)
	}
	for _, m := range byID {
		out.Models = append(out.Models, m)
		if scoped && len(out.CreditScopes) == 0 && slices.ContainsFunc(m.CapabilityMetadata.Operations, func(op platformbilling.CatalogOperation) bool {
			return op.Available && op.Executable && op.PublicTariff != nil
		}) {
			out.CreditScopes = []string{platformbilling.ProductCreditScope}
		}
	}
	slices.SortFunc(out.Models, func(a, b ProviderCatalogModel) int { return strings.Compare(a.ID, b.ID) })
	raw, err := common.Marshal(out.Models)
	if err != nil {
		return out, err
	}
	digest := sha256.Sum256(raw)
	out.CatalogVersion = hex.EncodeToString(digest[:])
	return out, nil
}

func providerOCRChannelSupported(channel model.Channel) bool {
	_, available := ocr.ManagedNativeChannelKey(&channel)
	return available
}

func providerEndpointOperation(endpoint constant.EndpointType) string {
	switch endpoint {
	case constant.EndpointTypeOpenAI:
		return "chat.completions.create"
	case constant.EndpointTypeOpenAIResponse:
		return "responses.create"
	case constant.EndpointTypeOpenAIResponseCompact:
		return "responses.compact"
	case constant.EndpointTypeAnthropic:
		return "claude.messages.create"
	case constant.EndpointTypeGemini:
		return "gemini.generate"
	case constant.EndpointTypeJinaRerank:
		return "rerank.create"
	case constant.EndpointTypeImageGeneration:
		return "images.generations.create"
	case constant.EndpointTypeEmbeddings:
		return "embeddings.create"
	case constant.EndpointTypePaddleOCR:
		return "ocr.jobs.create"
	case constant.EndpointTypeOpenAIVideo:
		return "videos.create"
	default:
		return ""
	}
}

func projectProviderModel(m platformbilling.CatalogModel) ProviderCatalogModel {
	m.Name = platformbilling.CatalogDisplayName(m.Name, m.Capabilities)
	out := ProviderCatalogModel{ID: m.ID, Name: m.Name, Operations: []string{}, InputModalities: append([]string{}, m.Capabilities.InputModalities...), OutputModalities: append([]string{}, m.Capabilities.OutputModalities...), Context: int64(m.ContextWindow)}
	if m.Capabilities.MaxOutputTokens != nil {
		out.MaxOutput = int64(*m.Capabilities.MaxOutputTokens)
	}
	m.Operations = []platformbilling.CatalogOperation{}
	slices.Sort(m.Capabilities.Operations)
	for _, wire := range m.Capabilities.Operations {
		op, input, output := providerOperation(wire, m.ID)
		if op.ID == "" {
			continue
		}
		if m.Capabilities.Streaming != nil && *m.Capabilities.Streaming && (op.BillingOperation == "chat" || op.BillingOperation == "responses") {
			op.Transports = append(op.Transports, "sse")
		}
		m.Operations = append(m.Operations, op)
		if !slices.Contains(out.Operations, op.BillingOperation) {
			out.Operations = append(out.Operations, op.BillingOperation)
		}
		if len(m.Capabilities.InputModalities) == 0 && !slices.Contains(out.InputModalities, input) {
			out.InputModalities = append(out.InputModalities, input)
		}
		if len(m.Capabilities.OutputModalities) == 0 && !slices.Contains(out.OutputModalities, output) {
			out.OutputModalities = append(out.OutputModalities, output)
		}
	}
	if len(m.Operations) == 0 {
		m.DisabledReason = "capabilities_unconfigured"
	}
	out.Probe = slices.ContainsFunc(m.Operations, func(op platformbilling.CatalogOperation) bool { return op.Executable })
	m.Capabilities.InputModalities = out.InputModalities
	m.Capabilities.OutputModalities = out.OutputModalities
	out.CapabilityMetadata = m
	return out
}

func providerOperation(wire, modelID string) (platformbilling.CatalogOperation, string, string) {
	op := platformbilling.CatalogOperation{ID: wire, Protocol: string(capability.ProtocolForOperation(capability.Operation(wire))), Method: "POST", Transports: []string{"json"}, Available: true, Executable: true, Policy: "llmrouter_native"}
	if !platformbilling.NativeOperationAvailable(wire) {
		op.Available = false
		op.Executable = false
		op.DisabledReason = "native_operation_unavailable"
	}
	input, output := "text", "text"
	switch wire {
	case "chat.completions.create":
		op.Endpoint = "/v1/chat/completions"
		op.BillingOperation = "chat"
	case "responses.create":
		op.Endpoint = "/v1/responses"
		op.BillingOperation = "responses"
	case "responses.compact":
		op.Endpoint = "/v1/responses/compact"
		op.BillingOperation = "responses"
	case "claude.messages.create":
		op.Endpoint = "/v1/messages"
		op.BillingOperation = "chat"
	case "gemini.generate":
		op.Endpoint = "/v1beta/models/" + modelID + ":generateContent"
		op.BillingOperation = "chat"
	case "embeddings.create":
		op.Endpoint = "/v1/embeddings"
		op.BillingOperation = "embedding"
		output = "embedding"
	case "multimodal.embeddings.create":
		op.Endpoint = "/v1/multimodal/embeddings"
		op.BillingOperation = "multimodal_embedding"
		output = "embedding"
	case "rerank.create":
		op.Endpoint = "/v1/rerank"
		op.BillingOperation = "rerank"
		output = "relevance"
	case "images.generations.create":
		op.Endpoint = "/v1/images/generations"
		op.BillingOperation = "image_generation"
		output = "image"
	case "images.edits.create":
		op.Endpoint = "/v1/images/edits"
		op.BillingOperation = "image_edit"
		input = "image"
		output = "image"
	case "dashscope.images.generate":
		op.Endpoint = "/v1/native/dashscope/images/generations"
		op.BillingOperation = "image_generation"
		output = "image"
	case "gemini.images.generate":
		op.Endpoint = "/v1/native/gemini/images/models/" + modelID + ":generateContent"
		op.BillingOperation = "image_generation"
		output = "image"
	case "ocr.jobs.create":
		op.Endpoint = "/v1/ocr"
		op.BillingOperation = "ocr"
		input = "document"
		op.Transports = []string{"async_job"}
	case "dashscope.ocr.recognitions.create":
		op.Endpoint = "/v1/native/dashscope/ocr/recognitions"
		op.BillingOperation = "ocr"
		input = "image"
	case "audio.speech.create":
		op.Endpoint = "/v1/audio/speech"
		op.BillingOperation = "speech"
		output = "audio"
		op.Transports = []string{"binary"}
	case "bailian.audio.speech.create", "elevenlabs.audio.speech.create":
		op.Endpoint = "/v1/native/" + op.Protocol + "/audio/speech"
		op.BillingOperation = "speech"
		output = "audio"
		op.Transports = []string{"binary"}
	case "audio.transcriptions.create":
		op.Endpoint = "/v1/audio/transcriptions"
		op.BillingOperation = "transcription"
		input = "audio"
	case "dashscope.audio.transcriptions.create":
		op.Endpoint = "/v1/native/dashscope/audio/transcriptions"
		op.BillingOperation = "transcription"
		input = "audio"
	case "videos.create":
		op.Endpoint = "/v1/videos"
		op.BillingOperation = "video"
		output = "video"
		op.Transports = []string{"async_job"}
	default:
		return platformbilling.CatalogOperation{}, "", ""
	}
	return op, input, output
}

// Native-only prices authorize their own operation, not unrelated operations
// declared on the same model. Legacy native pricing keeps its existing rules.
func providerPriceAvailability(m ProviderCatalogModel, billingModels []string) ProviderCatalogModel {
	for i, op := range m.CapabilityMetadata.Operations {
		priced := slices.ContainsFunc(billingModels, func(name string) bool {
			if op.ID == "bailian.audio.speech.create" {
				_, _, err := managedBailianSpeechTariff(name)
				return err == nil
			}
			if helper.HasModelBillingConfig(name) {
				return true
			}
			_, ok := billing_setting.ResolveModelNativePrice(name, op.BillingOperation)
			return ok
		})
		if !priced {
			op.Available = false
			op.Executable = false
			op.DisabledReason = "native_price_unavailable"
			m.CapabilityMetadata.Operations[i] = op
		}
	}
	m.Probe = slices.ContainsFunc(m.CapabilityMetadata.Operations, func(op platformbilling.CatalogOperation) bool { return op.Executable })
	return m
}

func productCreditPriceAvailability(m ProviderCatalogModel, candidates, chatAbilityTargets []modelcatalog.ResolvedModel, channels map[int]model.Channel, adapter *platformbilling.Adapter) ProviderCatalogModel {
	fx := strconv.FormatFloat(operation_setting.USDExchangeRate, 'g', -1, 64)
	chatTargets := candidates
	if len(chatTargets) == 0 {
		chatTargets = chatAbilityTargets
	}
	for i, op := range m.CapabilityMetadata.Operations {
		op.PublicTariff = nil
		if op.ID == "multimodal.embeddings.create" && len(candidates) > 0 && slices.ContainsFunc(candidates, func(candidate modelcatalog.ResolvedModel) bool {
			return !productMultimodalTarget(adapter, m.ID, candidate, channels[candidate.ChannelID])
		}) {
			op.Available, op.Executable, op.DisabledReason = false, false, "certified_knowledge_target_unavailable"
		}
		if platformbilling.IsProductCertifiedOperation(op.ID) {
			// Ability-only operations were added only on their certified target.
			if len(candidates) > 0 && !slices.ContainsFunc(candidates, func(candidate modelcatalog.ResolvedModel) bool {
				return slices.Contains(candidate.Capabilities.Operations, op.ID) &&
					catalogCertifiesProductTarget(adapter, m.ID, op.ID, channels[candidate.ChannelID], candidate.UpstreamModel)
			}) {
				op.Available, op.Executable, op.DisabledReason = false, false, "product_credit_certified_target_unavailable"
			} else if op.DisabledReason == "native_operation_unavailable" {
				op.Available, op.Executable, op.DisabledReason = true, true, ""
			}
		}
		if !op.Executable {
			m.CapabilityMetadata.Operations[i] = op
			continue
		}
		if !platformbilling.ProductCreditOperation(op.ID) || len(candidates) > 0 && !slices.ContainsFunc(candidates, func(candidate modelcatalog.ResolvedModel) bool {
			channel := channels[candidate.ChannelID]
			return slices.Contains(candidate.Capabilities.Operations, op.ID) && platformbilling.ProductCreditChannelSupported(op.ID, channel.Type)
		}) {
			op.Available, op.Executable, op.DisabledReason = false, false, "product_credit_operation_unavailable"
		} else if price, ok := billing_setting.ResolveProductCreditPrice(m.ID, op.BillingOperation, fx); !ok {
			op.Available, op.Executable, op.DisabledReason = false, false, "product_credit_price_unavailable"
		} else if !productCreditChatPriceAvailable(m.ID, op.ID, chatTargets, channels, adapter, price.Tariff) {
			op.Available, op.Executable, op.DisabledReason = false, false, "product_credit_banded_chat_target_unavailable"
		} else {
			op.PublicTariff = publicProductCreditTariff(price)
			if len(price.Tariff.InputTokenBands) != 0 && len(m.CapabilityMetadata.Operations) == 1 {
				m.InputModalities = []string{"text"}
				m.CapabilityMetadata.Capabilities.InputModalities = []string{"text"}
			}
		}
		m.CapabilityMetadata.Operations[i] = op
	}
	m.Probe = slices.ContainsFunc(m.CapabilityMetadata.Operations, func(op platformbilling.CatalogOperation) bool { return op.Executable })
	return m
}

func productCreditChatPriceAvailable(modelID, wire string, candidates []modelcatalog.ResolvedModel, channels map[int]model.Channel, adapter *platformbilling.Adapter, price metering.Price) bool {
	bands := price.InputTokenBands
	if wire != "chat.completions.create" || len(candidates) == 0 {
		return len(bands) == 0
	}
	for _, candidate := range candidates {
		channel := channels[candidate.ChannelID]
		if !slices.Contains(candidate.Capabilities.Operations, wire) || !platformbilling.ProductCreditChannelSupported(wire, channel.Type) ||
			channel.Type == constant.ChannelTypeDeepSeek && !platformbilling.DeepSeekProductChatTariff(price) {
			continue
		}
		if len(bands) == 0 {
			return true
		}
		base := channel.GetBaseURL()
		if base == "" {
			base = constant.ChannelBaseURLs[channel.Type]
		}
		endpoint := relaycommon.GetFullRequestURL(base, "/v1/chat/completions", channel.Type)
		if adapter.ProductBandedChatTarget(modelID, channel.Id, channel.Type, candidate.UpstreamModel, endpoint, bands[len(bands)-1].MaxInputTokens) {
			return true
		}
	}
	return false
}

func catalogCertifiesProductTarget(adapter *platformbilling.Adapter, name, wire string, channel model.Channel, candidateUpstream string) bool {
	id, upstream := adapter.ProductCertifiedModelTarget(name, wire)
	if id <= 0 || id != channel.Id || channel.ChannelInfo.IsMultiKey || candidateUpstream != "" && candidateUpstream != upstream ||
		!platformbilling.ProductCreditChannelSupported(wire, channel.Type) {
		return false
	}
	switch wire {
	case "videos.create":
		return adapter.VideoTarget(name, id, channel.Type, upstream, channel.Key, channel.GetBaseURL())
	case "dashscope.audio.transcriptions.create":
		return adapter.AudioTarget(name, wire, id, channel.Type, upstream, channel.Key, channel.GetBaseURL())
	default:
		return adapter.ImageTarget(name, wire, id, channel.Type, upstream, channel.Key, channel.GetBaseURL())
	}
}

// Publish only the customer tariff and immutable revision. Supplier quotes,
// route identifiers and provider evidence stay in the frozen private price.
func publicProductCreditTariff(price billing_setting.ProductCreditPrice) *platformbilling.CatalogPublicTariff {
	microcreditsPerUSD, _ := new(big.Rat).SetString(price.MicrocreditsPerUSD())
	tariff := price.Tariff
	tariff.Terms = slices.Clone(price.Tariff.Terms)
	convert := func(price metering.Rational) metering.Rational {
		rate, _ := new(big.Rat).SetString(price.Numerator + "/" + price.Denominator)
		rate.Mul(rate, microcreditsPerUSD)
		return metering.Rational{Numerator: rate.Num().String(), Denominator: rate.Denom().String()}
	}
	for i := range tariff.Terms {
		tariff.Terms[i].Price = convert(tariff.Terms[i].Price)
	}
	tariff.InputTokenBands = slices.Clone(price.Tariff.InputTokenBands)
	for i, band := range price.Tariff.InputTokenBands {
		prices := make(map[string]metering.Rational, len(band.Prices))
		for key, rate := range band.Prices {
			prices[key] = convert(rate)
		}
		tariff.InputTokenBands[i].Prices = prices
	}
	return &platformbilling.CatalogPublicTariff{Asset: "CREDIT/6", Revision: price.Revision, Tariff: tariff}
}
