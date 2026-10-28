package platformbilling

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/pkg/mediafacts"
)

// CatalogConfig is inbound read-only authority, separate from outbound billing
// and product-runtime credentials. No account or routing selector is accepted.
type CatalogConfig struct{ Token, ProductID, InstanceID string }

func CatalogConfigFromEnv(get func(string) string) CatalogConfig {
	c := CatalogConfig{Token: get("UNIHUB_CATALOG_EXPORT_TOKEN"), ProductID: get("UNIHUB_CATALOG_PRODUCT_ID"), InstanceID: get("UNIHUB_CATALOG_INSTANCE_ID")}
	for _, name := range []string{"UNIHUB_MODEL_SERVICE_TOKEN", "UNIHUB_PRODUCT_RUNTIME_TOKEN", "MANAGEMENT_TOKEN"} {
		if c.Token != "" && c.Token == get(name) {
			return CatalogConfig{}
		}
	}
	return c
}
func (c CatalogConfig) Valid() bool {
	return len(c.Token) >= 32 && !strings.ContainsAny(c.Token, " \r\n\t") && stableID.MatchString(c.ProductID) && stableID.MatchString(c.InstanceID)
}

type TechnicalCatalog struct {
	SchemaVersion int            `json:"schemaVersion"`
	Version       string         `json:"version"`
	ProductID     string         `json:"product_id"`
	InstanceID    string         `json:"instance_id"`
	Models        []CatalogModel `json:"models"`
}
type CatalogSourceModel struct {
	NativePresetTargets      map[string]bool
	SelectableChannelTypes   []int
	OperationDisabledReasons map[string]string
	dto.ModelCapabilities
	PublicID, DisplayName    string
	ContextWindow            int
	SupportsTools            bool
	SupportsReasoningSummary bool
	ReasoningEfforts         []string
}
type CatalogModel struct {
	DisabledReason           string                `json:"disabled_reason,omitempty"`
	ID                       string                `json:"id"`
	Name                     string                `json:"name"`
	Capabilities             dto.ModelCapabilities `json:"capabilities"`
	ContextWindow            int                   `json:"context_window,omitempty"`
	SupportsTools            bool                  `json:"supports_tools"`
	SupportsReasoningSummary bool                  `json:"supports_reasoning_summary"`
	ReasoningEfforts         []string              `json:"reasoning_efforts,omitempty"`
	Operations               []CatalogOperation    `json:"operations"`
}
type CatalogOperation struct {
	ParameterVariants []ParameterVariant `json:"parameter_variants,omitempty"`
	ID                string             `json:"id"`
	Protocol          string             `json:"protocol"`
	Endpoint          string             `json:"endpoint,omitempty"`
	Method            string             `json:"method,omitempty"`
	Transports        []string           `json:"transports"`
	BillingOperation  string             `json:"billing_operation,omitempty"`
	Available         bool               `json:"available"`
	Executable        bool               `json:"executable"`
	DisabledReason    string             `json:"disabled_reason,omitempty"`
	Policy            string             `json:"policy,omitempty"`
	Limits            OperationLimits    `json:"limits"`
	RateIDs           map[string]string  `json:"rate_ids,omitempty"`
}

