package platformbilling

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/url"
	"reflect"
	"regexp"
	"slices"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
)

var nativePresetID = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)

// NativePresetPolicy is operator-only evidence for one exact upstream target.
// Rotating keys, multi-key selection and alternate targets require recertification.
type NativePresetPolicy struct {
	Version          string         `json:"version"`
	PublicModel      string         `json:"public_model"`
	WireOperation    string         `json:"wire_operation"`
	ChannelID        int            `json:"channel_id"`
	ChannelType      int            `json:"channel_type"`
	UpstreamModel    string         `json:"upstream_model"`
	ChannelKeySHA256 string         `json:"channel_key_sha256"`
	Origin           string         `json:"origin"`
	APIVersion       string         `json:"api_version"`
	SupportsAdaptive bool           `json:"supports_adaptive"`
	Variants         []NativePreset `json:"variants"`
}
type NativePreset struct {
	ID         string          `json:"id"`
	Label      string          `json:"label"`
	Parameters json.RawMessage `json:"parameters"`
}
type ParameterVariant struct {
	NativePreset
	Version           string `json:"version"`
	CertificateSHA256 string `json:"certificate_sha256"`
	APIVersion        string `json:"api_version"`
	SupportsAdaptive  bool   `json:"supports_adaptive"`
	DescriptorSHA256  string `json:"descriptor_sha256"`
}

func (p NativePresetPolicy) valid(c Config, model, wire string) bool {
	if p.PublicModel != model || p.WireOperation != wire || model == "" || len(model) > 255 || p.Version == "" || len(p.Version) > 128 || p.ChannelID < 1 || p.UpstreamModel == "" || len(p.ChannelKeySHA256) != 64 || len(p.Variants) > 32 {
		return false
	}
	digest, err := hex.DecodeString(p.ChannelKeySHA256)
	if err != nil || len(digest) != 32 || strings.ToLower(p.ChannelKeySHA256) != p.ChannelKeySHA256 {
		return false
	}
	u, err := url.Parse(p.Origin)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" || (u.Scheme != "https" && u.Scheme != "http") {
		return false
	}
	switch wire {
	case "chat.completions.create", "responses.create":
		if p.ChannelType != constant.ChannelTypeOpenAI || p.APIVersion != "v1" || p.SupportsAdaptive {
			return false
		}
	case "claude.messages.create":
		if p.ChannelType != constant.ChannelTypeAnthropic || p.APIVersion != "2023-06-01" {
			return false
		}
	case "gemini.generate":
		if p.ChannelType != constant.ChannelTypeGemini || p.APIVersion != "v1beta" || p.SupportsAdaptive {
			return false
		}
	default:
		return false
	}
	seen := map[string]bool{}
	for _, v := range p.Variants {
		if !nativePresetID.MatchString(v.ID) || seen[v.ID] || strings.TrimSpace(v.Label) == "" || len(v.Label) > 256 || validateNativePreset(c, model, wire, v.Parameters, p.SupportsAdaptive) != nil {
			return false
		}
		seen[v.ID] = true
	}
	return true
}

// validateNativePreset uses both a closed preset grammar and the same request
// parser used by admission. It never accepts arbitrary request overlays.
func validateNativePreset(c Config, model, wire string, raw []byte, adaptive bool) error {
	var params map[string]any
	if common.UnmarshalUnique(raw, &params) != nil || params == nil {
		return ErrInvalid
	}
	request := map[string]any{"model": model}
	var err error
	switch wire {
	case "chat.completions.create":
		if len(params) != 1 || !protocolKeys(params, "reasoning_effort") || !protocolString(params["reasoning_effort"]) || !protocolEnum(params, "reasoning_effort", "none minimal low medium high xhigh") {
			return ErrInvalid
		}
		request["messages"] = []any{map[string]any{"role": "user", "content": "x"}}
		request["max_tokens"] = c.MaxOutputTokens
		request["reasoning_effort"] = params["reasoning_effort"]
	case "responses.create":
		r, ok := params["reasoning"].(map[string]any)
		if len(params) != 1 || !ok || len(r) != 1 || !protocolKeys(r, "effort") || !protocolString(r["effort"]) || !protocolEnum(r, "effort", "none minimal low medium high xhigh") {
			return ErrInvalid
		}
		request["input"] = "x"
		request["max_output_tokens"] = c.MaxOutputTokens
		request["reasoning"] = r
	case "claude.messages.create":
		thinking, ok := params["thinking"].(map[string]any)
		if len(params) != 2 || !protocolKeys(params, "thinking max_tokens") || !ok || !protocolKeys(thinking, "type budget_tokens") || thinking["type"] == "adaptive" && !adaptive {
			return ErrInvalid
		}
		request["messages"] = []any{map[string]any{"role": "user", "content": "x"}}
		request["max_tokens"] = params["max_tokens"]
		request["thinking"] = thinking
	case "gemini.generate":
		generation, ok := params["generationConfig"].(map[string]any)
		if len(params) != 1 || !ok || len(generation) != 1 {
			return ErrInvalid
		}
		thinking, ok := generation["thinkingConfig"].(map[string]any)
		if !ok || !protocolKeys(thinking, "thinkingBudget thinkingLevel includeThoughts") || len(thinking) == 0 {
			return ErrInvalid
		}
		if budget, present := thinking["thinkingBudget"]; present {
			if number, ok := budget.(json.Number); !ok || string(number) == "-0" {
				return ErrInvalid
			}
		}
		if _, b := thinking["thinkingBudget"]; !b {
			if _, l := thinking["thinkingLevel"]; !l {
				return ErrInvalid
			}
		}
		delete(request, "model")
		request["contents"] = []any{map[string]any{"role": "user", "parts": []any{map[string]any{"text": "x"}}}}
		request["generationConfig"] = map[string]any{"thinkingConfig": thinking, "maxOutputTokens": c.MaxOutputTokens}
	default:
		return ErrInvalid
	}
	body, e := common.Marshal(request)
	if e != nil {
		return e
	}
	switch wire {
	case "chat.completions.create":
		_, err = parseOperation(c, "chat", "application/json", body)
	case "responses.create":
		_, err = parseOperation(c, "responses", "application/json", body)
	case "claude.messages.create":
		_, err = parseClaudeOperation(c, body)
	case "gemini.generate":
		_, err = parseGeminiOperation(c, model, body)
	}
	return err
}

