package helper

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/gin-gonic/gin"
)

const MaxMultimodalEmbeddingBody = 70_000_000

// strictMultimodalFields is a typed-contract boundary, including duplicate keys at
// every depth. Values are decoded only after this allowlist succeeds.
func strictMultimodalFields(raw []byte, names ...string) (map[string]json.RawMessage, error) {
	var fields map[string]json.RawMessage
	if err := common.UnmarshalUnique(raw, &fields); err != nil || fields == nil {
		return nil, fmt.Errorf("invalid multimodal JSON object")
	}
	for k := range fields {
		if string(fields[k]) == "null" {
			return nil, fmt.Errorf("multimodal fields must not be null")
		}
		found := false
		for _, name := range names {
			if k == name {
				found = true
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("unsupported multimodal field %q", k)
		}
	}
	return fields, nil
}
func ParseMultimodalEmbeddingRequest(raw []byte, native bool) (*dto.MultimodalEmbeddingRequest, error) {
	if !utf8.Valid(raw) {
		return nil, fmt.Errorf("multimodal JSON must be valid UTF-8")
	}
	if len(raw) > MaxMultimodalEmbeddingBody {
		return nil, fmt.Errorf("multimodal request exceeds gateway body limit")
	}
	r := &dto.MultimodalEmbeddingRequest{}
	var contents json.RawMessage
	if native {
		fields, err := strictMultimodalFields(raw, "model", "input", "parameters")
		if err != nil {
			return nil, err
		}
		input, err := strictMultimodalFields(fields["input"], "contents")
		if err != nil {
			return nil, err
		}
		contents = input["contents"]
		if p, ok := fields["parameters"]; ok {
			if _, err := strictMultimodalFields(p, "dimension", "fps", "enable_fusion"); err != nil {
				return nil, err
			}
		}
		var n dto.QwenMultimodalEmbeddingRequest
		if err := common.Unmarshal(raw, &n); err != nil {
			return nil, fmt.Errorf("invalid native multimodal request")
		}
		r.Model = n.Model
		r.Input = n.Input.Contents
		r.Dimensions = n.Parameters.Dimension
		r.FPS = n.Parameters.FPS
		r.EnableFusion = n.Parameters.EnableFusion
	} else {
		fields, err := strictMultimodalFields(raw, "model", "input", "dimensions", "fps", "enable_fusion")
		if err != nil {
			return nil, err
		}
		contents = fields["input"]
		if err := common.Unmarshal(raw, r); err != nil {
			return nil, fmt.Errorf("invalid multimodal request")
		}
	}
	var items []json.RawMessage
	if err := common.Unmarshal(contents, &items); err != nil || len(items) < 1 || len(items) > 20 {
		return nil, fmt.Errorf("multimodal input requires 1 to 20 contents")
	}
	texts, images, videos := 0, 0, 0
	for i, item := range items {
		fields, err := strictMultimodalFields(item, "text", "image", "video")
		if err != nil {
			return nil, err
		}
		if len(fields) != 1 {
			return nil, fmt.Errorf("each content requires exactly one text, image or video")
		}
		for _, rawValue := range fields {
			var s string
			if err := common.Unmarshal(rawValue, &s); err != nil || s == "" || !utf8.ValidString(s) {
				return nil, fmt.Errorf("content must be a nonempty UTF-8 string")
			}
		}
		entry := r.Input[i]
		texts += len(entry.Text)
		if entry.Image != "" {
			images++
		}
		if entry.Video != "" {
			videos++
		}
	}
	if strings.TrimSpace(r.Model) == "" || texts > 32_000 || images > 5 || videos > 1 {
		return nil, fmt.Errorf("multimodal request exceeds model, text or media count limits")
	}
	if r.FPS != nil && *r.FPS != 1 {
		return nil, fmt.Errorf("fixed multimodal space requires fps=1")
	}
	if r.EnableFusion != nil && *r.EnableFusion {
		return nil, fmt.Errorf("fusion is unsupported; independent embeddings required")
	}
	if r.Dimensions != nil && !qwenMultimodalDimension(*r.Dimensions) {
		return nil, fmt.Errorf("unsupported multimodal dimension")
	}
	return r, nil
}
func qwenMultimodalDimension(n int) bool {
	switch n {
	case 2560, 2048, 1536, 1024, 768, 512, 256:
		return true
	}
	return false
}
func ValidateMultimodalEmbeddingSpace(r *dto.MultimodalEmbeddingRequest, s *dto.EmbeddingSpace) error {
	if s == nil || s.Provider != "aliyun" || s.Model != "qwen3-vl-embedding" || strings.TrimSpace(s.Version) == "" || s.InputSemantics != dto.QwenMultimodalInputSemantics || !qwenMultimodalDimension(s.Dimensions) {
		return fmt.Errorf("multimodal operation requires explicit fixed Qwen embedding_space")
	}
	if r.Dimensions != nil && *r.Dimensions != s.Dimensions {
		return fmt.Errorf("dimensions differ from fixed embedding_space")
	}
	return nil
}
func GetAndValidateMultimodalEmbeddingRequest(c *gin.Context) (*dto.MultimodalEmbeddingRequest, error) {
	if !strings.HasPrefix(c.GetHeader("Content-Type"), "application/json") {
		return nil, fmt.Errorf("multimodal embeddings require application/json")
	}
	storage, err := common.GetBodyStorage(c)
	if err != nil {
		return nil, err
	}
	if storage.Size() > MaxMultimodalEmbeddingBody {
		return nil, fmt.Errorf("multimodal request exceeds gateway body limit")
	}
	raw, err := storage.Bytes()
	if err != nil {
		return nil, err
	}
	return ParseMultimodalEmbeddingRequest(raw, strings.HasPrefix(c.Request.URL.Path, "/api/v1/services/embeddings/"))
}
