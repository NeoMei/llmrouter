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
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/mediafacts"
	"github.com/QuantumNous/new-api/pkg/metering"
)

var qwenASRSeconds = regexp.MustCompile(`^(0|[1-9][0-9]{0,5})(\.[0-9]{1,6})?$`)

type QwenASROptions struct {
	Language  *string `json:"language,omitempty"`
	EnableITN *bool   `json:"enable_itn,omitempty"`
}
type QwenASRInputAudio struct {
	Data string `json:"data"`
}
type QwenASRContent struct {
	Type       string             `json:"type"`
	InputAudio *QwenASRInputAudio `json:"input_audio,omitempty"`
}
type QwenASRMessage struct {
	Role    string           `json:"role"`
	Content []QwenASRContent `json:"content"`
}
type QwenASRRequest struct {
	Model         string           `json:"model"`
	Messages      []QwenASRMessage `json:"messages"`
	ASROptions    *QwenASROptions  `json:"asr_options,omitempty"`
	Stream        bool             `json:"stream,omitempty"`
	StreamOptions *struct {
		IncludeUsage bool `json:"include_usage"`
	} `json:"stream_options,omitempty"`
}
type QwenASRAnnotation struct {
	Type     string `json:"type"`
	Language string `json:"language,omitempty"`
	Emotion  string `json:"emotion,omitempty"`
}
type QwenASRResult struct {
	Completed   bool
	Transcript  string
	Annotations []QwenASRAnnotation
	Seconds     metering.Quantity
	UsageState  string
	RequestID   string
	Raw         []byte
	SHA256      [32]byte
}

type qwenASRPacket struct {
	ID      string `json:"id"`
	Model   string `json:"model"`
	Choices []struct {
		Index        int    `json:"index"`
		FinishReason string `json:"finish_reason"`
		Message      struct {
			Role        string              `json:"role"`
			Content     string              `json:"content"`
			Annotations []QwenASRAnnotation `json:"annotations"`
		} `json:"message"`
	} `json:"choices"`
	Usage *struct {
		Seconds any `json:"seconds"`
	} `json:"usage"`
}

func DecodeQwenASRRequest(body []byte) (QwenASRRequest, error) {
	var r QwenASRRequest
	if len(body) == 0 || len(body) > 12<<20 || !utf8.Valid(body) || common.UnmarshalUnique(body, &r) != nil {
		return r, errors.New("invalid qwen asr request")
	}
	if strings.TrimSpace(r.Model) == "" || len(r.Model) > 255 || r.Stream || r.StreamOptions != nil || len(r.Messages) < 1 || len(r.Messages) > 2 {
		return r, errors.New("invalid qwen asr request")
	}
	start := 0
	if len(r.Messages) == 2 {
		if r.Messages[0].Role != "system" || len(r.Messages[0].Content) != 1 || r.Messages[0].Content[0].Type != "text" {
			return r, errors.New("invalid qwen asr context")
		}
		start = 1
	}
	msg := r.Messages[start]
	if msg.Role != "user" || len(msg.Content) != 1 || msg.Content[0].Type != "input_audio" || msg.Content[0].InputAudio == nil || !strings.HasPrefix(msg.Content[0].InputAudio.Data, "data:audio/") {
		return r, errors.New("invalid qwen asr audio")
	}
	if len(msg.Content[0].InputAudio.Data) > 10<<20 || !utf8.ValidString(msg.Content[0].InputAudio.Data) {
		return r, errors.New("invalid qwen asr audio")
	}
	if r.ASROptions != nil {
		if r.ASROptions.Language != nil && !qwenASRLanguage(*r.ASROptions.Language) {
			return r, errors.New("invalid qwen asr language")
		}
	}
	if _, err := r.AudioSeconds(); err != nil {
		return r, err
	}
	return r, nil
}

// AudioSeconds validates the single input_audio part as bounded inline PCM WAV
// and returns its measured duration. It runs before admission and before send.
func (r QwenASRRequest) AudioSeconds() (float64, error) {
	start := 0
	if len(r.Messages) == 2 {
		start = 1
	}
	if start >= len(r.Messages) || len(r.Messages[start].Content) != 1 || r.Messages[start].Content[0].InputAudio == nil {
		return 0, errors.New("invalid qwen asr audio")
	}
	return qwenASRAudioSeconds(r.Messages[start].Content[0].InputAudio.Data)
}
func qwenASRAudioSeconds(dataURI string) (float64, error) {
	fact, err := mediafacts.DecodeMediaFact("audio", dataURI)
	if err != nil || fact.Format != "wav" || fact.Seconds <= 0 {
		return 0, errors.New("invalid qwen asr audio")
	}
	return fact.Seconds, nil
}
func qwenASRLanguage(v string) bool {
	switch v {
	case "zh", "yue", "en", "ja", "de", "ko", "ru", "fr", "pt", "ar", "it", "es", "hi", "id", "th", "tr", "uk", "vi", "cs", "da", "fil", "fi", "is", "ms", "no", "pl", "sv":
		return true
	}
	return false
}
func (r QwenASRRequest) Validate() error {
	b, e := common.Marshal(r)
	if e != nil {
		return e
	}
	_, e = DecodeQwenASRRequest(b)
	return e
}

