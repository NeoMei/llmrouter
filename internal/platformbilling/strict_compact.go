package platformbilling

import (
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
)

// CompactPolicy is a trusted deployment certificate for one exact native compact
// target. Its hard input ceiling covers all accepted provider-side history, and
// its hard output ceiling covers the entire compact pass, including reasoning.
// Neither ceiling comes from client JSON or a model-name guess. The caller must
// verify the selected channel/upstream and published terms before admission.
// Defining this contract does not certify any live provider or enable a route.
type CompactPolicy struct {
	ChannelID         int               `json:"channel_id,omitempty"`
	RateIDs           map[string]string `json:"rate_ids,omitempty"`
	Version           string            `json:"version"`
	ProviderReference string            `json:"provider_reference"`
	PublicModel       string            `json:"public_model"`
	ChannelType       int               `json:"channel_type"`
	UpstreamModel     string            `json:"upstream_model"`
	WireOperation     string            `json:"wire_operation"`
	EndpointPath      string            `json:"endpoint_path"`
	HardInputTokens   int               `json:"hard_input_tokens"`
	HardOutputTokens  int               `json:"hard_output_tokens"`
	// ServiceTier is the request's exact effective tier (absent/null means auto).
	// A different requested tier needs a separately selected certificate and terms.
	ServiceTier string `json:"service_tier"`
}

// nativeCompactRequest deliberately excludes create-only and compatibility DTO
// fields. Optional nulls are accepted by this typed envelope, while canonical
// bytes are always produced from the original unique JSON map to retain them.
type nativeCompactRequest struct {
	Model              string  `json:"model"`
	Input              any     `json:"input"`
	Instructions       *string `json:"instructions"`
	PreviousResponseID *string `json:"previous_response_id"`
	PromptCacheKey     *string `json:"prompt_cache_key"`
	PromptCacheOptions *struct {
		Mode *string `json:"mode"`
		TTL  *string `json:"ttl"`
	} `json:"prompt_cache_options"`
	PromptCacheRetention *string `json:"prompt_cache_retention"`
	ServiceTier          *string `json:"service_tier"`
}

