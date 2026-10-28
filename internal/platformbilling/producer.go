package platformbilling

import (
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/pkg/mediafacts"
	"github.com/QuantumNous/new-api/types"
	"strings"
)

// TokenProducer is the closed set of native adapter/format/raw-observer paths
// covered by managed settlement. A channel family or model name is not proof.
func TokenProducer(wire string, channel int) (types.RelayFormat, bool) {
	switch wire {
	case "chat.completions.create":
		return types.RelayFormatOpenAI, channel == constant.ChannelTypeOpenAI
	case "responses.compact":
		return types.RelayFormatOpenAIResponsesCompaction, channel == constant.ChannelTypeOpenAI
	case "responses.create":
		return types.RelayFormatOpenAIResponses, channel == constant.ChannelTypeOpenAI
	case "claude.messages.create":
		return types.RelayFormatClaude, channel == constant.ChannelTypeAnthropic
	case "gemini.generate":
		return types.RelayFormatGemini, channel == constant.ChannelTypeGemini
	case "rerank.create":
		return types.RelayFormatRerank, channel == constant.ChannelTypeAli
	case "multimodal.embeddings.create":
		return types.RelayFormatMultimodalEmbedding, channel == constant.ChannelTypeAli
	case "dashscope.audio.transcriptions.create":
		return types.RelayFormat("native_qwen_asr"), channel == constant.ChannelTypeAli
	case "embeddings.create":
		return types.RelayFormatEmbedding, channel == constant.ChannelTypeOpenAI
	}
	return "", false
}

func (s *Session) WireOperation() WireOperation {
	id := s.call.WireOperation
	if id == "" {
		id = legacyWireOperation(s.call.Operation)
	}
	return WireOperation{ID: id, PathModel: s.call.WirePathModel, Mode: s.call.WireMode}
}

// GenerationBody uses the validated original wire tree, not a re-marshaled DTO.
// Only the trusted routing model and Chat's required usage collection change.
// Every paid/optional field was admitted before the hold was made.
func (s *Session) GenerationBody(channel int, format types.RelayFormat, upstream string) ([]byte, error) {
	wire := s.WireOperation()
	expected, ok := TokenProducer(wire.ID, channel)
	if !ok || expected != format || len(s.canonical) == 0 || upstream == "" || (wire.ID == "gemini.generate" && strings.ContainsAny(upstream, "/:?#%")) {
		return nil, ErrInvalid
	}
	if s.call.MediaSnapshot != "" {
		var frozen frozenMediaReservation
		if common.Unmarshal([]byte(s.call.MediaSnapshot), &frozen) != nil {
			return nil, ErrInvalid
		}
		if wire.ID == "responses.compact" {
			p := frozen.CompactPolicy
			if p == nil || p.PublicModel != s.call.Model || p.ChannelType != channel || p.UpstreamModel != upstream || p.WireOperation != wire.ID || p.EndpointPath != "/v1/responses/compact" {
				return nil, ErrInvalid
			}
		} else if frozen.CompactPolicy != nil || frozen.Policy.ChannelType != channel || frozen.Policy.UpstreamModel != upstream {
			return nil, ErrInvalid
		}
	} else if wire.ID == "responses.compact" {
		return nil, ErrInvalid
	}
	var request map[string]any
	if common.UnmarshalUnique(s.canonical, &request) != nil {
		return nil, ErrInvalid
	}
	if wire.ID != "gemini.generate" {
		request["model"] = upstream
	}
	if wire.ID == "chat.completions.create" && request["stream"] == true {
		options, _ := request["stream_options"].(map[string]any)
		if options == nil {
			options = map[string]any{}
		}
		options["include_usage"] = true
		request["stream_options"] = options
	}
	return common.Marshal(request)
}

// NativeInputTarget matches a certified media contract to a currently selectable
// routing target. Catalog and execution use this same exact binding.
func (a *Adapter) NativeInputTarget(wire, model string, channel int, upstream, modality string) bool {
	policy, ok := a.config.NativeInputPolicies[wire][model]
	return ok && policy.valid(a.config, wire) && policy.ChannelType == channel && policy.UpstreamModel == upstream && ((modality == "image" && policy.MaxImages > 0) || (modality == "document" && policy.MaxDocuments > 0))
}

// AdmittedMediaFacts returns independent values decoded before reservation from
// the original native content. It never reparses a lossy relay DTO or fetches media.
// GenerationBody additionally binds this same frozen certificate to the selected target.
func (s *Session) AdmittedMediaFacts(format types.RelayFormat) ([]mediafacts.MediaFact, bool, error) {
	if IsAudioWire(s.WireOperation().ID) {
		return nil, false, nil
	}
	if s.WireOperation().ID == "embeddings.create" || s.WireOperation().ID == "rerank.create" || s.WireOperation().ID == "multimodal.embeddings.create" {
		return nil, false, nil
	}

	if s.call.MediaSnapshot == "" {
		return nil, false, nil
	}
	var frozen frozenMediaReservation
	if common.Unmarshal([]byte(s.call.MediaSnapshot), &frozen) != nil {
		return nil, false, ErrInvalid
	}
	if s.WireOperation().ID == "responses.compact" && len(frozen.Facts) == 0 {
		p := frozen.CompactPolicy
		if p == nil || format != types.RelayFormatOpenAIResponsesCompaction {
			return nil, false, ErrInvalid
		}
		return []mediafacts.MediaFact{}, true, nil
	}
	expected, proven := TokenProducer(s.WireOperation().ID, frozen.Policy.ChannelType)
	if !proven || expected != format || !frozen.Policy.valid(s.adapter.config, s.WireOperation().ID) {
		return nil, false, ErrInvalid
	}
	facts := make([]mediafacts.MediaFact, 0, len(frozen.Facts))
	for _, f := range frozen.Facts {
		modality := "document"
		if strings.HasPrefix(f.MIME, "image/") {
			modality = "image"
		}
		facts = append(facts, mediafacts.MediaFact{Modality: modality, Format: f.MIME, Width: f.Width, Height: f.Height, Pixels: int64(f.Width) * int64(f.Height), Pages: f.Pages, EncodedBytes: f.Bytes})
	}
	return facts, true, nil
}

// ReservationBounds are the immutable admitted context and complete output caps.
// Routing must not replace them with byte or tokenizer estimates for compact.
func (s *Session) ReservationBounds() (int64, int64) {
	return s.call.ReservationQuantity, s.call.ReservationOutput
}