func parseQwenSeconds(v any) (metering.Quantity, error) {
	var raw string
	switch x := v.(type) {
	case string:
		raw = x
	case json.Number:
		raw = x.String()
	case float64:
		if math.IsNaN(x) || math.IsInf(x, 0) {
			return metering.Quantity{}, errors.New("invalid seconds")
		}
		raw = strconv.FormatFloat(x, 'f', -1, 64)
	default:
		return metering.Quantity{}, errors.New("invalid seconds")
	}
	if !qwenASRSeconds.MatchString(raw) {
		return metering.Quantity{}, errors.New("invalid seconds")
	}
	parts := strings.SplitN(raw, ".", 2)
	value := strings.TrimLeft(parts[0]+func() string {
		if len(parts) == 2 {
			return parts[1]
		}
		return ""
	}(), "0")
	if value == "" {
		value = "0"
	}
	scale := 0
	if len(parts) == 2 {
		scale = len(parts[1])
	}
	if value == "0" {
		return metering.Quantity{}, errors.New("seconds must be positive")
	}
	return metering.Quantity{Value: value, Scale: scale}, nil
}
func ParseQwenASRResponse(raw []byte) (QwenASRResult, error) {
	var p qwenASRPacket
	out := QwenASRResult{Raw: append([]byte(nil), raw...), SHA256: sha256.Sum256(raw), UsageState: "missing"}
	if len(raw) == 0 || len(raw) > 4<<20 || !utf8.Valid(raw) || common.UnmarshalUnique(raw, &p) != nil || len(p.Choices) != 1 || p.Choices[0].Index != 0 || p.Choices[0].FinishReason != "stop" || p.Choices[0].Message.Role != "assistant" || strings.TrimSpace(p.Choices[0].Message.Content) == "" {
		return out, errors.New("invalid qwen asr response")
	}
	if p.Usage == nil {
		return out, errors.New("missing qwen asr usage")
	}
	q, e := parseQwenSeconds(p.Usage.Seconds)
	if e != nil {
		out.UsageState = "invalid"
		return out, e
	}
	out.Completed = true
	out.Transcript = p.Choices[0].Message.Content
	out.Annotations = p.Choices[0].Message.Annotations
	out.Seconds = q
	out.UsageState = "confirmed"
	if p.ID != "" {
		out.RequestID = p.ID
	}
	return out, nil
}
func (p *Producer) SendQwenASR(ctx context.Context, r QwenASRRequest) (QwenASRResult, error) {
	out := QwenASRResult{UsageState: "missing"}
	if r.Validate() != nil {
		return out, errors.New("invalid qwen asr request")
	}
	body, e := common.Marshal(r)
	if e != nil || len(body) > 12<<20 {
		return out, errors.New("invalid qwen asr request")
	}
	req, e := http.NewRequestWithContext(ctx, http.MethodPost, p.target.Origin+"/compatible-mode/v1/chat/completions", bytes.NewReader(body))
	if e != nil {
		return out, e
	}
	req.Header.Set("Authorization", "Bearer "+p.target.Credential)
	req.Header.Set("Content-Type", "application/json")
	resp, e := p.client.Do(req)
	if e != nil {
		return out, e
	}
	defer resp.Body.Close()
	raw, e := io.ReadAll(io.LimitReader(resp.Body, 4<<20+1))
	if e != nil || len(raw) > 4<<20 || resp.StatusCode != http.StatusOK {
		return out, errors.New("qwen asr provider response unavailable")
	}
	out, e = ParseQwenASRResponse(raw)
	return out, e
}

// NewQwenASRProducer creates the closed DashScope-compatible Qwen ASR producer.
func NewQwenASRProducer(origin, credential string) (*Producer, error) {
	if strings.TrimSpace(credential) == "" || len(credential) > 4096 || strings.ContainsAny(credential, "\r\n") {
		return nil, errors.New("invalid qwen asr credential")
	}
	u, e := url.Parse(origin)
	if e != nil || u.Scheme != "https" || u.Host == "" || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return nil, errors.New("invalid qwen asr origin")
	}
	return &Producer{target: Target{Origin: origin, Credential: credential}, client: &http.Client{Timeout: 120 * time.Second}}, nil
}

// NewQwenASRProducerWithTransport swaps a trusted in-process transport after
// the exact production origin and credential validation. It is a test seam
// only and is not reachable through request fields, deployment JSON or env.
func NewQwenASRProducerWithTransport(origin, credential string, transport http.RoundTripper) (*Producer, error) {
	p, err := NewQwenASRProducer(origin, credential)
	if err != nil {
		return nil, err
	}
	if transport == nil {
		return nil, errors.New("invalid qwen asr transport")
	}
	p.client.Transport = transport
	return p, nil
}
