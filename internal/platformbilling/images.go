package platformbilling

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	_ "golang.org/x/image/webp"
	"image"
	"image/color"
	_ "image/jpeg"
	"image/png"
	"io"
	"mime"
	"mime/multipart"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/pkg/metering"
	"gorm.io/gorm"
)

// ImagePolicy is an operator-owned exact producer certificate. Count is a
// reservation cap; only complete decoded terminal outputs can become usage.
type ImagePolicy struct {
	Version          string            `json:"version"`
	PublicModel      string            `json:"public_model"`
	WireOperation    string            `json:"wire_operation"`
	ChannelID        int               `json:"channel_id"`
	ChannelType      int               `json:"channel_type"`
	UpstreamModel    string            `json:"upstream_model"`
	ChannelKeySHA256 string            `json:"channel_key_sha256"`
	Origin           string            `json:"origin"`
	APIVersion       string            `json:"api_version"`
	MaxBodyBytes     int64             `json:"max_body_bytes"`
	MaxImageBytes    int               `json:"max_image_bytes"`
	MaxPixels        int               `json:"max_pixels"`
	MaxImages        int               `json:"max_images"`
	MaxInputImages   int               `json:"max_input_images"`
	ResourceOrigins  []string          `json:"resource_origins,omitempty"`
	RateIDs          map[string]string `json:"rate_ids"`
}
type frozenImage struct {
	Policy ImagePolicy `json:"policy"`
}
type ImageOutput struct {
	SHA256 string `json:"sha256"`
	MIME   string `json:"mime"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
	Bytes  int    `json:"bytes"`
}
type ImageResult struct {
	Body    []byte
	Usage   json.RawMessage
	Outputs []ImageOutput
	SHA256  string
}

func IsImageWire(w string) bool {
	return w == "images.generations.create" || w == "images.edits.create" || w == "dashscope.images.generate" || w == "gemini.images.generate"
}
func imageOrigin(origin string) bool {
	u, e := url.Parse(origin)
	return e == nil && u.Scheme == "https" && u.Host != "" && u.User == nil && u.Path == "" && u.RawQuery == "" && u.Fragment == ""
}
func (p ImagePolicy) valid(model, wire string) bool {
	key, e := hex.DecodeString(p.ChannelKeySHA256)
	if e != nil || len(key) != 32 || p.Version != "image_v1" || p.PublicModel != model || !protocolString(model) || p.WireOperation != wire || !IsImageWire(wire) || p.ChannelID < 1 || p.UpstreamModel == "" || strings.ContainsAny(p.UpstreamModel, "/:?#%") || !imageOrigin(p.Origin) || p.MaxBodyBytes < 1 || p.MaxBodyBytes > 64<<20 || p.MaxImageBytes < 1 || p.MaxImageBytes > 10<<20 || p.MaxPixels < 1 || p.MaxPixels > 16<<20 || p.MaxImages < 1 || p.MaxImages > dto.MaxImageN || p.MaxInputImages < 0 || p.MaxInputImages > 16 || len(p.RateIDs) == 0 {
		return false
	}
	for asset, rate := range p.RateIDs {
		if (asset != "CNY/6" && asset != "CREDIT/6") || rate == "" {
			return false
		}
	}
	for _, origin := range p.ResourceOrigins {
		if !imageOrigin(origin) {
			return false
		}
	}
	switch wire {
	case "gemini.images.generate":
		return p.ChannelType == constant.ChannelTypeGemini && p.Origin == "https://generativelanguage.googleapis.com" && p.APIVersion == "v1beta"
	case "dashscope.images.generate":
		return p.ChannelType == constant.ChannelTypeAli && p.Origin == "https://dashscope.aliyuncs.com" && p.APIVersion == "v1" && (p.UpstreamModel == "qwen-image-2.0" || p.UpstreamModel == "qwen-image-2.0-pro")
	case "images.edits.create":
		if p.ChannelType == constant.ChannelTypeAli {
			return p.Origin == "https://dashscope.aliyuncs.com" && p.APIVersion == "v1" && p.UpstreamModel == "wanx2.1-imageedit" && p.MaxInputImages == 1 && len(p.ResourceOrigins) > 0
		}
		return p.ChannelType == constant.ChannelTypeOpenAI && p.Origin == "https://api.openai.com" && p.APIVersion == "v1"
	default:
		if p.ChannelType == constant.ChannelTypeAli {
			return p.Origin == "https://dashscope.aliyuncs.com" && p.APIVersion == "v1" && (p.UpstreamModel == "qwen-image-2.0" || p.UpstreamModel == "qwen-image-2.0-pro")
		}
		return (p.ChannelType == constant.ChannelTypeOpenAI && p.Origin == "https://api.openai.com" && p.APIVersion == "v1") || (p.ChannelType == constant.ChannelTypeVolcEngine && p.Origin == "https://ark.cn-beijing.volces.com" && p.APIVersion == "v3")
	}
}
func imageBytes(p ImagePolicy, raw []byte, declared string) (ImageOutput, error) {
	if len(raw) == 0 || len(raw) > p.MaxImageBytes {
		return ImageOutput{}, ErrInvalid
	}
	config, format, e := image.DecodeConfig(bytes.NewReader(raw))
	if e != nil || config.Width < 1 || config.Height < 1 || int64(config.Width)*int64(config.Height) > int64(p.MaxPixels) {
		return ImageOutput{}, ErrInvalid
	}
	actual := "image/" + format
	if actual != "image/png" && actual != "image/jpeg" && actual != "image/webp" {
		return ImageOutput{}, ErrInvalid
	}
	if declared != "" && declared != actual {
		return ImageOutput{}, ErrInvalid
	}
	// Decode the full file, not just its dimensions. Reject trailing/truncated
	// container data and animation before accepting one successful image.
	if format == "png" {
		offset := 8
		ended := false
		for offset+12 <= len(raw) {
			length := uint64(binary.BigEndian.Uint32(raw[offset : offset+4]))
			end := uint64(offset) + 12 + length
			if end > uint64(len(raw)) {
				return ImageOutput{}, ErrInvalid
			}
			kind := string(raw[offset+4 : offset+8])
			if kind == "acTL" {
				return ImageOutput{}, ErrInvalid
			}
			if kind == "IEND" {
				if length != 0 || end != uint64(len(raw)) {
					return ImageOutput{}, ErrInvalid
				}
				ended = true
				break
			}
			offset = int(end)
		}
		if !ended {
			return ImageOutput{}, ErrInvalid
		}
	}
	if format == "jpeg" && !completeJPEG(raw) {
		return ImageOutput{}, ErrInvalid
	}
	if format == "webp" && !completeStillWebP(raw, config.Width, config.Height) {
		return ImageOutput{}, ErrInvalid
	}

	decoded, _, e := image.Decode(bytes.NewReader(raw))
	if e != nil || decoded.Bounds().Dx() != config.Width || decoded.Bounds().Dy() != config.Height {
		return ImageOutput{}, ErrInvalid
	}
	sum := sha256.Sum256(raw)
	return ImageOutput{hex.EncodeToString(sum[:]), actual, config.Width, config.Height, len(raw)}, nil
}
func imageData(p ImagePolicy, data string) (ImageOutput, error) {
	prefix, b64, ok := strings.Cut(data, ",")
	if !ok || !strings.HasPrefix(prefix, "data:image/") || !strings.HasSuffix(prefix, ";base64") {
		return ImageOutput{}, ErrInvalid
	}
	raw, e := base64.StdEncoding.Strict().DecodeString(b64)
	if e != nil {
		return ImageOutput{}, ErrInvalid
	}
	return imageBytes(p, raw, strings.TrimSuffix(strings.TrimPrefix(prefix, "data:"), ";base64"))
}

// wanMask converts the OpenAI transparent edit region to DashScope's opaque
// white edit region. Reject partial transparency rather than changing its area.
func wanMask(p ImagePolicy, raw []byte) ([]byte, error) {
	if len(raw) < 26 || !bytes.Equal(raw[:8], []byte("\x89PNG\r\n\x1a\n")) {
		return nil, ErrInvalid
	}
	colorType := raw[25]
	hasAlpha := colorType == 4 || colorType == 6
	if colorType == 0 || colorType == 2 || colorType == 3 {
		for offset := 8; offset+12 <= len(raw); {
			length := uint64(binary.BigEndian.Uint32(raw[offset : offset+4]))
			end := uint64(offset) + 12 + length
			if end > uint64(len(raw)) {
				return nil, ErrInvalid
			}
			if string(raw[offset+4:offset+8]) == "tRNS" {
				hasAlpha = true
			}
			offset = int(end)
		}
	}
	if !hasAlpha {
		return nil, ErrInvalid
	}
	source, err := png.Decode(bytes.NewReader(raw))
	if err != nil {
		return nil, ErrInvalid
	}
	bounds := source.Bounds()
	mask := image.NewGray(bounds)
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			_, _, _, alpha := source.At(x, y).RGBA()
			switch alpha {
			case 0:
				mask.SetGray(x, y, color.Gray{Y: 255})
			case 0xffff:
				mask.SetGray(x, y, color.Gray{Y: 0})
			default:
				return nil, ErrInvalid
			}
		}
	}
	var encoded bytes.Buffer
	if png.Encode(&encoded, mask) != nil || encoded.Len() > p.MaxImageBytes {
		return nil, ErrInvalid
	}
	return encoded.Bytes(), nil
}
func parseImageOperation(p ImagePolicy, wire WireOperation, ct string, body []byte) (operationRequest, error) {
	bad := operationRequest{}
	if !p.valid(p.PublicModel, wire.ID) || int64(len(body)) > p.MaxBodyBytes {
		return bad, ErrInvalid
	}
	var req map[string]any
	var canonical []byte
	media, params, e := mime.ParseMediaType(ct)
	if e != nil {
		return bad, ErrInvalid
	}
	if media == "multipart/form-data" && wire.ID == "images.edits.create" {
		// The exact submitted file order/name/MIME/bytes participates in identity;
		// the randomly selected multipart boundary does not.
		type partRecord struct{ Name, Filename, MIME, Data string }
		parts := []partRecord{}
		fields := map[string]bool{}
		req = map[string]any{}
		reader := multipart.NewReader(bytes.NewReader(body), params["boundary"])
		var primary, mask *ImageOutput
		for i := 0; ; i++ {
			part, e := reader.NextRawPart()
			if e == io.EOF {
				break
			}
			if e != nil || i >= 20 {
				return bad, ErrInvalid
			}
			name := part.FormName()
			if name == "" || fields[name] {
				return bad, ErrInvalid
			}
			fields[name] = true
			for name, values := range part.Header {
				if len(values) != 1 || (name != "Content-Disposition" && name != "Content-Type") {
					return bad, ErrInvalid
				}
			}
			disposition, attributes, e := mime.ParseMediaType(part.Header.Get("Content-Disposition"))
			if e != nil || disposition != "form-data" || len(attributes) > 2 {
				return bad, ErrInvalid
			}
			data, e := io.ReadAll(io.LimitReader(part, int64(p.MaxImageBytes)+1))
			if e != nil || len(data) == 0 || len(data) > p.MaxImageBytes {
				return bad, ErrInvalid
			}
			filename := part.FileName()
			if attributes["filename"] != filename || strings.ContainsAny(filename, `/\`) {
				return bad, ErrInvalid
			}
			if filename != "" {
				if part.Header.Get("Content-Type") == "" {
					return bad, ErrInvalid
				}
				if name != "image" && name != "mask" {
					return bad, ErrInvalid
				}
				fact, e := imageBytes(p, data, part.Header.Get("Content-Type"))
				if e != nil {
					return bad, e
				}
				if p.ChannelType == constant.ChannelTypeAli && name == "mask" {
					if part.Header.Get("Content-Type") != "image/png" {
						return bad, ErrInvalid
					}
					if _, e = wanMask(p, data); e != nil {
						return bad, e
					}
				}
				if name == "image" {
					primary = &fact
				} else {
					mask = &fact
				}
				req[name] = "@file"
				parts = append(parts, partRecord{name, filename, part.Header.Get("Content-Type"), base64.StdEncoding.EncodeToString(data)})
			} else {
				if !utf8.Valid(data) || len(data) > 65536 || part.Header.Get("Content-Type") != "" {
					return bad, ErrInvalid
				}
				if name == "n" || name == "output_compression" {
					req[name] = json.Number(string(data))
				} else {
					req[name] = string(data)
				}
				parts = append(parts, partRecord{Name: name, Data: string(data)})
			}
		}
		if primary == nil || p.MaxInputImages < 1 || (mask != nil && (primary.Width != mask.Width || primary.Height != mask.Height)) || (p.ChannelType == constant.ChannelTypeAli && mask == nil) {
			return bad, ErrInvalid
		}
		if p.ChannelType == constant.ChannelTypeAli && (primary.Width < 512 || primary.Width > 4096 || primary.Height < 512 || primary.Height > 4096) {
			return bad, ErrInvalid
		}
		canonical, e = common.Marshal(parts)
	} else if media == "application/json" {
		if !utf8.Valid(body) || common.UnmarshalUnique(body, &req) != nil || req == nil {
			return bad, ErrInvalid
		}
		canonical, e = common.Marshal(req)
	} else {
		return bad, ErrInvalid
	}
	if e != nil {
		return bad, ErrInvalid
	}
	quantity := 1
	if wire.ID == "gemini.images.generate" {
		if wire.PathModel != p.PublicModel || wire.Mode != "generateContent" || !protocolKeys(req, "contents generationConfig systemInstruction safetySettings") {
			return bad, ErrInvalid
		}
		config, ok := req["generationConfig"].(map[string]any)
		if !ok || !protocolKeys(config, "responseModalities responseFormat temperature topP topK candidateCount seed") || !protocolNumberFields(config, map[string][2]float64{"temperature": {0, 2}, "topP": {0, 1}, "topK": {1, 1000}, "seed": {0, 2147483647}}) {
			return bad, ErrInvalid
		}
		modalities, ok := config["responseModalities"].([]any)
		if !ok || len(modalities) != 1 || modalities[0] != "IMAGE" {
			return bad, ErrInvalid
		}
		format, ok := config["responseFormat"].(map[string]any)
		if !ok || !protocolKeys(format, "image") {
			return bad, ErrInvalid
		}
		im, ok := format["image"].(map[string]any)
		if !ok || !protocolKeys(im, "aspectRatio imageSize mimeType") || !protocolEnum(im, "aspectRatio", "ASPECT_RATIO_ONE_BY_ONE ASPECT_RATIO_TWO_BY_THREE ASPECT_RATIO_THREE_BY_TWO ASPECT_RATIO_THREE_BY_FOUR ASPECT_RATIO_FOUR_BY_THREE ASPECT_RATIO_FOUR_BY_FIVE ASPECT_RATIO_FIVE_BY_FOUR ASPECT_RATIO_NINE_BY_SIXTEEN ASPECT_RATIO_SIXTEEN_BY_NINE ASPECT_RATIO_TWENTY_ONE_BY_NINE ASPECT_RATIO_ONE_BY_EIGHT ASPECT_RATIO_EIGHT_BY_ONE ASPECT_RATIO_ONE_BY_FOUR ASPECT_RATIO_FOUR_BY_ONE") || !protocolEnum(im, "imageSize", "IMAGE_SIZE_FIVE_TWELVE IMAGE_SIZE_ONE_K IMAGE_SIZE_TWO_K IMAGE_SIZE_FOUR_K") || !protocolEnum(im, "mimeType", "IMAGE_JPEG") {
			return bad, ErrInvalid
		}
		contents, ok := req["contents"].([]any)
		if !ok || len(contents) == 0 || len(contents) > 16 {
			return bad, ErrInvalid
		}
		count := 0
		for _, v := range contents {
			content, ok := v.(map[string]any)
			if !ok || !protocolKeys(content, "role parts") || !protocolEnum(content, "role", "user model") {
				return bad, ErrInvalid
			}
			parts, ok := content["parts"].([]any)
			if !ok || len(parts) == 0 || len(parts) > 32 {
				return bad, ErrInvalid
			}
			for _, v := range parts {
				part, ok := v.(map[string]any)
				if !ok || len(part) != 1 {
					return bad, ErrInvalid
				}
				if text, ok := part["text"].(string); ok {
					if text == "" || len(text) > 65536 {
						return bad, ErrInvalid
					}
					continue
				}
				in, ok := part["inlineData"].(map[string]any)
				if !ok || !protocolKeys(in, "mimeType data") {
					return bad, ErrInvalid
				}
				b64, ok := in["data"].(string)
				if !ok {
					return bad, ErrInvalid
				}
				data, e := base64.StdEncoding.Strict().DecodeString(b64)
				mime, _ := in["mimeType"].(string)
				if e != nil || mime == "" {
					return bad, ErrInvalid
				}
				if _, e = imageBytes(p, data, mime); e != nil {
					return bad, e
				}
				count++
			}
		}
		if count > p.MaxInputImages {
			return bad, ErrInvalid
		}
		if _, exists := req["systemInstruction"]; exists {
			return bad, ErrInvalid
		}
		if _, exists := req["safetySettings"]; exists {
			return bad, ErrInvalid
		}
		for _, key := range []string{"seed", "topK"} {
			if v, exists := config[key]; exists {
				if _, e := confirmedCount(v); e != nil {
					return bad, ErrInvalid
				}
			}
		}
		if v, exists := config["candidateCount"]; exists {
			n, e := confirmedCount(v)
			if e != nil || n != 1 {
				return bad, ErrInvalid
			}
		}
		quantity = p.MaxImages
	} else {
		if req["model"] != p.PublicModel {
			return bad, ErrInvalid
		}
		if wire.ID == "dashscope.images.generate" {
			if !protocolKeys(req, "model input parameters") {
				return bad, ErrInvalid
			}
			input, ok := req["input"].(map[string]any)
			if !ok || !protocolKeys(input, "messages") {
				return bad, ErrInvalid
			}
			messages, ok := input["messages"].([]any)
			if !ok || len(messages) != 1 {
				return bad, ErrInvalid
			}
			message, ok := messages[0].(map[string]any)
			if !ok || !protocolKeys(message, "role content") || message["role"] != "user" {
				return bad, ErrInvalid
			}
			contents, ok := message["content"].([]any)
			if !ok || len(contents) == 0 || len(contents) > 17 {
				return bad, ErrInvalid
			}
			count, texts := 0, 0
			for _, v := range contents {
				part, ok := v.(map[string]any)
				if !ok || len(part) != 1 {
					return bad, ErrInvalid
				}
				if text, ok := part["text"].(string); ok {
					if text == "" || len(text) > 1300 {
						return bad, ErrInvalid
					}
					texts++
					continue
				}
				im, ok := part["image"].(string)
				if !ok {
					return bad, ErrInvalid
				}
				if _, e := imageData(p, im); e != nil {
					return bad, e
				}
				count++
			}
			if count > p.MaxInputImages || texts != 1 {
				return bad, ErrInvalid
			}
			parameters, ok := req["parameters"].(map[string]any)
			if !ok || !protocolKeys(parameters, "n size seed watermark prompt_extend") || !protocolBools(parameters, "watermark prompt_extend") || !protocolNumberFields(parameters, map[string][2]float64{"seed": {0, 2147483647}}) {
				return bad, ErrInvalid
			}
			req = parameters
		} else {
			fields := "model prompt n size quality style response_format background output_format output_compression moderation user image mask"
			if p.ChannelType == constant.ChannelTypeAli && wire.ID == "images.edits.create" {
				fields = "model prompt n image mask"
			} else if p.ChannelType == constant.ChannelTypeAli {
				fields = "model prompt n size response_format seed watermark prompt_extend stream"
				text, ok := req["prompt"].(string)
				if !ok || len(text) > 1300 || req["stream"] == true || !protocolBools(req, "prompt_extend") || !protocolEnum(req, "response_format", "url") {
					return bad, ErrInvalid
				}
			}
			if p.ChannelType == constant.ChannelTypeVolcEngine {
				fields = "model prompt image size seed watermark response_format stream sequential_image_generation sequential_image_generation_options"
			}
			if !protocolKeys(req, fields) || !protocolString(req["prompt"]) || len(req["prompt"].(string)) > 65536 || !protocolStrings(req, "size quality style response_format background output_format moderation user") || !protocolEnum(req, "response_format", "b64_json url") || !protocolNumberFields(req, map[string][2]float64{"output_compression": {0, 100}, "seed": {0, 2147483647}}) || !protocolBools(req, "watermark stream") {
				return bad, ErrInvalid
			}
			if p.ChannelType == constant.ChannelTypeVolcEngine {
				if req["stream"] == true || !protocolEnum(req, "sequential_image_generation", "disabled") {
					return bad, ErrInvalid
				}
				if _, exists := req["sequential_image_generation_options"]; exists {
					return bad, ErrInvalid
				}
				if value, exists := req["image"]; exists {
					refs := []any{value}
					if values, ok := value.([]any); ok {
						refs = values
					}
					if len(refs) == 0 || len(refs) > p.MaxInputImages {
						return bad, ErrInvalid
					}
					for _, v := range refs {
						s, ok := v.(string)
						if !ok {
							return bad, ErrInvalid
						}
						if _, e := imageData(p, s); e != nil {
							return bad, e
						}
					}
				}
			}
			if wire.ID == "images.edits.create" && media != "multipart/form-data" {
				return bad, ErrInvalid
			}
			if wire.ID == "images.generations.create" && p.ChannelType == constant.ChannelTypeOpenAI {
				if req["image"] != nil || req["mask"] != nil {
					return bad, ErrInvalid
				}
			}
		}
		if value, exists := req["n"]; exists {
			quantity, e = confirmedCount(value)
			if e != nil {
				return bad, e
			}
		}
		for _, key := range []string{"seed", "output_compression"} {
			if v, ok := req[key]; ok {
				n, e := confirmedCount(v)
				if e != nil || n > 2147483647 || (key == "output_compression" && n > 100) {
					return bad, ErrInvalid
				}
			}
		}
		if size, exists := req["size"]; exists {
			value, ok := size.(string)
			if !ok {
				return bad, ErrInvalid
			}
			if value != "auto" {
				if p.ChannelType == constant.ChannelTypeVolcEngine && (value == "1K" || value == "2K" || value == "4K") {
					pixels := 1024 * 1024
					if value == "2K" {
						pixels = 2048 * 2048
					}
					if value == "4K" {
						pixels = 4096 * 4096
					}
					if pixels > p.MaxPixels {
						return bad, ErrInvalid
					}
				} else {
					separator := "x"
					if p.WireOperation == "dashscope.images.generate" {
						separator = "*"
					}
					w, h, ok := strings.Cut(value, separator)
					width, e1 := strconv.ParseInt(w, 10, 32)
					height, e2 := strconv.ParseInt(h, 10, 32)
					if !ok || e1 != nil || e2 != nil || width < 1 || height < 1 || width*height > int64(p.MaxPixels) {
						return bad, ErrInvalid
					}
					if p.ChannelType == constant.ChannelTypeAli && (width*height < 512*512 || width*height > 2048*2048 || width%16 != 0 || height%16 != 0) {
						return bad, ErrInvalid
					}
				}
			}
		}
		if !protocolEnum(req, "quality", "auto low medium high standard hd") || !protocolEnum(req, "style", "vivid natural") || !protocolEnum(req, "background", "auto transparent opaque") || !protocolEnum(req, "output_format", "png jpeg webp") || !protocolEnum(req, "moderation", "auto low") {
			return bad, ErrInvalid
		}
		if quantity < 1 || quantity > p.MaxImages {
			return bad, ErrInvalid
		}
		if p.ChannelType == constant.ChannelTypeAli && wire.ID == "images.edits.create" && (quantity != 1 || utf8.RuneCountInString(req["prompt"].(string)) > 800) {
			return bad, ErrInvalid
		}
	}
	frozen, _ := common.Marshal(frozenImage{p})
	preview := &Session{canonical: canonical, call: Call{Model: p.PublicModel, WireOperation: wire.ID, MediaSnapshot: string(frozen)}}
	if _, _, _, e := preview.ImageBody(); e != nil {
		return bad, e
	}
	return operationRequest{Model: p.PublicModel, Canonical: canonical, Quantity: quantity, MediaSnapshot: string(frozen)}, nil
}
func protocolNumberFields(m map[string]any, bounds map[string][2]float64) bool {
	for k, b := range bounds {
		if v, ok := m[k]; ok && !protocolNumber(v, b[0], b[1]) {
			return false
		}
	}
	return true
}

// ObserveImageResult replaces certified remote resources with immutable inline
// bytes, retaining each provider's native envelope and usage lexemes.
func ObserveImageResult(p ImagePolicy, raw []byte, fetch func(string) ([]byte, string, error)) (ImageResult, error) {
	result := ImageResult{}
	if len(raw) == 0 || int64(len(raw)) > p.MaxBodyBytes || !utf8.Valid(raw) {
		return result, ErrInvalid
	}
	var envelope map[string]any
	if common.UnmarshalUnique(raw, &envelope) != nil || envelope == nil || envelope["error"] != nil || envelope["code"] != nil {
		return result, ErrInvalid
	}
	allowed := "data usage created background output_format size quality"
	if p.ChannelType == constant.ChannelTypeAli {
		allowed = "output usage request_id"
	}
	if p.WireOperation == "gemini.images.generate" {
		allowed = "candidates usageMetadata modelVersion responseId promptFeedback"
	}
	if !protocolKeys(envelope, allowed) || !imageOutputMetadata(envelope, p.MaxPixels) {
		return result, ErrInvalid
	}
	var lexical map[string]json.RawMessage
	if common.UnmarshalUnique(raw, &lexical) != nil {
		return result, ErrInvalid
	}
	result.Usage = lexical["usage"]
	if p.WireOperation == "gemini.images.generate" {
		result.Usage = lexical["usageMetadata"]
	}
	seen := map[string]bool{}
	var slots []map[string]any
	var sourceKeys []string
	if p.WireOperation == "gemini.images.generate" {
		if envelope["promptFeedback"] != nil {
			return result, ErrInvalid
		}
		candidates, ok := envelope["candidates"].([]any)
		if !ok || len(candidates) != 1 {
			return result, ErrInvalid
		}
		candidate, ok := candidates[0].(map[string]any)
		if !ok || !protocolKeys(candidate, "content finishReason index safetyRatings") || candidate["finishReason"] != "STOP" || !imageOutputMetadata(candidate, p.MaxPixels) {
			return result, ErrInvalid
		}
		if value, exists := candidate["safetyRatings"]; exists {
			ratings, ok := value.([]any)
			if !ok || len(ratings) > 32 {
				return result, ErrInvalid
			}
			for _, v := range ratings {
				rating, ok := v.(map[string]any)
				if !ok || !protocolKeys(rating, "category probability blocked") || !protocolString(rating["category"]) || !protocolString(rating["probability"]) || !protocolEnum(rating, "category", "HARM_CATEGORY_UNSPECIFIED HARM_CATEGORY_DEROGATORY HARM_CATEGORY_TOXICITY HARM_CATEGORY_VIOLENCE HARM_CATEGORY_SEXUAL HARM_CATEGORY_MEDICAL HARM_CATEGORY_DANGEROUS HARM_CATEGORY_HARASSMENT HARM_CATEGORY_HATE_SPEECH HARM_CATEGORY_SEXUALLY_EXPLICIT HARM_CATEGORY_DANGEROUS_CONTENT HARM_CATEGORY_CIVIC_INTEGRITY HARM_CATEGORY_JAILBREAK") || !protocolEnum(rating, "probability", "HARM_PROBABILITY_UNSPECIFIED NEGLIGIBLE LOW MEDIUM HIGH") || !protocolBools(rating, "blocked") || rating["blocked"] == true {
					return result, ErrInvalid
				}
			}
		}
		content, ok := candidate["content"].(map[string]any)
		if !ok || !protocolKeys(content, "role parts") || content["role"] != "model" {
			return result, ErrInvalid
		}
		parts, ok := content["parts"].([]any)
		if !ok {
			return result, ErrInvalid
		}
		for _, v := range parts {
			part, ok := v.(map[string]any)
			if !ok {
				return result, ErrInvalid
			}
			inline, ok := part["inlineData"].(map[string]any)
			if !ok || !protocolKeys(inline, "mimeType data") || len(part) != 1 {
				return result, ErrInvalid
			}
			slots = append(slots, inline)
			sourceKeys = append(sourceKeys, "data")
		}
	} else if p.ChannelType == constant.ChannelTypeAli {
		output, ok := envelope["output"].(map[string]any)
		if !ok || !protocolKeys(output, "choices") {
			return result, ErrInvalid
		}
		choices, ok := output["choices"].([]any)
		if !ok || len(choices) != 1 {
			return result, ErrInvalid
		}
		choice, ok := choices[0].(map[string]any)
		if !ok || !protocolKeys(choice, "finish_reason message") || choice["finish_reason"] != "stop" {
			return result, ErrInvalid
		}
		message, ok := choice["message"].(map[string]any)
		if !ok || !protocolKeys(message, "role content") || message["role"] != "assistant" {
			return result, ErrInvalid
		}
		parts, ok := message["content"].([]any)
		if !ok {
			return result, ErrInvalid
		}
		for _, v := range parts {
			part, ok := v.(map[string]any)
			if !ok || len(part) != 1 {
				return result, ErrInvalid
			}
			slots = append(slots, part)
			sourceKeys = append(sourceKeys, "image")
		}
	} else {
		data, ok := envelope["data"].([]any)
		if !ok {
			return result, ErrInvalid
		}
		for _, v := range data {
			part, ok := v.(map[string]any)
			if !ok || !protocolKeys(part, "b64_json url revised_prompt size") || part["error"] != nil || !imageOutputMetadata(part, p.MaxPixels) {
				return result, ErrInvalid
			}
			key := "b64_json"
			if part["url"] != nil {
				if part["b64_json"] != nil {
					return result, ErrInvalid
				}
				key = "url"
			}
			slots = append(slots, part)
			sourceKeys = append(sourceKeys, key)
		}
	}
	if len(slots) == 0 || len(slots) > p.MaxImages {
		return result, ErrInvalid
	}
	for i, slot := range slots {
		value, ok := slot[sourceKeys[i]].(string)
		if !ok || value == "" {
			return result, ErrInvalid
		}
		var data []byte
		var declared string
		var e error
		if strings.HasPrefix(value, "https://") {
			if fetch == nil {
				return result, ErrInvalid
			}
			u, e := url.Parse(value)
			allowed := false
			for _, origin := range p.ResourceOrigins {
				allowed = allowed || (e == nil && u.Scheme+"://"+u.Host == origin)
			}
			if !allowed || u.User != nil || u.Fragment != "" {
				return result, ErrInvalid
			}
			data, declared, e = fetch(value)
			if e != nil {
				return result, e
			}
		} else if strings.HasPrefix(value, "data:") {
			prefix, b64, ok := strings.Cut(value, ",")
			if !ok || !strings.HasSuffix(prefix, ";base64") {
				return result, ErrInvalid
			}
			declared = strings.TrimSuffix(strings.TrimPrefix(prefix, "data:"), ";base64")
			data, e = base64.StdEncoding.Strict().DecodeString(b64)
		} else {
			data, e = base64.StdEncoding.Strict().DecodeString(value)
			if sourceKeys[i] == "data" {
				declared, _ = slot["mimeType"].(string)
				if declared == "" {
					return result, ErrInvalid
				}
			}
		}
		if e != nil {
			return result, ErrInvalid
		}
		fact, e := imageBytes(p, data, declared)
		if e != nil || seen[fact.SHA256] {
			return result, ErrInvalid
		}
		seen[fact.SHA256] = true
		result.Outputs = append(result.Outputs, fact)
		b64 := base64.StdEncoding.EncodeToString(data)
		switch sourceKeys[i] {
		case "image":
			slot["image"] = "data:" + fact.MIME + ";base64," + b64
		case "url":
			delete(slot, "url")
			slot["b64_json"] = b64
		case "data":
			slot["data"] = b64
			slot["mimeType"] = fact.MIME
		default:
			slot["b64_json"] = b64
		}
	}
	if len(result.Usage) > 0 {
		var usage map[string]any
		if common.UnmarshalUnique(result.Usage, &usage) != nil || usage == nil || !imageUsage(p, usage) {
			return result, ErrInvalid
		}
		for _, key := range []string{"image_count", "generated_images"} {
			if v, ok := usage[key]; ok {
				count, e := confirmedCount(v)
				if e != nil || count != len(result.Outputs) {
					return result, ErrInvalid
				}
			}
		}
		if p.ChannelType == constant.ChannelTypeAli {
			count, e := confirmedCount(usage["image_count"])
			width, e2 := confirmedCount(usage["width"])
			height, e3 := confirmedCount(usage["height"])
			if e != nil || e2 != nil || e3 != nil || count != len(result.Outputs) {
				return result, ErrInvalid
			}
			for _, o := range result.Outputs {
				if width != o.Width || height != o.Height {
					return result, ErrInvalid
				}
			}
		}
	}
	if p.ChannelType == constant.ChannelTypeAli && len(result.Usage) == 0 {
		return result, ErrInvalid
	}
	if p.ChannelType == constant.ChannelTypeAli && p.WireOperation == "images.generations.create" {
		data := make([]any, 0, len(result.Outputs))
		for _, slot := range slots {
			_, b64, _ := strings.Cut(slot["image"].(string), ",")
			data = append(data, map[string]any{"b64_json": b64})
		}
		envelope = map[string]any{"data": data}
	}
	result.Body, _ = common.Marshal(envelope)
	if int64(len(result.Body)) > p.MaxBodyBytes {
		return ImageResult{}, ErrInvalid
	}
	sum := sha256.Sum256(raw)
	result.SHA256 = hex.EncodeToString(sum[:])
	return result, nil
}

// ObserveWanMaskedEdit accepts only the certified terminal task envelope. The
// provider task ID is an immutable submission binding, never client input.
func ObserveWanMaskedEdit(p ImagePolicy, providerTaskID string, raw []byte, fetch func(string) ([]byte, string, error)) (ImageResult, error) {
	result := ImageResult{}
	if !p.valid(p.PublicModel, "images.edits.create") || p.ChannelType != constant.ChannelTypeAli || providerTaskID == "" || len(raw) == 0 || int64(len(raw)) > p.MaxBodyBytes || !utf8.Valid(raw) || fetch == nil {
		return result, ErrInvalid
	}
	var envelope map[string]any
	if common.UnmarshalUnique(raw, &envelope) != nil || !protocolKeys(envelope, "output usage request_id") {
		return result, ErrInvalid
	}
	output, ok := envelope["output"].(map[string]any)
	if !ok || !protocolKeys(output, "task_id task_status results task_metrics submit_time scheduled_time end_time") || output["task_id"] != providerTaskID || output["task_status"] != "SUCCEEDED" {
		return result, ErrInvalid
	}
	items, ok := output["results"].([]any)
	if !ok || len(items) < 1 || len(items) > p.MaxImages {
		return result, ErrInvalid
	}
	usage, ok := envelope["usage"].(map[string]any)
	if !ok || !protocolKeys(usage, "image_count") {
		return result, ErrInvalid
	}
	count, err := confirmedCount(usage["image_count"])
	if err != nil || count != len(items) {
		return result, ErrInvalid
	}
	if metrics, exists := output["task_metrics"]; exists {
		m, ok := metrics.(map[string]any)
		if !ok || !protocolKeys(m, "TOTAL SUCCEEDED FAILED") {
			return result, ErrInvalid
		}
		total, e1 := confirmedCount(m["TOTAL"])
		succeeded, e2 := confirmedCount(m["SUCCEEDED"])
		failed, e3 := confirmedCount(m["FAILED"])
		if e1 != nil || e2 != nil || e3 != nil || total != count || succeeded != count || failed != 0 {
			return result, ErrInvalid
		}
	}
	data := make([]any, 0, len(items))
	seen := map[string]bool{}
	for _, item := range items {
		part, ok := item.(map[string]any)
		if !ok || !protocolKeys(part, "url") {
			return result, ErrInvalid
		}
		location, ok := part["url"].(string)
		if !ok {
			return result, ErrInvalid
		}
		u, e := url.Parse(location)
		allowed := false
		for _, origin := range p.ResourceOrigins {
			allowed = allowed || (e == nil && u.Scheme+"://"+u.Host == origin)
		}
		if !allowed || u.User != nil || u.Fragment != "" {
			return result, ErrInvalid
		}
		bytes, mimeType, e := fetch(location)
		if e != nil {
			return result, e
		}
		fact, e := imageBytes(p, bytes, mimeType)
		if e != nil || seen[fact.SHA256] {
			return result, ErrInvalid
		}
		seen[fact.SHA256] = true
		result.Outputs = append(result.Outputs, fact)
		data = append(data, map[string]any{"b64_json": base64.StdEncoding.EncodeToString(bytes)})
	}
	result.Body, err = common.Marshal(map[string]any{"data": data})
	if err != nil || int64(len(result.Body)) > p.MaxBodyBytes {
		return ImageResult{}, ErrInvalid
	}
	result.Usage, err = common.Marshal(usage)
	if err != nil {
		return ImageResult{}, ErrInvalid
	}
	evidence, err := common.Marshal(struct {
		Usage   json.RawMessage
		Outputs []ImageOutput
	}{result.Usage, result.Outputs})
	if err != nil {
		return ImageResult{}, ErrInvalid
	}
	sum := sha256.Sum256(evidence)
	result.SHA256 = hex.EncodeToString(sum[:])
	return result, nil
}
func (a *Adapter) ImagePolicy(model, wire string) (ImagePolicy, bool) {
	p, ok := a.config.ImagePolicies[wire][model]
	return p, ok && p.valid(model, wire)
}
func (a *Adapter) ImageTarget(model, wire string, id, kind int, upstream, key, origin string) bool {
	p, ok := a.ImagePolicy(model, wire)
	sum := sha256.Sum256([]byte(key))
	return ok && p.ChannelID == id && p.ChannelType == kind && p.UpstreamModel == upstream && p.ChannelKeySHA256 == hex.EncodeToString(sum[:]) && p.Origin == origin
}
func (s *Session) ImagePolicy() (ImagePolicy, error) {
	var f frozenImage
	if common.UnmarshalUnique([]byte(s.call.MediaSnapshot), &f) != nil || !f.Policy.valid(s.call.Model, s.WireOperation().ID) {
		return ImagePolicy{}, ErrInvalid
	}
	return f.Policy, nil
}
func (a *Adapter) ImageRequestModel(wire WireOperation, ct string, body []byte) (string, error) {
	if wire.ID == "gemini.images.generate" {
		return wire.PathModel, nil
	}
	if wire.ID == "images.edits.create" {
		r, e := parseOperation(a.config, "image_edit", ct, body)
		return r.Model, e
	}
	var r map[string]any
	if common.UnmarshalUnique(body, &r) != nil {
		return "", ErrInvalid
	}
	m, ok := r["model"].(string)
	if !ok {
		return "", ErrInvalid
	}
	return m, nil
}
func (s *Session) ImageBody() (body []byte, ct, path string, err error) {
	p, e := s.ImagePolicy()
	if e != nil {
		return nil, "", "", e
	}
	if p.WireOperation == "images.edits.create" {
		var parts []struct{ Name, Filename, MIME, Data string }
		if common.UnmarshalUnique(s.canonical, &parts) != nil {
			return nil, "", "", ErrInvalid
		}
		if p.ChannelType == constant.ChannelTypeAli {
			input := map[string]any{"function": "description_edit_with_mask"}
			parameters := map[string]any{"n": 1}
			for _, part := range parts {
				switch part.Name {
				case "prompt":
					input["prompt"] = part.Data
				case "image":
					input["base_image_url"] = "data:" + part.MIME + ";base64," + part.Data
				case "mask":
					raw, err := base64.StdEncoding.Strict().DecodeString(part.Data)
					if err != nil {
						return nil, "", "", ErrInvalid
					}
					mask, err := wanMask(p, raw)
					if err != nil {
						return nil, "", "", err
					}
					input["mask_image_url"] = "data:image/png;base64," + base64.StdEncoding.EncodeToString(mask)
				case "n":
					n, err := strconv.Atoi(part.Data)
					if err != nil {
						return nil, "", "", ErrInvalid
					}
					parameters["n"] = n
				}
			}
			body, e = common.Marshal(map[string]any{"model": p.UpstreamModel, "input": input, "parameters": parameters})
			if e != nil || int64(len(body)) > p.MaxBodyBytes {
				return nil, "", "", ErrInvalid
			}
			return body, "application/json", "/api/v1/services/aigc/image2image/image-synthesis", nil
		}
		var b bytes.Buffer
		w := multipart.NewWriter(&b)
		for _, part := range parts {
			if part.Filename == "" {
				value := part.Data
				if part.Name == "model" {
					value = p.UpstreamModel
				}
				if e = w.WriteField(part.Name, value); e != nil {
					return nil, "", "", e
				}
			} else {
				header := make(map[string][]string)
				header["Content-Disposition"] = []string{mime.FormatMediaType("form-data", map[string]string{"name": part.Name, "filename": part.Filename})}
				header["Content-Type"] = []string{part.MIME}
				writer, e := w.CreatePart(header)
				if e != nil {
					return nil, "", "", e
				}
				data, e := base64.StdEncoding.DecodeString(part.Data)
				if e != nil {
					return nil, "", "", e
				}
				if _, e = writer.Write(data); e != nil {
					return nil, "", "", e
				}
			}
		}
		if e = w.Close(); e != nil {
			return nil, "", "", e
		}
		if int64(b.Len()) > p.MaxBodyBytes {
			return nil, "", "", ErrInvalid
		}
		return b.Bytes(), w.FormDataContentType(), "/v1/images/edits", nil
	}
	var req map[string]any
	if common.UnmarshalUnique(s.canonical, &req) != nil {
		return nil, "", "", ErrInvalid
	}
	path = "/v1/images/generations"
	if p.WireOperation == "gemini.images.generate" {
		path = "/" + p.APIVersion + "/models/" + p.UpstreamModel + ":generateContent"
	} else {
		req["model"] = p.UpstreamModel
		if p.ChannelType == constant.ChannelTypeAli && p.WireOperation == "images.generations.create" {
			parameters := map[string]any{}
			for _, key := range []string{"n", "seed", "watermark", "prompt_extend"} {
				if value, ok := req[key]; ok {
					parameters[key] = value
				}
			}
			if _, ok := parameters["n"]; !ok {
				parameters["n"] = 1
			}
			if size, ok := req["size"].(string); ok {
				parameters["size"] = strings.Replace(size, "x", "*", 1)
			}
			req = map[string]any{"model": p.UpstreamModel, "input": map[string]any{"messages": []any{map[string]any{"role": "user", "content": []any{map[string]any{"text": req["prompt"]}}}}}, "parameters": parameters}
		}
		if p.ChannelType == constant.ChannelTypeAli {
			path = "/api/v1/services/aigc/multimodal-generation/generation"
		}
		if p.ChannelType == constant.ChannelTypeVolcEngine {
			path = "/api/v3/images/generations"
		}
	}
	body, e = common.Marshal(req)
	if e != nil || int64(len(body)) > p.MaxBodyBytes {
		return nil, "", "", ErrInvalid
	}
	return body, "application/json", path, nil
}
func (s *Session) ObserveImages(ctx context.Context, r ImageResult) error {
	p, e := s.ImagePolicy()
	if e != nil {
		return e
	}
	if len(r.Outputs) == 0 || int64(len(r.Outputs)) > s.call.ReservationQuantity {
		return ErrInvalid
	}
	expectedMIME := ""
	var original map[string]any
	if p.WireOperation == "images.edits.create" {
		if p.ChannelType != constant.ChannelTypeAli {
			var parts []struct{ Name, Filename, MIME, Data string }
			if common.UnmarshalUnique(s.canonical, &parts) != nil {
				return ErrInvalid
			}
			for _, part := range parts {
				if part.Name == "output_format" {
					expectedMIME = "image/" + part.Data
				}
			}
		}
	} else {
		if common.UnmarshalUnique(s.canonical, &original) != nil {
			return ErrInvalid
		}
		if p.WireOperation == "gemini.images.generate" {
			generation, _ := original["generationConfig"].(map[string]any)
			format, _ := generation["responseFormat"].(map[string]any)
			im, _ := format["image"].(map[string]any)
			if im["mimeType"] == "IMAGE_JPEG" {
				expectedMIME = "image/jpeg"
			}
		} else if format, ok := original["output_format"].(string); ok {
			expectedMIME = "image/" + format
		}
	}
	for _, out := range r.Outputs {
		if expectedMIME != "" && out.MIME != expectedMIME {
			return ErrInvalid
		}
	}
	u := metering.Usage{Version: "metered_v2", Operation: s.call.Operation, Source: "native_image", State: "incomplete"}
	var rate Rate
	if common.Unmarshal([]byte(s.call.RateSnapshot), &rate) != nil {
		return ErrInvalid
	}
	price := rate.Metered
	if price == nil {
		return ErrInvalid
	}
	if len(r.Outputs) > 0 && len(r.Outputs) <= p.MaxImages && int64(len(r.Outputs)) <= s.call.ReservationQuantity {
		counts := map[string]int{}
		for _, term := range price.Terms {
			counts[term.Key] = 0
		}
		for _, out := range r.Outputs {
			key := "images"
			if _, ok := counts[key]; !ok {
				key = strconv.Itoa(out.Width) + "x" + strconv.Itoa(out.Height)
			}
			if _, ok := counts[key]; !ok {
				return ErrInvalid
			}
			counts[key]++
		}
		u.State = "confirmed"
		for _, term := range price.Terms {
			u.Measurements = append(u.Measurements, metering.Measurement{Key: term.Key, Unit: "image", Quantity: metering.Quantity{Value: strconv.Itoa(counts[term.Key])}})
		}
	}
	observation, e := common.Marshal(struct {
		Usage        json.RawMessage
		Outputs      []ImageOutput
		SHA256       string
		NativeResult json.RawMessage
	}{r.Usage, r.Outputs, r.SHA256, r.Body})
	if e != nil || int64(len(observation)) > p.MaxBodyBytes+(1<<20) {
		return ErrInvalid
	}
	saved := s.adapter.db.WithContext(ctx).Model(&Call{}).Where("call_id = ? AND status = ? AND image_observation = ?", s.call.CallID, "UNKNOWN", "").Update("image_observation", string(observation))
	if saved.Error != nil {
		return saved.Error
	}
	if saved.RowsAffected != 1 {
		var existing Call
		if err := s.adapter.db.WithContext(ctx).First(&existing, "call_id = ?", s.call.CallID).Error; err != nil {
			return ErrConflict
		}
		if existing.ImageObservation != string(observation) {
			if p.ChannelType == constant.ChannelTypeAli && p.WireOperation == "images.edits.create" && existing.ImageObservation != "" {
				if err := s.adapter.db.WithContext(ctx).Model(&Call{}).Where("call_id = ? AND image_observation = ?", existing.CallID, existing.ImageObservation).Update("evidence_conflict", true).Error; err != nil {
					return err
				}
			}
			return ErrConflict
		}
	}
	return s.ObserveMetering(ctx, u)
}

// WanImageTask verifies ownership before exposing the provider binding or any
// saved result. The current channel must still match the frozen certificate.
func (a *Adapter) WanImageTask(ctx context.Context, credential, taskID, callID string) (Call, ImagePolicy, error) {
	row, err := a.Task(ctx, credential, taskID, callID)
	if err != nil || row.Operation != "image_edit" || row.WireOperation != "images.edits.create" || row.ProviderTaskID == "" {
		return Call{}, ImagePolicy{}, ErrInvalid
	}
	s := &Session{adapter: a, call: row}
	p, err := s.ImagePolicy()
	if err != nil || p.ChannelType != constant.ChannelTypeAli || p.UpstreamModel != "wanx2.1-imageedit" {
		return Call{}, ImagePolicy{}, ErrInvalid
	}
	var attempt Attempt
	if err := a.db.WithContext(ctx).Where("call_id = ?", callID).Order("id DESC").First(&attempt).Error; err != nil || attempt.ChannelID != p.ChannelID {
		return Call{}, ImagePolicy{}, ErrInvalid
	}
	return row, p, nil
}

// WanImageTaskForRecovery is service-only: the worker may poll only an
// existing, frozen provider binding and must never resubmit a paid request.
func (a *Adapter) WanImageTaskForRecovery(ctx context.Context, callID string) (Call, ImagePolicy, error) {
	if !a.Enabled() || !stableID.MatchString(callID) {
		return Call{}, ImagePolicy{}, ErrInvalid
	}
	var row Call
	if err := a.db.WithContext(ctx).First(&row, "call_id = ?", callID).Error; err != nil {
		return Call{}, ImagePolicy{}, ErrInvalid
	}
	instance := row.InstanceID
	if instance == "" {
		instance = "default"
	}
	if instance != a.config.InstanceID || row.EvidenceConflict || row.Operation != "image_edit" || row.WireOperation != "images.edits.create" || row.ProviderTaskID == "" || (row.Status != "UNKNOWN" && !(row.Status == "SETTLE_PENDING" && row.ImageObservation != "")) {
		return Call{}, ImagePolicy{}, ErrInvalid
	}
	p, err := (&Session{adapter: a, call: row}).ImagePolicy()
	if err != nil || p.ChannelType != constant.ChannelTypeAli || p.UpstreamModel != "wanx2.1-imageedit" {
		return Call{}, ImagePolicy{}, ErrInvalid
	}
	var attempt Attempt
	if err = a.db.WithContext(ctx).Where("call_id = ?", callID).Order("id DESC").First(&attempt).Error; err != nil || attempt.ChannelID != p.ChannelID || attempt.Status != "DISPATCHED" {
		return Call{}, ImagePolicy{}, ErrInvalid
	}
	return row, p, nil
}

func (a *Adapter) CompleteWanImage(ctx context.Context, row Call, result ImageResult) error {
	var current Call
	if err := a.db.WithContext(ctx).First(&current, "call_id = ? AND provider_task_id = ? AND digest = ?", row.CallID, row.ProviderTaskID, row.Digest).Error; err != nil {
		return ErrInvalid
	}
	s := &Session{adapter: a, call: current}
	if err := s.ObserveImages(ctx, result); err != nil {
		return err
	}
	if err := s.Finish(ctx); err != nil {
		return err
	}
	if err := a.db.WithContext(ctx).First(&current, "call_id = ?", row.CallID).Error; err != nil {
		return err
	}
	if current.EvidenceConflict {
		return ErrConflict
	}
	if current.Status != "SETTLED" {
		return ErrPending
	}
	return nil
}

// ReleaseWanImage fences a no-output terminal against a concurrently saved
// image observation before writing the durable refund intent.
func (a *Adapter) ReleaseWanImage(ctx context.Context, row Call) error {
	var current Call
	if err := a.db.WithContext(ctx).First(&current, "call_id = ? AND provider_task_id = ? AND digest = ?", row.CallID, row.ProviderTaskID, row.Digest).Error; err != nil {
		return ErrInvalid
	}
	if current.Status == "RELEASED" {
		return nil
	}
	if current.Status == "RELEASE_PENDING" {
		return a.deliver(ctx, current)
	}
	intent, err := common.Marshal(map[string]string{"reason": "provider_confirmed_no_output"})
	if err != nil {
		return err
	}
	saved := a.db.WithContext(ctx).Model(&Call{}).Where("call_id = ? AND provider_task_id = ? AND digest = ? AND status = ? AND evidence = ? AND evidence_conflict = ? AND image_observation = ?", row.CallID, row.ProviderTaskID, row.Digest, "UNKNOWN", "", false, "").Updates(map[string]any{"status": "RELEASE_PENDING", "intent": string(intent)})
	if saved.Error != nil {
		return saved.Error
	}
	if saved.RowsAffected != 1 {
		return ErrConflict
	}
	current.Status = "RELEASE_PENDING"
	current.Intent = string(intent)
	return a.deliver(ctx, current)
}

func (a *Adapter) RecoverWanImage(ctx context.Context, row Call) ([]byte, error) {
	if row.ImageObservation == "" {
		return nil, ErrPending
	}
	var observation struct {
		Usage        json.RawMessage
		Outputs      []ImageOutput
		SHA256       string
		NativeResult json.RawMessage
	}
	if common.UnmarshalUnique([]byte(row.ImageObservation), &observation) != nil || len(observation.NativeResult) == 0 || len(observation.Outputs) == 0 {
		return nil, ErrInvalid
	}
	result := ImageResult{Usage: observation.Usage, Outputs: observation.Outputs, SHA256: observation.SHA256, Body: observation.NativeResult}
	if err := a.CompleteWanImage(ctx, row, result); err != nil {
		return nil, err
	}
	return result.Body, nil
}
func (a *Adapter) ImageReplay(ctx context.Context, credential, task, id string, wire WireOperation, ct string, body []byte) (bool, error) {
	if !a.Enabled() || !IsImageWire(wire.ID) || !stableID.MatchString(id) || !stableID.MatchString(task) || !IsModelCredential(credential) {
		return false, ErrInvalid
	}
	var old Call
	if e := a.db.WithContext(ctx).First(&old, "call_id = ?", id).Error; errors.Is(e, gorm.ErrRecordNotFound) {
		return false, nil
	} else if e != nil {
		return true, e
	}
	if _, e := a.Receipt(ctx, credential, task, id); e != nil {
		return true, e
	}
	if old.WireOperation != wire.ID || old.WirePathModel != wire.PathModel || old.WireMode != wire.Mode {
		return true, ErrConflict
	}
	var frozen frozenImage
	if common.UnmarshalUnique([]byte(old.MediaSnapshot), &frozen) != nil {
		return true, ErrConflict
	}
	req, e := parseImageOperation(frozen.Policy, wire, ct, body)
	if e != nil {
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
	}{"platform-wire-v3", wire, string(data)})
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != old.Digest {
		return true, ErrConflict
	}
	return true, ErrDuplicate
}

// imageUsage closes each producer's observation schema. These counters retain
// their JSON number lexemes but never determine the successful-image charge.
func imageUsage(p ImagePolicy, usage map[string]any) bool {
	counters := "input_tokens output_tokens total_tokens image_count generated_images"
	details := ""
	switch {
	case p.WireOperation == "gemini.images.generate":
		counters = "promptTokenCount cachedContentTokenCount candidatesTokenCount toolUsePromptTokenCount thoughtsTokenCount totalTokenCount"
		details = "promptTokensDetails cacheTokensDetails candidatesTokensDetails toolUsePromptTokensDetails"
		if !protocolKeys(usage, counters+" "+details+" serviceTier") || !protocolEnum(usage, "serviceTier", "unspecified standard flex priority") {
			return false
		}
	case p.ChannelType == constant.ChannelTypeAli:
		counters = "image_count width height"
		if !protocolKeys(usage, counters) {
			return false
		}
	case p.ChannelType == constant.ChannelTypeVolcEngine:
		counters = "generated_images output_tokens total_tokens"
		if !protocolKeys(usage, counters) {
			return false
		}
	default:
		details = "input_tokens_details output_tokens_details"
		if !protocolKeys(usage, counters+" "+details) {
			return false
		}
	}
	for _, key := range strings.Fields(counters) {
		if v, exists := usage[key]; exists {
			count, err := confirmedCount(v)
			if err != nil || uint64(count) > 9007199254740991 {
				return false
			}
		}
	}
	for _, key := range strings.Fields(details) {
		value, exists := usage[key]
		if !exists {
			continue
		}
		if p.WireOperation != "gemini.images.generate" {
			object, ok := value.(map[string]any)
			if !ok || !protocolKeys(object, "text_tokens image_tokens") {
				return false
			}
			for _, n := range object {
				if count, err := confirmedCount(n); err != nil || uint64(count) > 9007199254740991 {
					return false
				}
			}
			continue
		}
		entries, ok := value.([]any)
		if !ok || len(entries) > 6 {
			return false
		}
		seen := map[string]bool{}
		for _, entry := range entries {
			object, ok := entry.(map[string]any)
			if !ok || !protocolKeys(object, "modality tokenCount") || !protocolString(object["modality"]) || !protocolEnum(object, "modality", "MODALITY_UNSPECIFIED TEXT IMAGE VIDEO AUDIO DOCUMENT") {
				return false
			}
			count, err := confirmedCount(object["tokenCount"])
			modality := object["modality"].(string)
			if err != nil || uint64(count) > 9007199254740991 || seen[modality] {
				return false
			}
			seen[modality] = true
		}
	}
	return true
}

var imageMetadataSize = regexp.MustCompile(`^[1-9][0-9]{0,7}x[1-9][0-9]{0,7}$`)
var imageMetadataCapability = regexp.MustCompile(`(?i)([a-z][a-z0-9+.-]*://|(^|[\s"'(=])//|www\.|data:|bearer\s|sk-[a-z0-9]|api[_-]?key\s*[:=]|authorization\s*[:=])`)

// Metadata is part of the public response, so allowed names alone are not a
// privacy boundary. Retain only native scalars, bounded identifiers and enums.
func imageOutputMetadata(object map[string]any, maxPixels int) bool {
	for _, key := range []string{"request_id", "responseId", "modelVersion"} {
		if value, exists := object[key]; exists {
			text, ok := value.(string)
			if !ok || text == "" || len(text) > 256 {
				return false
			}
			for _, r := range text {
				if r < 32 || r == 127 {
					return false
				}
			}
			if imageMetadataCapability.MatchString(text) {
				delete(object, key)
			}
		}
	}
	for _, key := range []string{"created", "index"} {
		if value, exists := object[key]; exists {
			count, err := confirmedCount(value)
			if err != nil || uint64(count) > 9007199254740991 || (key == "index" && count != 0) {
				return false
			}
		}
	}
	if !protocolEnum(object, "background", "transparent opaque") || !protocolEnum(object, "output_format", "png jpeg webp") || !protocolEnum(object, "quality", "low medium high standard hd") {
		return false
	}
	if value, exists := object["size"]; exists {
		size, ok := value.(string)
		if !ok || !imageMetadataSize.MatchString(size) {
			return false
		}
		w, h, _ := strings.Cut(size, "x")
		width, e1 := strconv.ParseInt(w, 10, 32)
		height, e2 := strconv.ParseInt(h, 10, 32)
		if e1 != nil || e2 != nil || width*height > int64(maxPixels) {
			return false
		}
	}
	if value, exists := object["revised_prompt"]; exists {
		text, ok := value.(string)
		if !ok || len(text) > 32768 {
			return false
		}
		if imageMetadataCapability.MatchString(text) {
			delete(object, "revised_prompt")
		}
		for _, r := range text {
			if r < 32 && r != '\n' && r != '\t' {
				return false
			}
		}
	}
	return true
}

// completeStillWebP validates every RIFF chunk, including chunks after the
// decoder's first image. Exactly one still bitstream is permitted.
func completeStillWebP(raw []byte, width, height int) bool {
	if len(raw) < 12 || string(raw[:4]) != "RIFF" || string(raw[8:12]) != "WEBP" || uint64(binary.LittleEndian.Uint32(raw[4:8]))+8 != uint64(len(raw)) {
		return false
	}
	seen := map[string]bool{}
	extended, imageSeen := false, false
	var flags byte
	bitstream := ""
	for offset := 12; offset < len(raw); {
		if len(raw)-offset < 8 {
			return false
		}
		kind := string(raw[offset : offset+4])
		length := uint64(binary.LittleEndian.Uint32(raw[offset+4 : offset+8]))
		end := uint64(offset) + 8 + length
		padded := end + (length & 1)
		if padded > uint64(len(raw)) || (length&1 != 0 && raw[end] != 0) {
			return false
		}
		data := raw[offset+8 : int(end)]
		switch kind {
		case "VP8X":
			if offset != 12 || length != 10 || data[0]&0xc3 != 0 || data[1] != 0 || data[2] != 0 || data[3] != 0 {
				return false
			}
			extended, flags = true, data[0]
			w := 1 + int(data[4]) + (int(data[5]) << 8) + (int(data[6]) << 16)
			h := 1 + int(data[7]) + (int(data[8]) << 8) + (int(data[9]) << 16)
			if w != width || h != height {
				return false
			}
		case "ANIM", "ANMF":
			return false
		case "VP8 ", "VP8L":
			if imageSeen || length == 0 || (!extended && offset != 12) || (kind == "VP8L" && seen["ALPH"]) {
				return false
			}
			imageSeen, bitstream = true, kind
			if extended && kind == "VP8L" {
				if length < 5 || data[0] != 0x2f || (data[4]&0x10 != 0) != (flags&0x10 != 0) {
					return false
				}
			}
		case "ALPH":
			if !extended || flags&0x10 == 0 || imageSeen || seen[kind] || length == 0 {
				return false
			}
		case "ICCP":
			if !extended || flags&0x20 == 0 || imageSeen || seen["ALPH"] || seen[kind] || length == 0 {
				return false
			}
		case "EXIF", "XMP ":
			flag := byte(0x08)
			if kind == "XMP " {
				flag = 0x04
			}
			if !extended || flags&flag == 0 || seen[kind] || length == 0 {
				return false
			}
		}
		seen[kind] = true
		offset = int(padded)
	}
	if !imageSeen {
		return false
	}
	return !extended || ((flags&0x20 != 0) == seen["ICCP"] && (flags&0x08 != 0) == seen["EXIF"] && (flags&0x04 != 0) == seen["XMP "] && (bitstream != "VP8 " || (flags&0x10 != 0) == seen["ALPH"]))
}

// completeJPEG checks marker framing through the sole terminal EOI. image.Decode
// validates pixels but intentionally tolerates trailing data and concatenations.
func completeJPEG(raw []byte) bool {
	if len(raw) < 4 || raw[0] != 0xff || raw[1] != 0xd8 {
		return false
	}
	offset := 2
	for offset < len(raw) {
		if raw[offset] != 0xff {
			return false
		}
		for offset < len(raw) && raw[offset] == 0xff {
			offset++
		}
		if offset >= len(raw) {
			return false
		}
		marker := raw[offset]
		offset++
		if marker == 0xd9 {
			return offset == len(raw)
		}
		if marker == 0 || marker == 0xd8 || (marker >= 0xd0 && marker <= 0xd7) {
			return false
		}
		if marker == 1 {
			continue
		}
		if offset+2 > len(raw) {
			return false
		}
		length := int(binary.BigEndian.Uint16(raw[offset : offset+2]))
		if length < 2 || offset+length > len(raw) {
			return false
		}
		offset += length
		if marker != 0xda {
			continue
		}
		for offset < len(raw) {
			if raw[offset] != 0xff {
				offset++
				continue
			}
			start := offset
			for offset < len(raw) && raw[offset] == 0xff {
				offset++
			}
			if offset >= len(raw) {
				return false
			}
			code := raw[offset]
			if code == 0 || (code >= 0xd0 && code <= 0xd7) {
				offset++
				continue
			}
			offset = start
			break
		}
	}
	return false
}