// ProjectCatalog publishes only validated public metadata and configured
// measurement constraints. Native monetary prices are not sale tariffs.
func (a *Adapter) ProjectCatalog(product string, models []CatalogSourceModel) (TechnicalCatalog, error) {
	out := TechnicalCatalog{SchemaVersion: 1, ProductID: product, InstanceID: a.config.InstanceID, Models: []CatalogModel{}}
	for _, source := range models {
		m := CatalogModel{ID: source.PublicID, Name: source.DisplayName, Capabilities: source.ModelCapabilities, ContextWindow: source.ContextWindow, SupportsTools: source.SupportsTools, SupportsReasoningSummary: source.SupportsReasoningSummary, ReasoningEfforts: source.ReasoningEfforts, Operations: []CatalogOperation{}}
		// Public limits describe this gateway's effective contract, not only
		// the upstream declaration. Allocate every changed pointer independently.
		tokenOperations := false
		mediaFormatsUnavailable := false
		bodyBound := int64(0)
		for _, wire := range source.Operations {
			if format, _ := TokenProducer(wire, 0); format != "" && wire != "embeddings.create" && wire != "rerank.create" && wire != "multimodal.embeddings.create" {
				tokenOperations = true
				bound := a.config.MaxBodyBytes
				// A model-wide public byte ceiling must also be valid for plain
				// requests without a media certificate. Larger certified media
				// requests may be admitted, but are not promised by this ceiling.
				ordinaryBound := int64(a.config.MaxInputTokens / 2)
				if ordinaryBound > 0 && (bound == 0 || ordinaryBound < bound) {
					bound = ordinaryBound
				}
				contextBound := a.config.MaxInputTokens
				if wire == "responses.compact" {
					if p, ok := a.config.CompactPolicies[source.PublicID]; ok && p.valid(a.config, source.PublicID) {
						contextBound = p.HardInputTokens
					}
				}

				if policy, ok := a.config.NativeInputPolicies[wire][source.PublicID]; ok && policy.valid(a.config, wire) && policy.HardInputTokens < contextBound {
					contextBound = policy.HardInputTokens
				}
				if contextBound > 0 && (m.ContextWindow == 0 || contextBound < m.ContextWindow) {
					m.ContextWindow = contextBound
				}
				if bound > 0 && (bodyBound == 0 || bound < bodyBound) {
					bodyBound = bound
				}
			}
		}
		if tokenOperations && a.config.MaxOutputTokens > 0 {
			bound := a.config.MaxOutputTokens
			if source.MaxOutputTokens != nil && *source.MaxOutputTokens < bound {
				bound = *source.MaxOutputTokens
			}
			for _, wire := range source.Operations {
				if wire == "responses.compact" {
					if p, ok := a.config.CompactPolicies[source.PublicID]; ok && p.valid(a.config, source.PublicID) && p.HardOutputTokens < bound {
						bound = p.HardOutputTokens
					}
				}
			}
			m.Capabilities.MaxOutputTokens = &bound
		}
		if bodyBound > 0 {
			limits := dto.MediaLimits{}
			if source.MediaLimits != nil {
				limits = *source.MediaLimits
			}
			if limits.MaxRequestBytes != nil && *limits.MaxRequestBytes < bodyBound {
				bodyBound = *limits.MaxRequestBytes
			}
			limits.MaxRequestBytes = &bodyBound
			for _, wire := range source.Operations {
				if policy, ok := a.config.NativeInputPolicies[wire][source.PublicID]; ok && policy.valid(a.config, wire) {
					images := policy.MaxImages
					if limits.MaxImages != nil && *limits.MaxImages < images {
						images = *limits.MaxImages
					}
					limits.MaxImages = &images
					pages := policy.MaxPages
					if limits.MaxPages != nil && *limits.MaxPages < pages {
						pages = *limits.MaxPages
					}
					limits.MaxPages = &pages
					pixels := policy.MaxPixels
					if limits.MaxPixels != nil && *limits.MaxPixels < pixels {
						pixels = *limits.MaxPixels
					}
					limits.MaxPixels = &pixels
					if int64(policy.MaxBytes) < bodyBound {
						bodyBound = int64(policy.MaxBytes)
					}
					supported := []string{"image/png", "image/jpeg", "application/pdf", "text/plain"}
					if source.MediaLimits != nil && len(source.MediaLimits.Formats) > 0 {
						allowed := []string{}
						for _, format := range supported {
							if mediafacts.AllowsFormat(source.MediaLimits.Formats, format) {
								allowed = append(allowed, format)
							}
						}
						supported = allowed
					}
					limits.Formats = supported
					mediaFormatsUnavailable = len(supported) == 0
				}
			}
			m.Capabilities.MediaLimits = &limits
		}
		if len(source.Operations) == 0 {
			m.DisabledReason = "capabilities_unconfigured"
		}
		for _, id := range source.Operations {
			op := CatalogOperation{ID: id, Protocol: "openai", Transports: []string{"json"}, Method: "POST"}
			switch id {
			case "chat.completions.create":
				op.Endpoint = "/v1/chat/completions"
				op.BillingOperation = "chat"
			case "responses.compact":
				op.Endpoint = "/v1/responses/compact"
				op.BillingOperation = "responses"
			case "responses.create":
				op.Endpoint = "/v1/responses"
				op.BillingOperation = "responses"
			case "rerank.create":
				op.Endpoint = "/v1/rerank"
				op.BillingOperation = "rerank"
			case "multimodal.embeddings.create":
				op.Endpoint = "/v1/multimodal/embeddings"
				op.BillingOperation = "multimodal_embedding"
			case "embeddings.create":
				op.Endpoint = "/v1/embeddings"
				op.BillingOperation = "embedding"
			case "gemini.images.generate":
				op.Endpoint = "/v1/native/gemini/images/models/" + source.PublicID + ":generateContent"
				op.BillingOperation = "image_generation"
				op.Protocol = "gemini"
			case "dashscope.images.generate":
				op.Endpoint = "/v1/native/dashscope/images/generations"
				op.BillingOperation = "image_generation"
				op.Protocol = "dashscope"
			case "images.generations.create":
				op.Endpoint = "/v1/images/generations"
				op.BillingOperation = "image_generation"
			case "images.edits.create":
				op.Endpoint = "/v1/images/edits"
				op.BillingOperation = "image_edit"
			case "bailian.audio.speech.create", "elevenlabs.audio.speech.create":
				op.Protocol = "bailian"
				if id == "elevenlabs.audio.speech.create" {
					op.Protocol = "elevenlabs"
				}
				op.Endpoint = "/v1/native/" + op.Protocol + "/audio/speech"
				op.BillingOperation = "speech"
				op.Transports = []string{"binary"}
			case "audio.speech.create":
				op.Endpoint = "/v1/audio/speech"
				op.BillingOperation = "speech"
				op.Transports = []string{"binary"}
			case "audio.transcriptions.create":
				op.Endpoint = "/v1/audio/transcriptions"
				op.BillingOperation = "transcription"
			case "dashscope.audio.transcriptions.create":
				op.Endpoint = "/v1/native/dashscope/audio/transcriptions"
				op.BillingOperation = "transcription"
				op.Protocol = "dashscope"
			case "videos.create":
				op.Endpoint = "/v1/videos"
				op.BillingOperation = "video"
				op.Transports = []string{"async_job"}
			case "ocr.jobs.retrieve", "ocr.resources.retrieve":
				op.Protocol = "paddleocr_jobs"
			case "dashscope.ocr.recognitions.create":
				op.Endpoint = "/v1/native/dashscope/ocr/recognitions"
				op.BillingOperation = "ocr"
				op.Protocol = "dashscope"
			case "ocr.jobs.create":
				op.Endpoint = "/v1/ocr"
				op.BillingOperation = "ocr"
				op.Protocol = "paddleocr_jobs"
				op.Transports = []string{"async_job"}
			case "claude.messages.create":
				op.Protocol = "claude"
				op.Endpoint = "/v1/messages"
				op.BillingOperation = "chat"
			case "midjourney.submit", "midjourney.fetch", "suno.submit", "suno.fetch":
				op.Protocol = "task"
			case "gemini.generate":
				op.Protocol = "gemini"
				op.Endpoint = "/v1beta/models/" + source.PublicID + ":generateContent"
				op.BillingOperation = "chat"
			case "gemini.embed":
				op.Protocol = "gemini"
			}
			if id != "responses.compact" && source.Streaming != nil && *source.Streaming && (op.BillingOperation == "chat" || op.BillingOperation == "responses") {
				op.Transports = append(op.Transports, "sse")
			}
			op.Limits = a.config.OperationLimits[op.BillingOperation][m.ID]
			op.Policy = op.Limits.Policy
			// A valid receipt policy is not proof that the selected provider
			// emits that receipt. Bind all selectable producers, fail closed
			// for mixed providers and currently unmeasured video adapters.
			videoProducerSupported := len(source.SelectableChannelTypes) > 0
			if op.BillingOperation == "video" {
				for _, channelType := range source.SelectableChannelTypes {
					supported := false
					switch channelType {
					case constant.ChannelTypeSora, constant.ChannelTypeOpenAI:
						supported = op.Policy == "sora_completed_seconds_v1"
					case constant.ChannelTypeAli:
						supported = op.Policy == "ali_wan26_t2v_billable_duration_v1"
					case constant.ChannelTypeKling:
						supported = op.Policy == "kling_final_units_v1"
					case constant.ChannelTypeDoubaoVideo, constant.ChannelTypeVolcEngine:
						supported = op.Policy == "doubao_completion_tokens_v1"
					}
					videoProducerSupported = videoProducerSupported && supported
				}
			}
			for asset, pins := range a.config.OperationRateIDs[op.BillingOperation] {
				if pin := pins[m.ID]; pin != "" {
					if op.RateIDs == nil {
						op.RateIDs = map[string]string{}
					}
					op.RateIDs[asset] = pin
				}
			}
			if id == "responses.compact" {
				p := a.config.CompactPolicies[m.ID]
				op.RateIDs = map[string]string{}
				for asset, pin := range p.RateIDs {
					op.RateIDs[asset] = pin
				}
			}
			if id == "ocr.jobs.create" || id == "dashscope.ocr.recognitions.create" {
				op.RateIDs = map[string]string{}
				for asset, rate := range a.config.OCRPolicies[id][m.ID].RateIDs {
					op.RateIDs[asset] = rate
				}
			}
			if IsImageWire(id) {
				p := a.config.ImagePolicies[id][m.ID]
				op.Limits.MaxBodyBytes = p.MaxBodyBytes
				op.Limits.MaxQuantity = p.MaxImages
				op.RateIDs = p.RateIDs
			}
			if IsAudioWire(id) {
				p := a.config.AudioPolicies[id][m.ID]
				if op.Limits.MaxBodyBytes == 0 {
					op.Limits.MaxBodyBytes = a.config.MaxBodyBytes
				}
				if p.ChannelType == constant.ChannelTypeMiniMax && op.Limits.MaxBodyBytes > dto.MaxMiniMaxSpeechBodyBytes {
					op.Limits.MaxBodyBytes = dto.MaxMiniMaxSpeechBodyBytes
				}
				if id == "audio.transcriptions.create" && op.Limits.MaxBodyBytes > 10<<20 {
					op.Limits.MaxBodyBytes = 10 << 20
				}
				op.Limits.MaxQuantity = p.MaxQuantity
				op.RateIDs = map[string]string{}
				for asset, rate := range p.RateIDs {
					op.RateIDs[asset] = rate
				}
			}
			knowledge := id == "embeddings.create" || id == "rerank.create" || id == "multimodal.embeddings.create"
			knowledgeBoundUnavailable := false
			if knowledge {
				p := a.config.KnowledgePolicies[id][m.ID]
				op.Limits.MaxBodyBytes = a.config.MaxBodyBytes
				quantity := int64(p.HardInputTokens) + int64(p.VisualTokens)
				knowledgeBoundUnavailable = quantity < 1 || quantity > 86400
				if !knowledgeBoundUnavailable {
					op.Limits.MaxQuantity = int(quantity)
				}
				op.RateIDs = map[string]string{}
				for asset, pin := range p.RateIDs {
					op.RateIDs[asset] = pin
				}
			}
			switch {
			case IsImageWire(id) && (!a.config.ImagePolicies[id][m.ID].valid(m.ID, id) || op.Policy != "successful_outputs_v1"):
				op.DisabledReason = "certified_image_unavailable"
			case (id == "ocr.jobs.create" || id == "dashscope.ocr.recognitions.create") && (!a.config.OCRPolicies[id][m.ID].valid(m.ID, id) || op.Policy != a.config.OCRPolicies[id][m.ID].UsagePolicy):
				op.DisabledReason = "certified_ocr_unavailable"
			case IsAudioWire(id) && (!a.config.AudioPolicies[id][m.ID].valid(m.ID, id) || op.Policy != a.config.AudioPolicies[id][m.ID].UsagePolicy):
				op.DisabledReason = "certified_audio_unavailable"
			case id == "responses.compact" && ((source.MaxOutputTokens != nil && a.config.CompactPolicies[m.ID].HardOutputTokens > *source.MaxOutputTokens) || (source.ContextWindow > 0 && a.config.CompactPolicies[m.ID].HardInputTokens > source.ContextWindow)):
				op.DisabledReason = "certified_compact_bounds_incompatible"
			case id == "responses.compact" && !a.config.CompactPolicies[m.ID].valid(a.config, m.ID):
				op.DisabledReason = "certified_compact_unavailable"
			case id == "gemini.generate" && (strings.ContainsAny(m.ID, "/:?#%") || strings.TrimSpace(m.ID) != m.ID):
				op.DisabledReason = "invalid_native_path_model"
			case op.BillingOperation == "":
				op.DisabledReason = "managed_adapter_unavailable"
				op.Method = ""
				op.Transports = []string{}
			case !a.Enabled():
				op.DisabledReason = "managed_gateway_unavailable"
			case source.OperationDisabledReasons[id] != "":
				op.DisabledReason = source.OperationDisabledReasons[id]
			case mediaFormatsUnavailable:
				op.DisabledReason = "managed_media_formats_unavailable"
			case op.Policy == "":
				op.DisabledReason = "usage_policy_unconfigured"
			case op.BillingOperation == "video" && !videoProducerSupported:
				op.DisabledReason = "managed_producer_policy_mismatch"
			case op.BillingOperation == "embedding" && source.EmbeddingSpace == nil:
				op.DisabledReason = "embedding_identity_unconfigured"
			case (id == "embeddings.create" || id == "rerank.create" || id == "multimodal.embeddings.create") && !a.config.KnowledgePolicies[id][m.ID].valid(a.config, m.ID, id):
				op.DisabledReason = "certified_knowledge_unavailable"
			case (id == "embeddings.create" || id == "rerank.create" || id == "multimodal.embeddings.create") && op.Policy != "provider_reported_v1":
				op.DisabledReason = "managed_producer_policy_mismatch"
			case knowledge && knowledgeBoundUnavailable:
				op.DisabledReason = "certified_public_bounds_unavailable"
			case op.BillingOperation == "chat" && len(a.config.OperationRateIDs["chat"]) == 0:
				op.DisabledReason = "metered_admission_unconfigured"
			default:
				op.Available = true
				op.DisabledReason = "account_authorization_required"
			}
			var presetErr error
			op.ParameterVariants, presetErr = a.projectNativePresets(source, m, op)
			if presetErr != nil {
				return TechnicalCatalog{}, presetErr
			}
			m.Operations = append(m.Operations, op)
		}
		slices.SortFunc(m.Operations, func(a, b CatalogOperation) int { return strings.Compare(a.ID, b.ID) })
		out.Models = append(out.Models, m)
	}
	slices.SortFunc(out.Models, func(a, b CatalogModel) int { return strings.Compare(a.ID, b.ID) })
	data, e := common.Marshal(out)
	if e != nil {
		return TechnicalCatalog{}, e
	}
	sum := sha256.Sum256(data)
	out.Version = hex.EncodeToString(sum[:])
	return out, nil
}

func CatalogHandler(config CatalogConfig, read func(context.Context) (TechnicalCatalog, error)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if !config.Valid() || read == nil {
			http.Error(w, "catalog unavailable", 503)
			return
		}
		auth := r.Header.Values("Authorization")
		want := "Bearer " + config.Token
		if len(auth) != 1 || subtle.ConstantTimeCompare([]byte(auth[0]), []byte(want)) != 1 {
			http.Error(w, "unauthorized", 401)
			return
		}
		q, err := url.ParseQuery(r.URL.RawQuery)
		if err != nil || r.Method != "GET" || r.URL.Path != "/internal/v1/model-catalog" || len(q) != 2 || len(q["product_id"]) != 1 || len(q["instance_id"]) != 1 || q.Get("product_id") != config.ProductID || q.Get("instance_id") != config.InstanceID {
			http.Error(w, "forbidden", 403)
			return
		}
		out, e := read(r.Context())
		if e != nil || out.ProductID != config.ProductID || out.InstanceID != config.InstanceID {
			http.Error(w, "catalog unavailable", 503)
			return
		}
		data, e := common.Marshal(out)
		if e != nil {
			http.Error(w, "catalog unavailable", 503)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		_, _ = w.Write(data)
	})
}
