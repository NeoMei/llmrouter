package platformbilling

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/mediafacts"
	"github.com/pdfcpu/pdfcpu/pkg/api"
	pdfmodel "github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"strings"
	"time"
	"unicode/utf8"
)

// NativeInputPolicy is a deployment-certified contract for one exact public
// model/wire/producer/upstream target. HardInputTokens must cover the provider's
// entire accepted context, including images/documents and opaque history; it is
// never an estimated per-image token cost. Only flat token tariffs are supported.
type NativeInputPolicy struct {
	Version           string `json:"version"`
	ProviderReference string `json:"provider_reference"`
	ChannelType       int    `json:"channel_type"`
	UpstreamModel     string `json:"upstream_model"`
	HardInputTokens   int    `json:"hard_input_tokens"`
	MaxBytes          int    `json:"max_bytes"`
	MaxImages         int    `json:"max_images"`
	MaxPixels         int64  `json:"max_pixels"`
	MaxDocuments      int    `json:"max_documents"`
	MaxPages          int    `json:"max_pages"`
}

func (p NativeInputPolicy) valid(c Config, wire string) bool {
	_, proven := TokenProducer(wire, p.ChannelType)
	return proven && wire != "embeddings.create" && p.Version == "hard_context_v1" && strings.HasPrefix(p.ProviderReference, "https://") && p.UpstreamModel != "" && (wire != "gemini.generate" || !strings.ContainsAny(p.UpstreamModel, "/:?#%")) && p.HardInputTokens > 0 && p.HardInputTokens <= c.MaxInputTokens && p.MaxBytes > 0 && p.MaxBytes <= 10<<20 && p.MaxImages >= 0 && p.MaxImages <= 100 && p.MaxPixels > 0 && p.MaxPixels <= 16<<20 && p.MaxDocuments >= 0 && p.MaxDocuments <= 16 && p.MaxPages > 0 && p.MaxPages <= 100
}

type frozenMediaFact struct {
	SHA256 string `json:"sha256"`
	MIME   string `json:"mime"`
	Bytes  int    `json:"bytes"`
	Width  int    `json:"width,omitempty"`
	Height int    `json:"height,omitempty"`
	Pages  int    `json:"pages,omitempty"`
}
type frozenMediaReservation struct {
	CompactPolicy *CompactPolicy    `json:"compact_policy,omitempty"`
	Policy        NativeInputPolicy `json:"policy"`
	Facts         []frozenMediaFact `json:"facts"`
}
type mediaCarrier struct {
	mime, data string
	plain      bool
}

// Collect only protocol content containers. Schemas, tool arguments, metadata
// and opaque result JSON are never searched for media-looking keys.
func nativeContentMedia(wire string, request map[string]any) ([]mediaCarrier, error) {
	carriers := []mediaCarrier{}
	var content func(any) error
	content = func(value any) error {
		parts, ok := value.([]any)
		if !ok {
			return nil
		}
		for _, v := range parts {
			p, ok := v.(map[string]any)
			if !ok {
				return ErrInvalid
			}
			carrier := mediaCarrier{}
			switch p["type"] {
			case "image_url":
				im, _ := p["image_url"].(map[string]any)
				carrier.data, _ = im["url"].(string)
			case "input_image":
				carrier.data, _ = p["image_url"].(string)
			case "file":
				f, _ := p["file"].(map[string]any)
				carrier.data, _ = f["file_data"].(string)
			case "input_file":
				carrier.data, _ = p["file_data"].(string)
			case "image", "document":
				src, _ := p["source"].(map[string]any)
				if src["type"] != "base64" && src["type"] != "text" {
					return errNativeMediaReservationUnavailable
				}
				carrier.mime, _ = src["media_type"].(string)
				carrier.data, _ = src["data"].(string)
				carrier.plain = src["type"] == "text"
			case "input_audio", "video_url":
				return errNativeMediaReservationUnavailable
			case "tool_result":
				if err := content(p["content"]); err != nil {
					return err
				}
				continue
			default:
				continue
			}
			if carrier.data == "" {
				return errNativeMediaReservationUnavailable
			}
			carriers = append(carriers, carrier)
		}
		return nil
	}
	if wire == "gemini.generate" {
		var parts func(any) error
		parts = func(value any) error {
			list, _ := value.([]any)
			for _, v := range list {
				p, _ := v.(map[string]any)
				if _, ok := p["fileData"]; ok {
					return errNativeMediaReservationUnavailable
				}
				inline, ok := p["inlineData"]
				if !ok {
					inline, ok = p["inline_data"]
				}
				if ok {
					data, _ := inline.(map[string]any)
					mime, _ := data["mimeType"].(string)
					if mime == "" {
						mime, _ = data["mime_type"].(string)
					}
					text, _ := data["data"].(string)
					carriers = append(carriers, mediaCarrier{mime: mime, data: text})
				}
				if response, ok := p["functionResponse"].(map[string]any); ok {
					if err := parts(response["parts"]); err != nil {
						return err
					}
				}
			}
			return nil
		}
		contents, _ := request["contents"].([]any)
		for _, v := range contents {
			m, _ := v.(map[string]any)
			if err := parts(m["parts"]); err != nil {
				return nil, err
			}
		}
	} else {
		key := "messages"
		if wire == "responses.create" || wire == "responses.compact" {
			key = "input"
		}
		messages, _ := request[key].([]any)
		for _, v := range messages {
			m, _ := v.(map[string]any)
			if (wire == "responses.create" || wire == "responses.compact") && (m["type"] == "function_call_output" || m["type"] == "custom_tool_call_output") {
				if err := content(m["output"]); err != nil {
					return nil, err
				}
			}
			if err := content(m["content"]); err != nil {
				return nil, err
			}
		}
		if err := content(request["system"]); err != nil {
			return nil, err
		}
	}
	return carriers, nil
}

