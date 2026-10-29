package platformbilling

import (
	"encoding/base64"
	"github.com/QuantumNous/new-api/common"
	"strconv"
	"strings"
)

// ConfigFromEnv takes an injected environment reader. No credentials or service
// clients are process globals; enabling a deployment requires explicit limits.
func ConfigFromEnv(get func(string) string) (Config, error) {
	c := Config{Enabled: get("UNIHUB_PLATFORM_ENABLED") == "true"}
	c.InstanceID = strings.TrimSpace(get("UNIHUB_PRODUCT_INSTANCE_ID"))
	if c.InstanceID == "" {
		c.InstanceID = "default"
	}
	if !c.Enabled {
		return c, nil
	}
	for _, name := range []string{"UNIHUB_ALI_EMBEDDING_ENABLED", "UNIHUB_ALI_RERANK_ENABLED"} {
		if raw := get(name); raw != "" && raw != "false" && raw != "true" {
			return c, ErrInvalid
		}
	}
	c.AllowAliEmbeddingV4 = get("UNIHUB_ALI_EMBEDDING_ENABLED") == "true"
	c.AllowAliRerank = get("UNIHUB_ALI_RERANK_ENABLED") == "true"
	if c.AllowAliEmbeddingV4 || c.AllowAliRerank {
		var keys map[string]string
		if common.UnmarshalUnique([]byte(get("UNIHUB_KNOWLEDGE_RESULT_KEYS")), &keys) != nil || len(keys) == 0 || len(keys) > 32 {
			return c, ErrInvalid
		}
		c.AliEmbeddingResultKeys = make(map[string][]byte, len(keys))
		seen := make(map[string]bool, len(keys))
		for id, encoded := range keys {
			key, err := base64.StdEncoding.Strict().DecodeString(encoded)
			if !knowledgeKeyID.MatchString(id) || err != nil || len(key) != 32 || seen[string(key)] || base64.StdEncoding.EncodeToString(key) != encoded {
				return c, ErrInvalid
			}
			seen[string(key)] = true
			c.AliEmbeddingResultKeys[id] = key
		}
		c.AliEmbeddingResultActiveKey = get("UNIHUB_KNOWLEDGE_RESULT_ACTIVE_KEY")
		if _, ok := c.AliEmbeddingResultKeys[c.AliEmbeddingResultActiveKey]; !ok {
			return c, ErrInvalid
		}
	}
	if raw := get("UNIHUB_NATIVE_HISTORY"); raw != "" {
		if common.UnmarshalUnique([]byte(raw), &c.NativeHistory) != nil {
			return c, ErrInvalid
		}
	}
	if raw := get("UNIHUB_IMAGE_POLICIES"); raw != "" {
		var values map[string]map[string]map[string]any
		if common.UnmarshalUnique([]byte(raw), &values) != nil {
			return c, ErrInvalid
		}
		for _, models := range values {
			for _, p := range models {
				if !protocolKeys(p, "version public_model wire_operation channel_id channel_type upstream_model channel_key_sha256 origin api_version max_body_bytes max_image_bytes max_pixels max_images max_input_images resource_origins rate_ids") {
					return c, ErrInvalid
				}
			}
		}
		if common.UnmarshalUnique([]byte(raw), &c.ImagePolicies) != nil {
			return c, ErrInvalid
		}
		for wire, models := range c.ImagePolicies {
			for model, p := range models {
				if !p.valid(model, wire) {
					return c, ErrInvalid
				}
			}
		}
	}
	if raw := get("UNIHUB_OCR_POLICIES"); raw != "" {
		if common.UnmarshalJsonStr(raw, &c.OCRPolicies) != nil {
			return c, ErrInvalid
		}
	}
	c.Origin = get("UNIHUB_ORIGIN")
	c.ServiceToken = get("UNIHUB_MODEL_SERVICE_TOKEN")
	c.Group = get("UNIHUB_ROUTING_GROUP")
	var err error
	c.PrincipalID, err = strconv.Atoi(get("UNIHUB_ROUTING_PRINCIPAL_ID"))
	if err != nil || c.PrincipalID < 1 {
		return c, ErrInvalid
	}
	c.MaxInputTokens, err = strconv.Atoi(get("UNIHUB_MAX_INPUT_TOKENS"))
	if err != nil {
		return c, ErrInvalid
	}
	c.MaxOutputTokens, err = strconv.Atoi(get("UNIHUB_MAX_OUTPUT_TOKENS"))
	if err != nil {
		return c, ErrInvalid
	}
	c.MaxBodyBytes, err = strconv.ParseInt(get("UNIHUB_MAX_BODY_BYTES"), 10, 64)
	if err != nil {
		return c, ErrInvalid
	}
	if raw := get("UNIHUB_MODEL_RATE_IDS"); raw != "" {
		if common.UnmarshalJsonStr(raw, &c.RateIDs) != nil {
			return c, ErrInvalid
		}
	}
	// Legacy model pins select CREDIT rates only. Explicit asset pins take
	// precedence, for example {"CNY/6":{"model-name":"cash-rate-id"}}.
	if raw := get("UNIHUB_MODEL_ASSET_RATE_IDS"); raw != "" {
		if common.UnmarshalJsonStr(raw, &c.AssetRateIDs) != nil {
			return c, ErrInvalid
		}
		for asset := range c.AssetRateIDs {
			if asset != "CREDIT/6" && asset != "CNY/6" {
				return c, ErrInvalid
			}
		}
	}
	// Explicit operation pins opt requests into metered_v2. Duration and media
	// caps are deployment-owned bounds, never values trusted from client JSON.
	if raw := get("UNIHUB_OPERATION_RATE_IDS"); raw != "" {
		if common.UnmarshalJsonStr(raw, &c.OperationRateIDs) != nil {
			return c, ErrInvalid
		}
	}
	if raw := get("UNIHUB_OPERATION_LIMITS"); raw != "" {
		if common.UnmarshalJsonStr(raw, &c.OperationLimits) != nil {
			return c, ErrInvalid
		}
		for _, models := range c.OperationLimits {
			for _, limit := range models {
				if limit.MaxBodyBytes < 0 || limit.MaxBodyBytes > 64<<20 || limit.MaxQuantity < 0 || limit.MaxQuantity > 86400 {
					return c, ErrInvalid
				}
			}
		}
	}
	if raw := get("UNIHUB_AUDIO_POLICIES"); raw != "" {
		var policies map[string]map[string]map[string]any
		if common.UnmarshalUnique([]byte(raw), &policies) != nil {
			return c, ErrInvalid
		}
		for _, models := range policies {
			for _, p := range models {
				if !protocolKeys(p, "version public_model wire_operation channel_id channel_type upstream_model channel_key_sha256 origin usage_policy max_quantity rate_ids") {
					return c, ErrInvalid
				}
			}
		}
		if common.UnmarshalUnique([]byte(raw), &c.AudioPolicies) != nil {
			return c, ErrInvalid
		}
		for wire, models := range c.AudioPolicies {
			for model, p := range models {
				if !p.valid(model, wire) {
					return c, ErrInvalid
				}
			}
		}
	}
	if raw := get("UNIHUB_VIDEO_POLICIES"); raw != "" {
		var policies map[string]map[string]map[string]any
		if common.UnmarshalUnique([]byte(raw), &policies) != nil {
			return c, ErrInvalid
		}
		for _, models := range policies {
			for _, p := range models {
				if !protocolKeys(p, "version public_model wire_operation channel_id channel_type upstream_model channel_key_sha256 origin api_version usage_policy max_seconds max_video_bytes resolutions resource_origins rate_ids") {
					return c, ErrInvalid
				}
			}
		}
		if common.UnmarshalUnique([]byte(raw), &c.VideoPolicies) != nil {
			return c, ErrInvalid
		}
		for wire, models := range c.VideoPolicies {
			for model, p := range models {
				if !p.valid(model, wire) {
					return c, ErrInvalid
				}
			}
		}
	}
	if raw := get("UNIHUB_KNOWLEDGE_POLICIES"); raw != "" {
		var policies map[string]map[string]map[string]any
		if common.UnmarshalUnique([]byte(raw), &policies) != nil {
			return c, ErrInvalid
		}
		for _, models := range policies {
			for _, p := range models {
				if !protocolKeys(p, "channel_key_sha256 version provider_reference public_model wire_operation channel_id channel_type origin upstream_model hard_input_tokens visual_tokens max_item_tokens max_batch embedding_space rate_ids") {
					return c, ErrInvalid
				}
				if raw, exists := p["embedding_space"]; exists {
					space, ok := raw.(map[string]any)
					if !ok || !protocolKeys(space, "provider model version dimensions input_semantics") {
						return c, ErrInvalid
					}
				}
			}
		}
		if common.UnmarshalUnique([]byte(raw), &c.KnowledgePolicies) != nil {
			return c, ErrInvalid
		}
		for wire, models := range c.KnowledgePolicies {
			for model, p := range models {
				if !p.valid(c, model, wire) {
					return c, ErrInvalid
				}
			}
		}
	}
	if raw := get("UNIHUB_NATIVE_INPUT_POLICIES"); raw != "" {
		if common.UnmarshalUnique([]byte(raw), &c.NativeInputPolicies) != nil {
			return c, ErrInvalid
		}
		for wire, models := range c.NativeInputPolicies {
			for _, policy := range models {
				if !policy.valid(c, wire) {
					return c, ErrInvalid
				}
			}
		}
	}
	if raw := get("UNIHUB_COMPACT_POLICIES"); raw != "" {
		var policies map[string]map[string]any
		if common.UnmarshalUnique([]byte(raw), &policies) != nil {
			return c, ErrInvalid
		}
		for _, p := range policies {
			if !protocolKeys(p, "version provider_reference public_model channel_id channel_type upstream_model wire_operation endpoint_path hard_input_tokens hard_output_tokens service_tier rate_ids") {
				return c, ErrInvalid
			}
		}
		if common.UnmarshalUnique([]byte(raw), &c.CompactPolicies) != nil {
			return c, ErrInvalid
		}
		for model, p := range c.CompactPolicies {
			if !p.valid(c, model) {
				return c, ErrInvalid
			}
		}
	}
	if raw := get("UNIHUB_NATIVE_PRESET_POLICIES"); raw != "" {
		if decodeNativePresetPolicies([]byte(raw), &c) != nil {
			return c, ErrInvalid
		}
	}
	return c, nil
}
