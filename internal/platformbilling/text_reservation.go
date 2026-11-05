package platformbilling

import (
	"net/http"
	"net/url"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
)

// TextInputPolicy is an operator-verified context certificate for one exact
// native route. It changes technical admission bounds, never financial caps.
type TextInputPolicy struct {
	Version           string `json:"version"`
	ChannelID         int    `json:"channel_id"`
	ChannelType       int    `json:"channel_type"`
	UpstreamModel     string `json:"upstream_model"`
	Endpoint          string `json:"endpoint"`
	MaxInputTokens    int    `json:"max_input_tokens"`
	ContextTokens     int    `json:"context_tokens"`
	MaxOutputTokens   int    `json:"max_output_tokens"`
	ProviderReference string `json:"provider_reference"`
}

func (p TextInputPolicy) valid(c Config, wire, model string) bool {
	_, proven := TokenProducer(wire, p.ChannelType)
	// The legacy DeepSeek Chat adapter shares the confirmed raw OpenAI usage
	// observer and is already in platformChatChannel's execution allowlist.
	proven = proven || (wire == "chat.completions.create" && p.ChannelType == constant.ChannelTypeDeepSeek)
	u, err := url.Parse(p.Endpoint)
	ref, refErr := url.Parse(p.ProviderReference)
	return (wire == "chat.completions.create" || wire == "responses.create") && proven &&
		model != "" && len(model) <= 255 && p.Version == "full_body_bytes_v1" && p.ChannelID > 0 &&
		p.UpstreamModel != "" && len(p.UpstreamModel) <= 255 &&
		p.MaxInputTokens > 0 && p.MaxInputTokens <= 1<<20 && p.ContextTokens > 0 && p.ContextTokens <= 1<<20 &&
		p.MaxOutputTokens > 0 && p.MaxOutputTokens <= c.MaxOutputTokens &&
		p.MaxOutputTokens < p.ContextTokens &&
		err == nil && u.Scheme == "https" && u.Host != "" && u.User == nil && u.RawQuery == "" && !u.ForceQuery && u.Fragment == "" && u.Path != "" &&
		refErr == nil && ref.Scheme == "https" && ref.Host != "" && ref.User == nil
}

type frozenTextReservation struct {
	Version string           `json:"version"`
	Input   int              `json:"input"`
	Output  int              `json:"output"`
	Policy  *TextInputPolicy `json:"policy,omitempty"`
}

func decodeTextInputPolicies(raw []byte, c *Config) error {
	var entries map[string]map[string]map[string]any
	if common.UnmarshalUnique(raw, &entries) != nil || entries == nil || len(entries) > 2 {
		return ErrInvalid
	}
	for _, models := range entries {
		if len(models) > 128 {
			return ErrInvalid
		}
		for _, p := range models {
			if !protocolKeys(p, "version channel_id channel_type upstream_model endpoint max_input_tokens context_tokens max_output_tokens provider_reference") {
				return ErrInvalid
			}
		}
	}
	var policies map[string]map[string]TextInputPolicy
	if common.UnmarshalUnique(raw, &policies) != nil {
		return ErrInvalid
	}
	for wire, models := range policies {
		if wire != "chat.completions.create" && wire != "responses.create" {
			return ErrInvalid
		}
		for model, p := range models {
			if !p.valid(*c, wire, model) {
				return ErrInvalid
			}
		}
	}
	c.TextInputPolicies = policies
	return nil
}

func textReservation(c Config, wire, model string, bodyBytes, output int) (int, string, error) {
	// Bound before multiplication. Full JSON includes tools, schemas, system,
	// history and metadata; this deliberately retains the existing 2x policy.
	if bodyBytes < 1 || bodyBytes > 1<<20 || output < 1 || output > c.MaxOutputTokens {
		return 0, "", ErrInvalid
	}
	bound := c.MaxInputTokens
	frozen := frozenTextReservation{Version: "full_body_bytes_v1", Input: 2 * bodyBytes, Output: output}
	if p, ok := c.TextInputPolicies[wire][model]; ok {
		if !p.valid(c, wire, model) || output > p.MaxOutputTokens {
			return 0, "", ErrInvalid
		}
		bound = min(p.MaxInputTokens, p.ContextTokens-output)
		frozen.Policy = &p
	} else {
		if frozen.Input > bound {
			return 0, "", ErrInvalid
		}
		return bound, "", nil
	}
	if frozen.Input > bound {
		return 0, "", ErrInvalid
	}
	raw, err := common.Marshal(frozen)
	if err != nil {
		return 0, "", err
	}
	return frozen.Input, string(raw), nil
}

// ValidateTextInputTarget runs on the adapter's actual endpoint before dispatch.
// A policy for a direct provider must not authorize a fallback/forwarding route.
func (s *Session) ValidateTextInputTarget(channel, channelType int, upstream, endpoint string) error {
	if s.call.TextInputSnapshot == "" {
		return nil
	}
	var frozen frozenTextReservation
	if common.UnmarshalUnique([]byte(s.call.TextInputSnapshot), &frozen) != nil || frozen.Version != "full_body_bytes_v1" ||
		frozen.Policy == nil || frozen.Input < 1 || frozen.Output < 1 ||
		int64(frozen.Input) != s.call.ReservationQuantity || int64(frozen.Output) != s.call.ReservationOutput {
		return ErrInvalid
	}
	if p := frozen.Policy; p.ChannelID != channel || p.ChannelType != channelType || p.UpstreamModel != upstream || p.Endpoint != endpoint {
		return ErrInvalid
	}
	return nil
}

func (s *Session) ValidateTextInputOutbound(channel, channelType int, upstream string, req *http.Request, insecureTLS bool) error {
	if s.call.TextInputSnapshot == "" {
		return nil
	}
	if req == nil || req.URL == nil || req.Method != http.MethodPost || (req.Host != "" && req.Host != req.URL.Host) {
		return ErrInvalid
	}
	if insecureTLS {
		return ErrInvalid
	}
	return s.ValidateTextInputTarget(channel, channelType, upstream, req.URL.String())
}
