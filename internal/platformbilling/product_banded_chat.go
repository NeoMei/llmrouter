package platformbilling

import (
	"bytes"
	"io"
	"net/http"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/types"
)

const productBandedChatWire = "chat.completions.create"

func decodeProductCreditTextInputPolicies(raw []byte, c *Config) error {
	separate := *c
	if err := decodeTextInputPolicies(raw, &separate); err != nil {
		return err
	}
	for wire, policies := range separate.TextInputPolicies {
		if wire != productBandedChatWire {
			return ErrInvalid
		}
		for _, policy := range policies {
			if !productBandedChatProducer(policy) {
				return ErrInvalid
			}
		}
	}
	c.ProductCreditTextInputPolicies = separate.TextInputPolicies
	return nil
}

func productBandedChatProducer(policy TextInputPolicy) bool {
	return policy.ChannelType == constant.ChannelTypeOpenAI ||
		policy.ChannelType == constant.ChannelTypeDeepSeek && policy.Endpoint == deepSeekProductChatEndpoint && deepSeekProductChatUpstream(policy.UpstreamModel)
}

func (s *Session) ProductBandedChat() bool {
	return s.ProductCredit() && s.call.TextInputSnapshot != ""
}

// ProductBandedChatTarget returns the operator-certified bounds for the exact
// target and tariff ceiling that admission requires.
func (a *Adapter) ProductBandedChatTarget(model string, channel, channelType int, upstream, endpoint string, tariffCap int64) (contextTokens, maxOutputTokens int, ok bool) {
	if a == nil {
		return 0, 0, false
	}
	p, ok := a.config.ProductCreditTextInputPolicies[productBandedChatWire][model]
	if !ok || !productBandedChatProducer(p) || !p.valid(a.config, productBandedChatWire, model) ||
		p.ChannelID != channel || p.ChannelType != channelType || p.UpstreamModel != upstream ||
		p.Endpoint != endpoint || int64(p.MaxInputTokens) > tariffCap {
		return 0, 0, false
	}
	return p.ContextTokens, p.MaxOutputTokens, true
}

func (s *Session) BindProductBandedChat(publicModel string, channel, channelType int, upstream, endpoint string, tariffCap int64) (int, int, error) {
	if !s.ProductCredit() || s.call.Status != "ADMITTED" || s.WireOperation().ID != productBandedChatWire {
		return 0, 0, ErrInvalid
	}
	if _, _, ok := s.adapter.ProductBandedChatTarget(publicModel, channel, channelType, upstream, endpoint, tariffCap); !ok {
		return 0, 0, ErrInvalid
	}
	request, err := strictChatRequest(s.canonical)
	if err != nil || request["model"] != s.call.Model {
		return 0, 0, ErrInvalid
	}
	// The standard implicit cache rate must not price an explicit cache hit.
	// Text-only parts also keep media expansion outside the certified byte cap.
	for _, message := range request["messages"].([]any) {
		parts, _ := message.(map[string]any)["content"].([]any)
		for _, value := range parts {
			part := value.(map[string]any)
			if !protocolKeys(part, "type text refusal") {
				return 0, 0, ErrInvalid
			}
		}
	}
	config := s.adapter.config
	config.TextInputPolicies = map[string]map[string]TextInputPolicy{
		productBandedChatWire: {s.call.Model: config.ProductCreditTextInputPolicies[productBandedChatWire][publicModel]},
	}
	parsed, err := parseOperation(config, "chat", "application/json", s.canonical)
	if err != nil || parsed.MediaSnapshot != "" {
		return 0, 0, ErrInvalid
	}
	outbound, err := s.GenerationBody(channelType, types.RelayFormatOpenAI, upstream)
	if err != nil {
		return 0, 0, err
	}
	input, snapshot, err := textReservation(config, productBandedChatWire, s.call.Model, len(outbound), parsed.Output)
	if err != nil || snapshot == "" || int64(input) > tariffCap {
		return 0, 0, ErrInvalid
	}
	s.call.TextInputSnapshot = snapshot
	s.call.ReservationQuantity = int64(input)
	s.call.ReservationOutput = int64(parsed.Output)
	return input, parsed.Output, nil
}

func (s *Session) ValidateProductBandedChatOutbound(channel, channelType int, upstream string, req *http.Request, insecureTLS bool) error {
	if !s.ProductBandedChat() {
		return nil
	}
	if s.ValidateTextInputOutbound(channel, channelType, upstream, req, insecureTLS) != nil || req.GetBody == nil {
		return ErrInvalid
	}
	var frozen frozenTextReservation
	if common.UnmarshalUnique([]byte(s.call.TextInputSnapshot), &frozen) != nil {
		return ErrInvalid
	}
	limit := int64(frozen.Input / 2)
	expected, err := s.GenerationBody(channelType, types.RelayFormatOpenAI, upstream)
	if err != nil || int64(len(expected)) != limit || req.ContentLength != limit {
		return ErrInvalid
	}
	reader, err := req.GetBody()
	if err != nil {
		return ErrInvalid
	}
	defer reader.Close()
	actual, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil || !bytes.Equal(actual, expected) {
		return ErrInvalid
	}
	return nil
}
