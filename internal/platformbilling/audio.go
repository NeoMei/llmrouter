package platformbilling

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"gorm.io/gorm"
	"io"
	"math"
	"mime"
	"mime/multipart"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/pkg/mediafacts"
	"github.com/QuantumNous/new-api/pkg/metering"
	"github.com/QuantumNous/new-api/relay/channel/nativespeech"
)

// AudioPolicy is an operator certificate, never accepted from client JSON.
// It pins one exact channel member, native model, origin and sale tariff.
type AudioPolicy struct {
	Version          string            `json:"version"`
	PublicModel      string            `json:"public_model"`
	WireOperation    string            `json:"wire_operation"`
	ChannelID        int               `json:"channel_id"`
	ChannelType      int               `json:"channel_type"`
	UpstreamModel    string            `json:"upstream_model"`
	ChannelKeySHA256 string            `json:"channel_key_sha256"`
	Origin           string            `json:"origin"`
	UsagePolicy      string            `json:"usage_policy"`
	MaxQuantity      int               `json:"max_quantity"`
	RateIDs          map[string]string `json:"rate_ids"`
}
type frozenAudio struct {
	Policy AudioPolicy `json:"policy"`
}

func IsAudioWire(wire string) bool {
	return wire == "audio.speech.create" || wire == "audio.transcriptions.create" || wire == "dashscope.audio.transcriptions.create" || wire == "bailian.audio.speech.create" || wire == "elevenlabs.audio.speech.create"
}
func (p AudioPolicy) valid(model, wire string) bool {
	hash, err := hex.DecodeString(p.ChannelKeySHA256)
	origin, e := url.Parse(p.Origin)
	if err != nil || len(hash) != 32 || p.Version != "audio_v1" || p.PublicModel != model || !protocolString(model) || p.WireOperation != wire || p.ChannelID < 1 || len(p.RateIDs) == 0 || p.MaxQuantity < 1 || p.MaxQuantity > 86400 || e != nil || origin.Scheme != "https" || origin.Host == "" || origin.Path != "" || origin.RawQuery != "" || origin.User != nil || origin.Fragment != "" {
		return false
	}
	for asset, rate := range p.RateIDs {
		if (asset != "CREDIT/6" && asset != "CNY/6") || rate == "" {
			return false
		}
	}
	switch wire {
	case "bailian.audio.speech.create":
		_, err := nativespeech.PolicyForModel("bailian", p.UpstreamModel)
		return err == nil && p.ChannelType == constant.ChannelTypeAli && p.Origin == "https://dashscope.aliyuncs.com" && p.UsagePolicy == "provider_reported_v1"
	case "elevenlabs.audio.speech.create":
		_, err := nativespeech.PolicyForModel("elevenlabs", p.UpstreamModel)
		return err == nil && p.ChannelType == constant.ChannelTypeCustom && p.Origin == "https://api.elevenlabs.io" && p.UsagePolicy == "elevenlabs_character_cost_v1"
	case "audio.speech.create":
		if p.ChannelType == constant.ChannelTypeMiniMax {
			return (p.UpstreamModel == "speech-2.8-hd" || p.UpstreamModel == "speech-2.8-turbo") && (p.Origin == "https://api.minimax.io" || p.Origin == "https://api.minimaxi.com") && p.UsagePolicy == "provider_reported_v1"
		}
		return p.ChannelType == constant.ChannelTypeOpenAI && p.Origin == "https://api.openai.com" && (p.UpstreamModel == "tts-1" || p.UpstreamModel == "tts-1-hd" || p.UpstreamModel == "gpt-4o-mini-tts") && p.UsagePolicy == "unicode_scalar_v1"
	case "audio.transcriptions.create":
		return p.ChannelType == constant.ChannelTypeOpenAI && p.Origin == "https://api.openai.com" && (p.UpstreamModel == "whisper-1" || p.UpstreamModel == "gpt-4o-transcribe" || p.UpstreamModel == "gpt-4o-mini-transcribe") && p.UsagePolicy == "server_audio_measurement_v1"
	case "dashscope.audio.transcriptions.create":
		return p.ChannelType == constant.ChannelTypeAli && p.Origin == "https://dashscope.aliyuncs.com" && p.UpstreamModel == "qwen3-asr-flash" && p.UsagePolicy == "provider_reported_seconds_v1"
	}
	return false
}
func parseAudioOperation(c Config, p AudioPolicy, ct string, body []byte) (operationRequest, error) {
	invalid := operationRequest{}
	bound := c.MaxBodyBytes
	if configured := c.OperationLimits[wireBillingOperation(p.WireOperation)][p.PublicModel].MaxBodyBytes; configured > 0 {
		bound = configured
	}
	if p.ChannelType == constant.ChannelTypeMiniMax && bound > dto.MaxMiniMaxSpeechBodyBytes {
		bound = dto.MaxMiniMaxSpeechBodyBytes
	}
	if p.WireOperation == "audio.transcriptions.create" && bound > 10<<20 {
		bound = 10 << 20
	}
	if !p.valid(p.PublicModel, p.WireOperation) || int64(len(body)) > bound {
		return invalid, ErrInvalid
	}
	if p.WireOperation == "dashscope.audio.transcriptions.create" {
		if ct != "application/json" || !utf8.Valid(body) {
			return invalid, ErrInvalid
		}
		q, e := nativespeech.DecodeQwenASRRequest(body)
		if e != nil {
			return invalid, ErrInvalid
		}
		if q.Model != p.PublicModel {
			return invalid, ErrInvalid
		}
		seconds, e := q.AudioSeconds()
		if e != nil || seconds > float64(p.MaxQuantity) {
			return invalid, ErrInvalid
		}
		canonical, e := common.Marshal(q)
		if e != nil {
			return invalid, ErrInvalid
		}
		frozen, _ := common.Marshal(frozenAudio{p})
		return operationRequest{Model: p.PublicModel, Canonical: canonical, Quantity: p.MaxQuantity, MediaSnapshot: string(frozen)}, nil
	}
	if p.WireOperation == "audio.transcriptions.create" {
		result, err := parseOperation(c, "transcription", ct, body)
		if err != nil || result.Model != p.PublicModel {
			return invalid, ErrInvalid
		}
		media, params, err := mime.ParseMediaType(ct)
		if err != nil || media != "multipart/form-data" {
			return invalid, ErrInvalid
		}
		reader := multipart.NewReader(bytes.NewReader(body), params["boundary"])
		for {
			part, err := reader.NextRawPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				return invalid, ErrInvalid
			}
			for name, values := range part.Header {
				if len(values) != 1 || (name != "Content-Disposition" && name != "Content-Type") {
					return invalid, ErrInvalid
				}
			}
			disposition, attributes, e := mime.ParseMediaType(part.Header.Get("Content-Disposition"))
			if e != nil || disposition != "form-data" || attributes["name"] == "" {
				return invalid, ErrInvalid
			}
			for name := range attributes {
				if name != "name" && name != "filename" {
					return invalid, ErrInvalid
				}
			}
			if filename := attributes["filename"]; filename != part.FileName() || strings.ContainsAny(filename, `/\`) {
				return invalid, ErrInvalid
			}
			if part.FormName() != "file" && (attributes["filename"] != "" || part.Header.Get("Content-Type") != "") {
				return invalid, ErrInvalid
			}
			data, err := io.ReadAll(part)
			if err != nil {
				return invalid, ErrInvalid
			}
			if part.FormName() == "file" {
				if part.FileName() == "" || part.Header.Get("Content-Type") != "audio/wav" {
					return invalid, ErrInvalid
				}
				fact, e := mediafacts.DecodeMediaFact("audio", base64.StdEncoding.EncodeToString(data))
				if e != nil || fact.Format != "wav" || fact.Seconds <= 0 || fact.Seconds > float64(p.MaxQuantity) {
					return invalid, ErrInvalid
				}
			} else {
				if !utf8.Valid(data) {
					return invalid, ErrInvalid
				}
				switch part.FormName() {
				case "model":
				case "language":
					if len(data) > 16 {
						return invalid, ErrInvalid
					}
				case "prompt":
					if len(data) > 4096 {
						return invalid, ErrInvalid
					}
				case "response_format":
					if (p.UpstreamModel != "whisper-1" && string(data) == "verbose_json") || (string(data) != "json" && string(data) != "verbose_json" && string(data) != "text") {
						return invalid, ErrInvalid
					}
				case "temperature":
					n, e := strconv.ParseFloat(string(data), 64)
					if e != nil || math.IsNaN(n) || math.IsInf(n, 0) || n < 0 || n > 1 {
						return invalid, ErrInvalid
					}
				default:
					return invalid, ErrInvalid
				}
			}
		}
		frozen, _ := common.Marshal(frozenAudio{p})
		result.MediaSnapshot = string(frozen)
		result.Quantity = p.MaxQuantity
		return result, nil
	}
	if ct != "application/json" || !utf8.Valid(body) {
		return invalid, ErrInvalid
	}
	var request map[string]any
	if common.UnmarshalUnique(body, &request) != nil || request["model"] != p.PublicModel {
		return invalid, ErrInvalid
	}
	quantity := p.MaxQuantity
	if strings.HasPrefix(p.WireOperation, "audio.") {
		if !protocolKeys(request, "model input voice response_format speed instructions metadata") || !protocolString(request["input"]) || !protocolString(request["voice"]) || !protocolStrings(request, "input voice response_format instructions") {
			return invalid, ErrInvalid
		}
		text := request["input"].(string)
		if utf8.RuneCountInString(text) > p.MaxQuantity {
			return invalid, ErrInvalid
		}
		if v, ok := request["speed"]; ok && !protocolNumber(v, 0.25, 4) {
			return invalid, ErrInvalid
		}
		if !protocolEnum(request, "response_format", "mp3 wav") {
			return invalid, ErrInvalid
		}
		var audio dto.AudioRequest
		if common.Unmarshal(body, &audio) != nil {
			return invalid, ErrInvalid
		}
		audio.Model = p.UpstreamModel
		if p.ChannelType == constant.ChannelTypeMiniMax {
			if _, exists := request["instructions"]; exists {
				return invalid, ErrInvalid
			}
			if v, exists := request["metadata"]; exists {
				meta, ok := v.(map[string]any)
				if !ok || !protocolKeys(meta, "voice_setting audio_setting") {
					return invalid, ErrInvalid
				}
				if v, exists := meta["voice_setting"]; exists {
					m, ok := v.(map[string]any)
					if !ok || !protocolKeys(m, "vol pitch emotion text_normalization latex_read") || !protocolBools(m, "text_normalization latex_read") {
						return invalid, ErrInvalid
					}
				}
				if v, exists := meta["audio_setting"]; exists {
					m, ok := v.(map[string]any)
					if !ok || !protocolKeys(m, "sample_rate bitrate channel force_cbr") || !protocolBools(m, "force_cbr") {
						return invalid, ErrInvalid
					}
				}
			}
			if _, err := dto.BuildMiniMaxSpeech(audio); err != nil {
				return invalid, ErrInvalid
			}
		} else {
			if _, ok := request["metadata"]; ok {
				return invalid, ErrInvalid
			}
			if p.UpstreamModel != "gpt-4o-mini-tts" && request["instructions"] != nil {
				return invalid, ErrInvalid
			}
			quantity = utf8.RuneCountInString(text)
		}
	} else {
		req, err := nativeAudioRequest(p, request)
		if err != nil {
			return invalid, err
		}
		provider := "bailian"
		if p.WireOperation == "elevenlabs.audio.speech.create" {
			provider = "elevenlabs"
		}
		policy, err := nativespeech.PolicyForModel(provider, p.UpstreamModel)
		if err != nil {
			return invalid, ErrInvalid
		}
		producer, err := nativespeech.New(nativespeech.Target{Policy: policy, Origin: p.Origin, Credential: "validation-only"})
		if err != nil || producer.Validate(req) != nil || utf8.RuneCountInString(req.Text) > p.MaxQuantity {
			return invalid, ErrInvalid
		}
	}
	canonical, err := common.Marshal(request)
	if err != nil {
		return invalid, ErrInvalid
	}
	frozen, _ := common.Marshal(frozenAudio{p})
	return operationRequest{Model: p.PublicModel, Canonical: canonical, Quantity: quantity, MediaSnapshot: string(frozen)}, nil
}
func nativeAudioRequest(p AudioPolicy, request map[string]any) (nativespeech.Request, error) {
	var native map[string]any
	if p.WireOperation == "bailian.audio.speech.create" {
		if !protocolKeys(request, "model input") {
			return nativespeech.Request{}, ErrInvalid
		}
		native, _ = request["input"].(map[string]any)
		if !protocolKeys(native, "text voice language_type instructions optimize_instructions") || !protocolStrings(native, "text voice language_type instructions") || !protocolBools(native, "optimize_instructions") {
			return nativespeech.Request{}, ErrInvalid
		}
	} else {
		if !protocolKeys(request, "model voice_id text voice_settings") {
			return nativespeech.Request{}, ErrInvalid
		}
		native = map[string]any{"text": request["text"], "voice": request["voice_id"]}
		if value, ok := request["voice_settings"]; ok {
			settings, ok := value.(map[string]any)
			if !ok || !protocolKeys(settings, "stability similarity_boost style speed use_speaker_boost") || !protocolBools(settings, "use_speaker_boost") {
				return nativespeech.Request{}, ErrInvalid
			}
			for _, key := range []string{"stability", "similarity_boost", "style"} {
				if value, ok := settings[key]; ok && !protocolNumber(value, 0, 1) {
					return nativespeech.Request{}, ErrInvalid
				}
			}
			if value, ok := settings["speed"]; ok && !protocolNumber(value, 0.7, 1.2) {
				return nativespeech.Request{}, ErrInvalid
			}
			native["voice_settings"] = settings
		}
	}
	raw, err := common.Marshal(native)
	if err != nil {
		return nativespeech.Request{}, ErrInvalid
	}
	return nativespeech.DecodeRequest(raw)
}
func (a *Adapter) AudioTarget(model, wire string, id, kind int, upstream, key, origin string) bool {
	p := a.config.AudioPolicies[wire][model]
	sum := sha256.Sum256([]byte(key))
	return p.valid(model, wire) && p.ChannelID == id && p.ChannelType == kind && p.UpstreamModel == upstream && p.ChannelKeySHA256 == hex.EncodeToString(sum[:]) && p.Origin == origin
}
func (a *Adapter) AudioPolicy(model, wire string) (AudioPolicy, bool) {
	p, ok := a.config.AudioPolicies[wire][model]
	return p, ok && p.valid(model, wire)
}
func (s *Session) AudioPolicy() (AudioPolicy, error) {
	var f frozenAudio
	if common.UnmarshalUnique([]byte(s.call.MediaSnapshot), &f) != nil || !f.Policy.valid(s.call.Model, s.WireOperation().ID) {
		return AudioPolicy{}, ErrInvalid
	}
	return f.Policy, nil
}
func (s *Session) NativeAudioRequest() (nativespeech.Request, error) {
	p, e := s.AudioPolicy()
	if e != nil {
		return nativespeech.Request{}, e
	}
	var body map[string]any
	if common.UnmarshalUnique(s.canonical, &body) != nil {
		return nativespeech.Request{}, ErrInvalid
	}
	return nativeAudioRequest(p, body)
}
func NativeSpeechMetering(p metering.Price, result nativespeech.Result) metering.Usage {
	u := metering.Usage{Version: "metered_v2", Operation: "speech", State: "incomplete", Source: "native_speech"}
	if !result.Completed {
		return u
	}
	if p.Policy == "provider_reported_v1" && result.Usage.State == "confirmed" && result.Usage.Characters != nil {
		u.State = "confirmed"
		u.Measurements = []metering.Measurement{{Key: "characters", Unit: "character", Quantity: metering.Quantity{Value: strconv.FormatInt(*result.Usage.Characters, 10)}}}
		return u
	}
	if p.Policy == "elevenlabs_character_cost_v1" && result.Usage.State == "uncovered" && result.Usage.RawQuantity != "" {
		parts := strings.Split(result.Usage.RawQuantity, ".")
		value := parts[0]
		scale := 0
		if len(parts) == 2 {
			value += parts[1]
			scale = len(parts[1])
		}
		value = strings.TrimLeft(value, "0")
		if value == "" {
			value = "0"
		}
		u.State = "confirmed"
		u.Measurements = []metering.Measurement{{Key: "provider", Unit: "provider_unit", ProviderUnit: "elevenlabs.character_cost.multilingual_v2.v1", Quantity: metering.Quantity{Value: value, Scale: scale}}}
		if metering.ValidateUsage(p, u) != nil {
			u.State = "invalid"
		}
		return u
	}
	if result.Usage.State == "missing" {
		u.State = "missing"
	} else {
		u.State = "invalid"
	}
	return u
}

func audioRequestModel(op, ct string, body []byte, c Config) (string, error) {
	if op == "dashscope.audio.transcriptions.create" {
		var r nativespeech.QwenASRRequest
		if ct != "application/json" || common.UnmarshalUnique(body, &r) != nil || r.Validate() != nil {
			return "", ErrInvalid
		}
		return r.Model, nil
	}
	if op == "audio.transcriptions.create" || op == "transcription" {
		r, e := parseOperation(c, "transcription", ct, body)
		return r.Model, e
	}
	var r map[string]any
	if common.UnmarshalUnique(body, &r) != nil {
		return "", ErrInvalid
	}
	model, _ := r["model"].(string)
	return model, nil
}
func (a *Adapter) AudioRequestModel(wire, ct string, body []byte) (string, error) {
	return audioRequestModel(wire, ct, body, a.config)
}
func (s *Session) ValidateAudioTarget(id, kind int, upstream, key, origin string) error {
	p, e := s.AudioPolicy()
	if e != nil {
		return e
	}
	sum := sha256.Sum256([]byte(key))
	if p.ChannelID != id || p.ChannelType != kind || p.UpstreamModel != upstream || p.ChannelKeySHA256 != hex.EncodeToString(sum[:]) || p.Origin != origin {
		return ErrInvalid
	}
	return nil
}

// AudioReplay classifies an existing immutable call using its frozen certificate
// before consulting mutable routing, model policies or published sale rates.
func (a *Adapter) AudioReplay(ctx context.Context, credential, task, id, wire, ct string, body []byte) (bool, error) {
	if !a.Enabled() || !IsAudioWire(wire) || !stableID.MatchString(id) || !stableID.MatchString(task) || !IsModelCredential(credential) {
		return false, ErrInvalid
	}
	var old Call
	if err := a.db.WithContext(ctx).First(&old, "call_id = ?", id).Error; errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	} else if err != nil {
		return true, err
	}
	if _, err := a.Receipt(ctx, credential, task, id); err != nil {
		return true, err
	}
	if old.WireOperation != wire {
		return true, ErrConflict
	}
	var frozen frozenAudio
	if common.UnmarshalUnique([]byte(old.MediaSnapshot), &frozen) != nil {
		return true, ErrConflict
	}
	c := a.config
	c.MaxBodyBytes = 64 << 20
	c.OperationLimits = map[string]map[string]OperationLimits{old.Operation: {old.Model: {MaxQuantity: frozen.Policy.MaxQuantity}}}
	req, err := parseAudioOperation(c, frozen.Policy, ct, body)
	if err != nil {
		return true, ErrConflict
	}
	data, _ := common.Marshal(struct {
		Version, Operation, Task, Actor, Account, Origin, Asset, Funding string
		Profile                                                          int
		Body                                                             string
	}{"platform-metered-v2", old.Operation, old.TaskID, old.ActorID, old.AccountID, old.OriginProductID, old.Asset, old.FundingMode, old.BillingProfileVersion, string(req.Canonical)})
	data, _ = common.Marshal(struct {
		Request string
		Media   string
	}{string(data), old.MediaSnapshot})
	data, _ = common.Marshal(struct {
		Version string
		Wire    WireOperation
		Scope   string
	}{"platform-wire-v3", WireOperation{ID: wire}, string(data)})
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != old.Digest {
		return true, ErrConflict
	}
	return true, ErrDuplicate
}

// RecordNativeAudioObservation retains supplier quantity lexemes separately from
// tariff interpretation and output delivery; no provider URL or credential is stored.
func (s *Session) RecordNativeAudioObservation(ctx context.Context, r nativespeech.Result, delivered bool) error {
	if !IsAudioWire(s.WireOperation().ID) {
		return ErrInvalid
	}
	raw, err := common.Marshal(struct {
		Usage                                   nativespeech.Usage
		Completed, Delivered                    bool
		Bytes                                   int
		SHA256, ContentType, RequestID, TraceID string
	}{r.Usage, r.Completed, delivered, len(r.Audio), hex.EncodeToString(r.SHA256[:]), r.ContentType, r.RequestID, r.TraceID})
	if err != nil || len(raw) > 65536 {
		return ErrInvalid
	}
	saved := s.adapter.db.WithContext(ctx).Model(&Call{}).Where("call_id = ? AND status = ? AND audio_observation = ?", s.call.CallID, "UNKNOWN", "").Update("audio_observation", string(raw))
	if saved.Error != nil {
		return saved.Error
	}
	if saved.RowsAffected != 1 {
		return ErrConflict
	}
	return nil
}

// RecordQwenASRObservation persists the exact native response and provider
// seconds before any transcript is delivered or tariff is settled.
func (s *Session) RecordQwenASRObservation(ctx context.Context, r nativespeech.QwenASRResult, delivered bool) error {
	if s.WireOperation().ID != "dashscope.audio.transcriptions.create" {
		return ErrInvalid
	}
	raw, err := common.Marshal(struct {
		Result    nativespeech.QwenASRResult `json:"result"`
		Delivered bool                       `json:"delivered"`
	}{r, delivered})
	if err != nil || len(raw) > 4<<20 {
		return ErrInvalid
	}
	saved := s.adapter.db.WithContext(ctx).Model(&Call{}).Where("call_id = ? AND status = ? AND audio_observation = ?", s.call.CallID, "UNKNOWN", "").Update("audio_observation", string(raw))
	if saved.Error != nil {
		return saved.Error
	}
	if saved.RowsAffected != 1 {
		return ErrConflict
	}
	return nil
}

func NativeQwenASRMetering(p metering.Price, r nativespeech.QwenASRResult) metering.Usage {
	u := metering.Usage{Version: "metered_v2", Operation: "transcription", State: "incomplete", Source: "native_qwen_asr", ProviderRequestID: r.RequestID}
	if r.Completed && r.UsageState == "confirmed" && r.Seconds.Value != "" {
		u.State = "confirmed"
		u.Measurements = []metering.Measurement{{Key: "audio", Unit: "audio_second", Quantity: r.Seconds}}
		if metering.ValidateUsage(p, u) != nil {
			u.State = "invalid"
			u.Measurements = nil
		}
	} else if r.UsageState == "missing" {
		u.State = "missing"
	}
	return u
}

func (s *Session) QwenASRRequest() (nativespeech.QwenASRRequest, error) {
	if s.WireOperation().ID != "dashscope.audio.transcriptions.create" {
		return nativespeech.QwenASRRequest{}, ErrInvalid
	}
	p, e := s.AudioPolicy()
	if e != nil {
		return nativespeech.QwenASRRequest{}, e
	}
	var r nativespeech.QwenASRRequest
	if common.UnmarshalUnique(s.canonical, &r) != nil {
		return nativespeech.QwenASRRequest{}, ErrInvalid
	}
	r.Model = p.UpstreamModel
	if r.Validate() != nil {
		return nativespeech.QwenASRRequest{}, ErrInvalid
	}
	return r, nil
}
