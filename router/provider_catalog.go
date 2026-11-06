package router

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"regexp"
	"slices"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/internal/platformbilling"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relay/capability"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/service/modelcatalog"
	"github.com/QuantumNous/new-api/service/ocr"
	"github.com/QuantumNous/new-api/setting/billing_setting"
	"github.com/gin-gonic/gin"
)

// ProviderCatalog is read-only delegated discovery. The account fields bind the
// response to the authenticated caller; neither groups nor prices are supplied
// by that caller. Inference separately verifies the original credential.
type ProviderCatalog struct {
	SchemaVersion   int                    `json:"schema_version"`
	CatalogVersion  string                 `json:"catalog_version"`
	ModelScope      string                 `json:"model_scope"`
	PricingSource   string                 `json:"pricing_source"`
	OriginProductID string                 `json:"origin_product_id"`
	InstanceID      string                 `json:"instance_id"`
	ActorID         string                 `json:"actor_id"`
	AccountID       string                 `json:"account_id"`
	Models          []ProviderCatalogModel `json:"models"`
}

type ProviderCatalogModel struct {
	ID                 string                       `json:"id"`
	Name               string                       `json:"name"`
	Operations         []string                     `json:"operations"`
	InputModalities    []string                     `json:"input_modalities"`
	OutputModalities   []string                     `json:"output_modalities"`
	Context            int64                        `json:"context"`
	MaxOutput          int64                        `json:"max_output"`
	Probe              bool                         `json:"probe"`
	RateID             string                       `json:"rate_id"`
	CapabilityMetadata platformbilling.CatalogModel `json:"capability_metadata"`
}

type providerCatalogRequest struct {
	OriginProductID string `json:"origin_product_id"`
	InstanceID      string `json:"instance_id"`
	ActorID         string `json:"actor_id"`
	AccountID       string `json:"account_id"`
}

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
		if common.UnmarshalUnique(data, &req) != nil || decoder.Decode(&req) != nil || req.OriginProductID != "superwagie" || req.InstanceID != config.InstanceID || !providerCatalogIdentity.MatchString(req.ActorID) || !providerCatalogIdentity.MatchString(req.AccountID) {
			c.Status(http.StatusForbidden)
			return
		}
		grant := platformbilling.Claims{OriginProductID: req.OriginProductID, InstanceID: req.InstanceID, ActorID: req.ActorID, AccountID: req.AccountID}
		result, err := readProviderCatalog(c.Request.Context(), adapter, grant)
		if err != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "provider_catalog_unavailable"})
			return
		}
		c.JSON(http.StatusOK, result)
	})
}

func readProviderCatalog(ctx context.Context, adapter *platformbilling.Adapter, grant platformbilling.Claims) (ProviderCatalog, error) {
	out := ProviderCatalog{SchemaVersion: 1, ModelScope: "llmrouter_available", PricingSource: "llmrouter_native", OriginProductID: grant.OriginProductID, InstanceID: grant.InstanceID, ActorID: grant.ActorID, AccountID: grant.AccountID, Models: []ProviderCatalogModel{}}
	if adapter == nil || !adapter.Enabled() || model.DB == nil || grant.OriginProductID != "superwagie" {
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
	catalog, err := modelcatalog.AssembleRuntimeCatalog(ctx, runtime, routes, targets, modelcatalog.NewGormRuntimeSources(model.DB), deploymentCatalogAuthorizer{user, group, grant.OriginProductID})
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
	if err := model.DB.WithContext(ctx).Select("id", "type", "status", "settings", "key", "channel_info").Where("status = ?", common.ChannelStatusEnabled).Find(&channels).Error; err != nil {
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
		billingModels := make([]string, 0, len(candidates))
		for _, candidate := range candidates {
			billingModels = append(billingModels, candidate.BillingModel)
		}
		for i, op := range projected.CapabilityMetadata.Operations {
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
		byID[m.PublicID] = providerPriceAvailability(projected, billingModels)
	}
	// Read enabled abilities directly, without the legacy model-count limit or
	// the price page's refresh cache. Channel credentials never enter this view.
	var abilities []model.Ability
	if err := model.DB.WithContext(ctx).Where(&model.Ability{Group: group, Enabled: true}).Order("model").Order("channel_id").Find(&abilities).Error; err != nil {
		return out, err
	}
	// Existing endpoint metadata augments the adapter's native defaults.
	model.GetPricing()
	for _, ability := range abilities {
		if reserved[strings.ToLower(ability.Model)] || (!helper.HasModelBillingConfig(ability.Model) && !billing_setting.HasModelNativePricing(ability.Model)) {
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
		for _, endpoint := range endpoints {
			wire := providerEndpointOperation(endpoint)
			if wire == "ocr.jobs.create" && !providerOCRChannelSupported(ch) {
				continue
			}
			if wire == "" || !capability.DefaultRegistry().Supports(ch.Type, capability.ProtocolForOperation(capability.Operation(wire)), capability.Operation(wire)) {
				continue
			}
			if !slices.Contains(m.Capabilities.Operations, wire) {
				m.Capabilities.Operations = append(m.Capabilities.Operations, wire)
			}
		}
		byID[ability.Model] = providerPriceAvailability(projectProviderModel(m), []string{ability.Model})
	}
	for _, m := range byID {
		out.Models = append(out.Models, m)
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