func nativeMediaReservation(c Config, wire, model string, request map[string]any) (int, string, error) {
	carriers, err := nativeContentMedia(wire, request)
	if err != nil {
		return 0, "", err
	}
	storedContext := request["previous_response_id"] != nil
	if wire == "claude.messages.create" {
		messages, _ := request["messages"].([]any)
		for _, value := range messages {
			message, _ := value.(map[string]any)
			parts, _ := message["content"].([]any)
			for _, value := range parts {
				part, _ := value.(map[string]any)
				storedContext = storedContext || part["type"] == "redacted_thinking"
			}
		}
	}
	if wire == "responses.create" || wire == "responses.compact" {
		if input, ok := request["input"].([]any); ok {
			for _, v := range input {
				item, _ := v.(map[string]any)
				storedContext = storedContext || item["type"] == "item_reference" || (len(item) == 1 && item["id"] != nil) || item["encrypted_content"] != nil
			}
		}
	}
	if len(carriers) == 0 && !storedContext {
		return c.MaxInputTokens, "", nil
	}
	policy, ok := c.NativeInputPolicies[wire][model]
	if !ok || !policy.valid(c, wire) {
		return 0, "", errNativeMediaReservationUnavailable
	}
	snapshot := frozenMediaReservation{Policy: policy}
	total, images, documents, pages := 0, 0, 0, 0
	var pixels int64
	for _, carrier := range carriers {
		encoded := carrier.data
		if strings.HasPrefix(encoded, "data:") {
			head, data, ok := strings.Cut(encoded, ",")
			if !ok || !strings.HasSuffix(head, ";base64") {
				return 0, "", ErrInvalid
			}
			mime := strings.TrimSuffix(strings.TrimPrefix(head, "data:"), ";base64")
			if carrier.mime != "" && carrier.mime != mime {
				return 0, "", ErrInvalid
			}
			carrier.mime = mime
			encoded = data
		}
		if len(encoded) > base64.StdEncoding.EncodedLen(policy.MaxBytes) {
			return 0, "", ErrInvalid
		}
		var raw []byte
		if carrier.plain {
			raw = []byte(encoded)
		} else {
			raw, err = base64.StdEncoding.Strict().DecodeString(encoded)
			if err != nil {
				return 0, "", ErrInvalid
			}
		}
		total += len(raw)
		if len(raw) == 0 || total > policy.MaxBytes {
			return 0, "", ErrInvalid
		}
		sum := sha256.Sum256(raw)
		fact := frozenMediaFact{SHA256: hex.EncodeToString(sum[:]), MIME: carrier.mime, Bytes: len(raw)}
		switch carrier.mime {
		case "image/png", "image/jpeg":
			im, err := mediafacts.DecodeMediaFact("image", encoded)
			if err != nil || "image/"+im.Format != carrier.mime {
				return 0, "", ErrInvalid
			}
			images++
			pixels += im.Pixels
			fact.Width, fact.Height = im.Width, im.Height
			if images > policy.MaxImages || pixels > policy.MaxPixels {
				return 0, "", ErrInvalid
			}
		case "application/pdf":
			documents++
			if documents > policy.MaxDocuments || !bytes.HasPrefix(raw, []byte("%PDF-")) {
				return 0, "", ErrInvalid
			}
			conf := pdfmodel.NewStatelessConfiguration()
			conf.Offline = true
			conf.Limits = pdfmodel.ResourceLimits{MaxInputBytes: int64(policy.MaxBytes), MaxObjectBytes: 4 << 20, MaxStreamBytes: 10 << 20, MaxDecodeBytes: 32 << 20, MaxImagePixels: 16 << 20, MaxImageBytes: 64 << 20, MaxObjectCount: 10000, MaxObjectStreamCount: 1000, MaxObjectStreamFirst: 1 << 20, MaxXRefEntries: 10000, MaxRecursionDepth: 32}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			count, err := api.PageCount(ctx, bytes.NewReader(raw), conf)
			cancel()
			if err != nil || count < 1 {
				return 0, "", ErrInvalid
			}
			pages += count
			fact.Pages = count
			if pages > policy.MaxPages {
				return 0, "", ErrInvalid
			}
		case "text/plain":
			documents++
			if documents > policy.MaxDocuments || !utf8.Valid(raw) {
				return 0, "", ErrInvalid
			}
		default:
			return 0, "", errNativeMediaReservationUnavailable
		}
		snapshot.Facts = append(snapshot.Facts, fact)
	}
	raw, err := common.Marshal(snapshot)
	if err != nil {
		return 0, "", ErrInvalid
	}
	return policy.HardInputTokens, string(raw), nil
}