func decodeNativePresetPolicies(raw []byte, c *Config) error {
	var value any
	if common.UnmarshalUnique(raw, &value) != nil || value == nil {
		return ErrInvalid
	}
	// Map layers carry model IDs; struct layers must retain exact case and keys.
	wires, ok := value.(map[string]any)
	if !ok {
		return ErrInvalid
	}
	for _, modelsValue := range wires {
		models, ok := modelsValue.(map[string]any)
		if !ok {
			return ErrInvalid
		}
		for _, p := range models {
			fields, ok := p.(map[string]any)
			if !ok {
				return ErrInvalid
			}
			for key, value := range fields {
				if value == nil {
					return ErrInvalid
				}
				if key == "supports_adaptive" {
					if _, ok := value.(bool); !ok {
						return ErrInvalid
					}
				}
			}
			if validateDTOKeys(p, reflect.TypeOf(NativePresetPolicy{})) != nil {
				return ErrInvalid
			}
		}
	}
	c.NativePresetPolicies = nil
	if common.UnmarshalUnique(raw, &c.NativePresetPolicies) != nil {
		return ErrInvalid
	}
	for wire, models := range c.NativePresetPolicies {
		for model, p := range models {
			if !p.valid(*c, model, wire) {
				return ErrInvalid
			}
		}
	}
	return nil
}

// NativePresetTarget proves current target identity without publishing it.
func (a *Adapter) NativePresetTarget(model, wire string, channelID, channelType int, upstream, key, origin string) bool {
	p, ok := a.config.NativePresetPolicies[wire][model]
	if !ok {
		return false
	}
	sum := sha256.Sum256([]byte(key))
	return p.valid(a.config, model, wire) && p.ChannelID == channelID && p.ChannelType == channelType && p.UpstreamModel == upstream && p.ChannelKeySHA256 == hex.EncodeToString(sum[:]) && p.Origin == strings.TrimSuffix(origin, "/")
}

func (a *Adapter) projectNativePresets(source CatalogSourceModel, model CatalogModel, op CatalogOperation) ([]ParameterVariant, error) {
	p, exists := a.config.NativePresetPolicies[op.ID][model.ID]
	if !exists || !op.Available || !source.NativePresetTargets[op.ID] {
		return nil, nil
	}
	if !p.valid(a.config, model.ID, op.ID) || model.Capabilities.MaxOutputTokens == nil {
		return nil, ErrInvalid
	}
	config := a.config
	config.MaxOutputTokens = *model.Capabilities.MaxOutputTokens
	if model.ContextWindow > 0 && model.ContextWindow < config.MaxInputTokens {
		config.MaxInputTokens = model.ContextWindow
	}
	certificate, e := common.Marshal(p)
	if e != nil {
		return nil, e
	}
	sum := sha256.Sum256(certificate)
	out := make([]ParameterVariant, 0, len(p.Variants))
	for _, v := range p.Variants {
		if slices.Contains(model.ReasoningEfforts, v.ID) && (op.ID == "chat.completions.create" || op.ID == "responses.create") {
			return nil, ErrInvalid
		}
		if validateNativePreset(config, model.ID, op.ID, v.Parameters, p.SupportsAdaptive) != nil {
			return nil, ErrInvalid
		}
		var params map[string]any
		if common.UnmarshalUnique(v.Parameters, &params) != nil {
			return nil, ErrInvalid
		}
		v.Parameters, e = common.Marshal(params)
		if e != nil {
			return nil, e
		}
		descriptor := ParameterVariant{NativePreset: v, Version: p.Version, CertificateSHA256: hex.EncodeToString(sum[:]), APIVersion: p.APIVersion, SupportsAdaptive: p.SupportsAdaptive}
		raw, e := common.Marshal([]any{model.ID, op.ID, v.ID, v.Label, p.Version, descriptor.CertificateSHA256, p.APIVersion, p.SupportsAdaptive, params})
		if e != nil {
			return nil, e
		}
		digest := sha256.Sum256(raw)
		descriptor.DescriptorSHA256 = hex.EncodeToString(digest[:])
		out = append(out, descriptor)
	}
	return out, nil
}
