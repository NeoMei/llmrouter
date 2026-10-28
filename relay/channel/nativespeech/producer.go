// Package nativespeech produces bounded native speech results. It grants no
// execution or billing authority; callers must journal and authorize before Send.
package nativespeech

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
)

const maxJSONBytes = 128 << 10

var errInvalidRequest = errors.New("unsupported native speech request or policy")
var errResponse = errors.New("native speech response incomplete or invalid; reconcile before retry")
var voiceID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,80}$`)
var bailianVoice = regexp.MustCompile(`^[A-Za-z][A-Za-z ]{0,79}$`)
var safeReference = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
var decimalObservation = regexp.MustCompile(`^(0|[1-9][0-9]{0,15})(\.[0-9]{1,9})?$`)

// Request deliberately has no model, origin, credential, payer or pricing fields.
type Request struct {
	Text                 string         `json:"text"`
	Voice                string         `json:"voice"`
	Language             *string        `json:"language_type,omitempty"`
	Instructions         *string        `json:"instructions,omitempty"`
	OptimizeInstructions *bool          `json:"optimize_instructions,omitempty"`
	VoiceSettings        *VoiceSettings `json:"voice_settings,omitempty"`
}
type VoiceSettings struct {
	Stability       *float64 `json:"stability,omitempty"`
	SimilarityBoost *float64 `json:"similarity_boost,omitempty"`
	Style           *float64 `json:"style,omitempty"`
	Speed           *float64 `json:"speed,omitempty"`
	UseSpeakerBoost *bool    `json:"use_speaker_boost,omitempty"`
}

// Policy is selected by trusted server configuration, never parsed from Request.
// Exact models are enumerated; aliases require explicit selection of this policy.
type Policy struct {
	Provider, Model, Version, Reference string
	MaxTextScalars                      int
	Instructions                        bool
}

func PolicyForModel(provider, model string) (Policy, error) {
	p := Policy{Provider: provider, Model: model}
	switch provider {
	case "bailian":
		p.Version = "qwen3-flash-http-v1"
		p.Reference = "https://help.aliyun.com/en/model-studio/qwen-tts-api"
		p.MaxTextScalars = 600
		switch model {
		case "qwen3-tts-flash-2025-11-27", "qwen3-tts-flash":
		case "qwen3-tts-instruct-flash-2026-01-26", "qwen3-tts-instruct-flash":
			p.Instructions = true
		default:
			return Policy{}, errInvalidRequest
		}
	case "elevenlabs":
		if model != "eleven_multilingual_v2" {
			return Policy{}, errInvalidRequest
		}
		p.Version = "eleven-multilingual-v2-http-v1"
		p.Reference = "https://elevenlabs.io/docs/api-reference/text-to-speech/convert"
		p.MaxTextScalars = 10000
	default:
		return Policy{}, errInvalidRequest
	}
	return p, nil
}

// Limits can only narrow the hard ceilings. Durations constrain validation,
// never serve as a substitute for supplier-reported usage.
type Limits struct{ MaxAudioBytes, MaxAudioSeconds, TimeoutSeconds int }
type Target struct {
	Policy             Policy
	Origin, Credential string
	ResourceOrigins    []string
	Limits             Limits
}
type Producer struct {
	target        Target
	client        *http.Client
	allowLoopback bool
}
type Usage struct {
	// missing, invalid, conflict, confirmed or uncovered. Only confirmed characters
	// can feed the existing provider_reported_v1 character tariff.
	State       string
	RawQuantity string
	Characters  *int64
	Source      string
	Counters    map[string]int64
}
type Result struct {
	Completed                       bool
	Audio                           []byte
	SHA256                          [32]byte
	ContentType, RequestID, TraceID string
	Usage                           Usage
}

func DecodeRequest(b []byte) (Request, error) {
	var r Request
	if len(b) > maxJSONBytes || !utf8.Valid(b) || decodeNativeJSON(b, &r) != nil {
		return Request{}, errInvalidRequest
	}
	d := common.NewJsonDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if d.Decode(&r) != nil {
		return Request{}, errInvalidRequest
	}
	return r, nil
}

func (p *Producer) Validate(r Request) error {
	if !utf8.ValidString(r.Text) || strings.TrimSpace(r.Text) == "" || utf8.RuneCountInString(r.Text) > p.target.Policy.MaxTextScalars {
		return errInvalidRequest
	}
	if p.target.Policy.Provider == "bailian" {
		if !bailianVoice.MatchString(r.Voice) || r.VoiceSettings != nil {
			return errInvalidRequest
		}
		if !p.target.Policy.Instructions && (r.Instructions != nil || r.OptimizeInstructions != nil) {
			return errInvalidRequest
		}
		if r.Instructions != nil && (!utf8.ValidString(*r.Instructions) || strings.TrimSpace(*r.Instructions) == "" || len(*r.Instructions) > 1600) {
			return errInvalidRequest
		}
		if r.OptimizeInstructions != nil && *r.OptimizeInstructions && r.Instructions == nil {
			return errInvalidRequest
		}
		if r.Language != nil {
			switch *r.Language {
			case "Auto", "Chinese", "English", "German", "Italian", "Portuguese", "Spanish", "Japanese", "Korean", "French", "Russian":
			default:
				return errInvalidRequest
			}
		}
	} else {
		if !voiceID.MatchString(r.Voice) || r.Language != nil || r.Instructions != nil || r.OptimizeInstructions != nil {
			return errInvalidRequest
		}
		if r.VoiceSettings != nil {
			v := r.VoiceSettings
			for _, value := range []*float64{v.Stability, v.SimilarityBoost, v.Style} {
				if value != nil && (math.IsNaN(*value) || math.IsInf(*value, 0) || *value < 0 || *value > 1) {
					return errInvalidRequest
				}
			}
			// Deliberately narrow consumer policy, independent of broader future API limits.
			if v.Speed != nil && (math.IsNaN(*v.Speed) || math.IsInf(*v.Speed, 0) || *v.Speed < 0.7 || *v.Speed > 1.2) {
				return errInvalidRequest
			}
		}
	}
	return nil
}

// Send performs a single synthesis POST. No automatic synthesis retries or
// fallback occur. Callers must retain Usage even when resource delivery fails.
func (p *Producer) Send(ctx context.Context, r Request) (Result, error) {
	result := Result{Usage: Usage{State: "missing"}}
	if err := p.Validate(r); err != nil {
		return result, err
	}
	var body []byte
	var err error
	path := "/api/v1/services/aigc/multimodal-generation/generation"
	if p.target.Policy.Provider == "bailian" {
		body, err = common.Marshal(struct {
			Model string  `json:"model"`
			Input Request `json:"input"`
		}{p.target.Policy.Model, r})
	} else {
		path = "/v1/text-to-speech/" + r.Voice + "?output_format=mp3_44100_128"
		body, err = common.Marshal(struct {
			Text     string         `json:"text"`
			Model    string         `json:"model_id"`
			Settings *VoiceSettings `json:"voice_settings,omitempty"`
		}{r.Text, p.target.Policy.Model, r.VoiceSettings})
	}
	if err != nil || len(body) > maxJSONBytes {
		return result, errInvalidRequest
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.target.Origin+path, bytes.NewReader(body))
	if err != nil {
		return result, errInvalidRequest
	}
	req.Header.Set("Content-Type", "application/json")
	if p.target.Policy.Provider == "bailian" {
		req.Header.Set("Authorization", "Bearer "+p.target.Credential)
	} else {
		req.Header.Set("xi-api-key", p.target.Credential)
		req.Header.Set("Accept", "audio/mpeg")
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return result, errResponse
	}
	defer resp.Body.Close()
	result.RequestID = headerReference(resp.Header, "request-id")
	result.TraceID = headerReference(resp.Header, "x-trace-id")
	if resp.StatusCode != http.StatusOK {
		return result, errResponse
	}
	var audio []byte
	var ct string
	if p.target.Policy.Provider == "bailian" {
		if !contentType(resp.Header, "application/json") {
			return result, errResponse
		}
		raw, readErr := readBounded(resp.Body, maxJSONBytes)
		if readErr != nil {
			return result, errResponse
		}
		var packet struct {
			Status    *int   `json:"status_code"`
			Code      string `json:"code"`
			RequestID string `json:"request_id"`
			Output    *struct {
				FinishReason string `json:"finish_reason"`
				Audio        *struct {
					URL string `json:"url"`
				} `json:"audio"`
			} `json:"output"`
			Usage map[string]json.RawMessage `json:"usage"`
		}
		if !utf8.Valid(raw) {
			result.Usage.State = "invalid"
			return result, errResponse
		}
		if e := decodeNativeJSON(raw, &packet); e != nil {
			result.Usage.State = "invalid"
			var duplicate *common.DuplicateJSONKeyError
			if errors.As(e, &duplicate) {
				result.Usage.State = "conflict"
			}
			return result, errResponse
		}
		if safeReference.MatchString(packet.RequestID) {
			result.RequestID = packet.RequestID
		}
		if packet.Code != "" || packet.Status != nil && *packet.Status != 200 || packet.Output == nil || packet.Output.FinishReason != "stop" || packet.Output.Audio == nil {
			return result, errResponse
		}
		result.Usage.Source = "bailian.usage.characters"
		if packet.Usage != nil {
			result.Usage.Counters = map[string]int64{}
			state := "confirmed"
			for key, rawQuantity := range packet.Usage {
				switch key {
				case "characters", "input_tokens", "output_tokens", "total_tokens":
				default:
					if state != "invalid" {
						state = "uncovered"
					}
					continue
				}
				if bytes.Equal(rawQuantity, []byte("null")) {
					if key != "characters" {
						state = "invalid"
					}
					continue
				}
				var quantity int64
				if common.UnmarshalUnique(rawQuantity, &quantity) != nil || quantity < 0 || quantity > math.MaxInt32 {
					state = "invalid"
					continue
				}
				result.Usage.Counters[key] = quantity
				if key != "characters" && quantity != 0 && state != "invalid" {
					state = "uncovered"
				}
			}
			if quantity, ok := result.Usage.Counters["characters"]; ok {
				result.Usage.State = state
				result.Usage.RawQuantity = strconv.FormatInt(quantity, 10)
				if state == "confirmed" {
					result.Usage.Characters = &quantity
				}
			} else if state != "confirmed" {
				result.Usage.State = state
			}
		}
		resource, resourceErr := p.resourceURL(packet.Output.Audio.URL)
		if resourceErr != nil {
			return result, errResponse
		}
		download, requestErr := http.NewRequestWithContext(ctx, http.MethodGet, resource, nil)
		if requestErr != nil {
			return result, errResponse
		}
		// New request, no supplier credential, cookies or caller headers.
		audioResp, downloadErr := p.client.Do(download)
		if downloadErr != nil {
			return result, errResponse
		}
		defer audioResp.Body.Close()
		if audioResp.StatusCode != http.StatusOK || !contentType(audioResp.Header, "audio/wav", "audio/x-wav", "audio/wave") {
			return result, errResponse
		}
		audio, err = readBounded(audioResp.Body, p.target.Limits.MaxAudioBytes)
		ct = "audio/wav"
	} else {
		result.Usage = elevenUsage(resp.Header)
		if !contentType(resp.Header, "audio/mpeg") {
			return result, errResponse
		}
		audio, err = readBounded(resp.Body, p.target.Limits.MaxAudioBytes)
		ct = "audio/mpeg"
	}
	if err != nil || validateAudio(audio, ct, p.target.Limits.MaxAudioSeconds) != nil {
		return result, errResponse
	}
	result.Completed = true
	result.Audio = audio
	result.ContentType = ct
	result.SHA256 = sha256.Sum256(audio)
	return result, nil
}

func elevenUsage(h http.Header) Usage {
	u := Usage{State: "missing", Source: "elevenlabs.character-cost"}
	values := h.Values("character-cost")
	if len(values) == 0 {
		return u
	}
	if len(values) != 1 || strings.Contains(values[0], ",") {
		u.State = "conflict"
		return u
	}
	if !decimalObservation.MatchString(values[0]) {
		u.State = "invalid"
		return u
	}
	// The primary documentation names this metadata but does not define its
	// numeric grammar or a model-specific mapping to LR integer characters.
	// This bounded lexical observation is retained exactly, never rounded/cast.
	u.State = "uncovered"
	u.RawQuantity = values[0]
	return u
}
func headerReference(h http.Header, key string) string {
	v := h.Values(key)
	if len(v) == 1 && safeReference.MatchString(v[0]) {
		return v[0]
	}
	return ""
}
func readBounded(r io.Reader, limit int) ([]byte, error) {
	b, e := io.ReadAll(io.LimitReader(r, int64(limit)+1))
	if e != nil || len(b) == 0 || len(b) > limit {
		return nil, errResponse
	}
	return b, nil
}

// Go's struct decoder matches field names case-insensitively. Reject aliases
// that would otherwise bypass exact-key duplicate rejection and overwrite usage.
func decodeNativeJSON(raw []byte, destination any) error {
	var value any
	if err := common.UnmarshalUnique(raw, &value); err != nil {
		return err
	}
	if err := rejectFieldAliases(value); err != nil {
		return err
	}
	return common.UnmarshalUnique(raw, destination)
}
func rejectFieldAliases(value any) error {
	switch v := value.(type) {
	case map[string]any:
		keys := make(map[string]bool, len(v))
		for key, child := range v {
			// Match encoding/json's Unicode simple-fold equivalence, including
			// long s and Kelvin sign. Lowercasing alone misses these aliases.
			runes := []rune(key)
			for i, original := range runes {
				for next := unicode.SimpleFold(original); next != original; next = unicode.SimpleFold(next) {
					if next < runes[i] {
						runes[i] = next
					}
				}
			}
			folded := string(runes)
			if keys[folded] {
				return &common.DuplicateJSONKeyError{Key: "field alias"}
			}
			keys[folded] = true
			if err := rejectFieldAliases(child); err != nil {
				return err
			}
		}
	case []any:
		for _, child := range v {
			if err := rejectFieldAliases(child); err != nil {
				return err
			}
		}
	}
	return nil
}
