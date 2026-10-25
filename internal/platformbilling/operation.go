package platformbilling

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"

	"strings"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
)

// OperationLimits are server-certified execution bounds, never client estimates.
// Duration/provider-unit operations require a configured cap enforced by their
// producer; absence rejects admission before reserving funds.
type OperationLimits struct {
	MaxBodyBytes    int64  `json:"max_body_bytes"`
	MaxQuantity     int    `json:"max_quantity"`
	Policy          string `json:"policy"`
	ProviderUnitCap string `json:"provider_unit_cap"`
	ProviderUnit    string `json:"provider_unit"`
}
type operationRequest struct {
	Model     string
	Canonical []byte
	Quantity  int
	Output    int
}

func parseOperation(c Config, op, contentType string, body []byte) (operationRequest, error) {
	var result operationRequest
	fields := map[string]string{
		"responses":        "model input instructions max_output_tokens stream temperature top_p tools tool_choice parallel_tool_calls reasoning text metadata store",
		"embedding":        "model input encoding_format dimensions user",
		"speech":           "model input voice response_format speed instructions",
		"transcription":    "model file language prompt response_format temperature stream",
		"image_generation": "model prompt n size quality style response_format background output_format output_compression moderation user",
		"image_edit":       "model prompt image mask n size quality response_format background output_format output_compression user",
		"ocr":              "model file language",
		"video":            "model prompt seconds size input_reference",
	}
	var request map[string]any
	mediaType, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		return result, ErrInvalid
	}
	var canonical []byte
	if mediaType == "application/json" {
		if op == "chat" {
			request, err = strictChatRequest(body)
		} else {
			err = common.UnmarshalUnique(body, &request)
		}
		if err != nil || request == nil {
			return result, ErrInvalid
		}
		canonical, err = common.Marshal(request)
	} else if mediaType == "multipart/form-data" && (op == "transcription" || op == "image_edit" || op == "ocr" || op == "video") {
		request = map[string]any{}
		reader := multipart.NewReader(bytes.NewReader(body), params["boundary"])
		type descriptor struct{ Name, Filename, ContentType, Digest string }
		files := []descriptor{}
		for i := 0; ; i++ {
			part, e := reader.NextPart()
			if e == io.EOF {
				break
			}
			if e != nil || i >= 16 {
				return result, ErrInvalid
			}
			name := part.FormName()
			if name == "" {
				return result, ErrInvalid
			}
			if _, exists := request[name]; exists {
				return result, ErrInvalid
			}
			data, e := io.ReadAll(part)
			if e != nil || len(data) == 0 {
				return result, ErrInvalid
			}
			if part.FileName() != "" {
				if name != "file" && name != "image" && name != "mask" && name != "input_reference" {
					return result, ErrInvalid
				}
				sum := sha256.Sum256(data)
				files = append(files, descriptor{name, part.FileName(), part.Header.Get("Content-Type"), hex.EncodeToString(sum[:])})
				request[name] = "@file"
			} else {
				if len(data) > 65536 {
					return result, ErrInvalid
				}
				request[name] = string(data)
			}
		}
		if len(files) == 0 {
			return result, ErrInvalid
		}
		canonical, err = common.Marshal(struct {
			Fields map[string]any
			Files  []descriptor
		}{request, files})
	} else {
		return result, ErrInvalid
	}
	if err != nil {
		return result, ErrInvalid
	}
	if op != "chat" {
		allowed, ok := fields[op]
		if !ok {
			return result, ErrInvalid
		}
		for k := range request {
			if !strings.Contains(" "+allowed+" ", " "+k+" ") {
				return result, ErrInvalid
			}
		}
	}
	result.Model, _ = request["model"].(string)
	if result.Model == "" || len(result.Model) > 255 {
		return result, ErrInvalid
	}
	result.Canonical = canonical
	result.Quantity = c.MaxInputTokens
	count := func(key string, fallback int) (int, error) {
		v, ok := request[key]
		if !ok {
			return fallback, nil
		}
		if mediaType == "multipart/form-data" {
			v = json.Number(v.(string))
		}
		return confirmedCount(v)
	}
	switch op {
	case "chat", "responses":
		key := "max_output_tokens"
		if op == "chat" {
			key = "max_tokens"
			if _, ok := request["max_completion_tokens"]; ok {
				key = "max_completion_tokens"
			}
		}
		result.Output, err = count(key, 0)
		if err != nil || result.Output < 1 || result.Output > c.MaxOutputTokens || len(body) > c.MaxInputTokens/2 {
			return result, ErrInvalid
		}
		if op == "responses" {
			if _, ok := request["input"].(string); !ok {
				return result, ErrInvalid
			}
			if _, ok := request["tools"]; ok {
				return result, ErrInvalid
			}
		}
		if op == "chat" {
			n, e := count("n", 1)
			if e != nil || n != 1 {
				return result, ErrInvalid
			}
			for _, k := range []string{"max_tokens", "max_completion_tokens"} {
				if _, ok := request[k]; ok {
					v, e := count(k, 0)
					if e != nil || v < 1 || v > result.Output {
						return result, ErrInvalid
					}
				}
			}
		}
	case "embedding":
		total := 0
		switch input := request["input"].(type) {
		case string:
			total = len(input)
		case []any:
			if len(input) > 2048 {
				return result, ErrInvalid
			}
			for _, v := range input {
				s, ok := v.(string)
				if !ok || s == "" {
					return result, ErrInvalid
				}
				total += len(s)
			}
		default:
			return result, ErrInvalid
		}
		if total == 0 || total > c.MaxInputTokens/2 {
			return result, ErrInvalid
		}
	case "speech":
		input, ok := request["input"].(string)
		if !ok || input == "" || !utf8.ValidString(input) {
			return result, ErrInvalid
		}
		result.Quantity = utf8.RuneCountInString(input)
		if result.Quantity > c.MaxInputTokens {
			return result, ErrInvalid
		}
	case "image_generation", "image_edit":
		prompt, ok := request["prompt"].(string)
		if !ok || prompt == "" {
			return result, ErrInvalid
		}
		result.Quantity, err = count("n", 1)
		if err != nil || result.Quantity < 1 || result.Quantity > dto.MaxImageN {
			return result, ErrInvalid
		}
		if op == "image_edit" && request["image"] != "@file" {
			return result, ErrInvalid
		}
	case "ocr":
		result.Quantity = 1
		if request["file"] != "@file" {
			return result, ErrInvalid
		}
	case "transcription", "video":
		limit, ok := c.OperationLimits[op][result.Model]
		if !ok || limit.MaxQuantity < 1 || limit.MaxQuantity > 86400 {
			return result, ErrInvalid
		}
		result.Quantity = limit.MaxQuantity
		if op == "transcription" && request["file"] != "@file" {
			return result, ErrInvalid
		}
		if op == "video" {
			seconds, e := count("seconds", 0)
			if e != nil || seconds < 1 || seconds > limit.MaxQuantity {
				return result, ErrInvalid
			}
		}
	}
	return result, nil
}

func (a *Adapter) OperationBodyLimit(op string) int64 {
	max := a.config.MaxBodyBytes
	for _, v := range a.config.OperationLimits[op] {
		if v.MaxBodyBytes > max && v.MaxBodyBytes <= 64<<20 {
			max = v.MaxBodyBytes
		}
	}
	return max
}

// ValidatedContentType preserves multipart boundaries for the already-validated bytes.
func ValidatedContentType(v string) string {
	t, _, e := mime.ParseMediaType(v)
	if e != nil {
		return ""
	}
	if t == "application/json" || t == "multipart/form-data" {
		return v
	}
	return ""
}