// parseCompactOperation is pure admission preparation. It never probes a
// provider, reserves funds, or adds an unsupported output-cap field to the wire.
func parseCompactOperation(c Config, policy CompactPolicy, body []byte) (operationRequest, error) {
	invalid := operationRequest{}
	if !utf8.Valid(body) || c.MaxBodyBytes < 1 || int64(len(body)) > c.MaxBodyBytes || policy.HardInputTokens < 1 || policy.HardInputTokens > c.MaxInputTokens || policy.HardOutputTokens < 1 || policy.HardOutputTokens > c.MaxOutputTokens {
		return invalid, ErrInvalid
	}
	reference, err := url.Parse(policy.ProviderReference)
	if err != nil || reference.Scheme != "https" || reference.Hostname() == "" || reference.User != nil || policy.Version != "hard_context_v1" || !protocolString(policy.PublicModel) || len(policy.PublicModel) > 255 || policy.ChannelType < 1 || !protocolString(policy.UpstreamModel) || policy.WireOperation != "responses.compact" || policy.EndpointPath != "/v1/responses/compact" || !protocolEnum(map[string]any{"tier": policy.ServiceTier}, "tier", "auto default fast flex priority") {
		return invalid, ErrInvalid
	}
	var request map[string]any
	if common.UnmarshalUnique(body, &request) != nil || !protocolKeys(request, "model input instructions previous_response_id prompt_cache_key prompt_cache_options prompt_cache_retention service_tier") {
		return invalid, ErrInvalid
	}
	var decoded nativeCompactRequest
	if common.Unmarshal(body, &decoded) != nil || decoded.Model != policy.PublicModel || strings.TrimSpace(decoded.Model) == "" {
		return invalid, ErrInvalid
	}
	if decoded.PreviousResponseID != nil && !protocolString(*decoded.PreviousResponseID) {
		return invalid, ErrInvalid
	}
	if decoded.PromptCacheKey != nil && utf8.RuneCountInString(*decoded.PromptCacheKey) > 64 {
		return invalid, ErrInvalid
	}
	if value := request["prompt_cache_options"]; value != nil {
		options, ok := value.(map[string]any)
		if !ok || !protocolKeys(options, "mode ttl") || !protocolEnum(options, "mode", "implicit explicit") || !protocolEnum(options, "ttl", "30m") {
			return invalid, ErrInvalid
		}
	}
	if value := request["prompt_cache_retention"]; value != nil && !protocolEnum(request, "prompt_cache_retention", "in_memory 24h") {
		return invalid, ErrInvalid
	}
	tier := "auto"
	if decoded.ServiceTier != nil {
		tier = *decoded.ServiceTier
	}
	if tier != policy.ServiceTier {
		return invalid, ErrInvalid
	}
	if decoded.Input == nil {
		if decoded.PreviousResponseID == nil {
			return invalid, ErrInvalid
		}
	} else if items, ok := decoded.Input.([]any); ok {
		if len(items) == 0 && decoded.PreviousResponseID == nil {
			return invalid, ErrInvalid
		}
		for _, value := range items {
			item, ok := value.(map[string]any)
			if !ok {
				return invalid, ErrInvalid
			}
			if item["type"] == "compaction" {
				// This is compact's only added history branch. All existing Responses
				// message/tool/reasoning unions continue to use their shared validator.
				if !protocolKeys(item, "type id encrypted_content") || !protocolString(item["encrypted_content"]) {
					return invalid, ErrInvalid
				}
				if id := item["id"]; id != nil {
					if _, ok := id.(string); !ok {
						return invalid, ErrInvalid
					}
				}
			} else if !responsesInput([]any{item}) {
				return invalid, ErrInvalid
			}
		}
	} else if !responsesInput(decoded.Input) {
		return invalid, ErrInvalid
	}
	frozen := frozenMediaReservation{CompactPolicy: &policy}
	carriers, err := nativeContentMedia("responses.compact", request)
	if err != nil {
		return invalid, err
	}
	if len(carriers) > 0 {
		_, media, err := nativeMediaReservation(c, "responses.compact", decoded.Model, request)
		if err != nil {
			return invalid, err
		}
		if common.Unmarshal([]byte(media), &frozen) != nil || frozen.Policy.ChannelType != policy.ChannelType || frozen.Policy.UpstreamModel != policy.UpstreamModel || frozen.Policy.HardInputTokens != policy.HardInputTokens {
			return invalid, ErrInvalid
		}
		frozen.CompactPolicy = &policy
	}
	canonical, err := common.Marshal(request)
	if err != nil {
		return invalid, ErrInvalid
	}
	snapshot, err := common.Marshal(frozen)
	if err != nil {
		return invalid, ErrInvalid
	}
	return operationRequest{Model: decoded.Model, Canonical: canonical, Quantity: policy.HardInputTokens, Output: policy.HardOutputTokens, MediaSnapshot: string(snapshot)}, nil
}

// CompactTarget requires an exact configured producer certificate, independently
// of model metadata or the presence of a responses tariff.
func (a *Adapter) CompactTarget(model string, channelID, channel int, upstream string, contextWindow int) bool {
	p, ok := a.config.CompactPolicies[model]
	_, proven := TokenProducer("responses.compact", channel)
	return ok && proven && (contextWindow <= 0 || p.HardInputTokens <= contextWindow) && p.PublicModel == model && p.ChannelID == channelID && p.ChannelType == channel && p.UpstreamModel == upstream
}

func (p CompactPolicy) valid(c Config, model string) bool {
	raw, err := common.Marshal(map[string]any{"model": model, "input": "certificate validation", "service_tier": p.ServiceTier})
	if err != nil {
		return false
	}
	_, err = parseCompactOperation(c, p, raw)
	_, proven := TokenProducer("responses.compact", p.ChannelType)
	if err != nil || !proven || p.ChannelID < 1 || len(p.RateIDs) == 0 {
		return false
	}
	for asset, id := range p.RateIDs {
		if (asset != "CREDIT/6" && asset != "CNY/6") || !stableID.MatchString(id) {
			return false
		}
	}
	return true
}
