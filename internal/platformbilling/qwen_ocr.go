package platformbilling

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/mediafacts"
	"github.com/QuantumNous/new-api/pkg/metering"
	"math"
	"strconv"
	"strings"
	"unicode/utf8"
)

type frozenQwenOCR struct {
	Policy OCRPolicy `json:"ocr_policy"`
	Width  int       `json:"width"`
	Height int       `json:"height"`
}

func parseQwenOCR(c Config, ct string, body []byte) (operationRequest, error) {
	var root map[string]any
	if ct != "application/json" || !utf8.Valid(body) || common.UnmarshalUnique(body, &root) != nil || !protocolKeys(root, "model input parameters") {
		return operationRequest{}, ErrInvalid
	}
	model, _ := root["model"].(string)
	p := c.OCRPolicies["dashscope.ocr.recognitions.create"][model]
	limit := c.MaxBodyBytes
	if v := c.OperationLimits["ocr"][model].MaxBodyBytes; v > 0 {
		limit = v
	}
	if !p.valid(model, "dashscope.ocr.recognitions.create") || int64(len(body)) > limit {
		return operationRequest{}, ErrInvalid
	}
	input, ok := root["input"].(map[string]any)
	if !ok || !protocolKeys(input, "messages") {
		return operationRequest{}, ErrInvalid
	}
	messages, ok := input["messages"].([]any)
	if !ok || len(messages) != 1 {
		return operationRequest{}, ErrInvalid
	}
	message, ok := messages[0].(map[string]any)
	if !ok || !protocolKeys(message, "role content") || message["role"] != "user" {
		return operationRequest{}, ErrInvalid
	}
	content, ok := message["content"].([]any)
	if !ok || len(content) != 1 {
		return operationRequest{}, ErrInvalid
	}
	img, ok := content[0].(map[string]any)
	if !ok || !protocolKeys(img, "image enable_rotate min_pixels max_pixels") {
		return operationRequest{}, ErrInvalid
	}
	if v, present := img["enable_rotate"]; present {
		if rotation, ok := v.(bool); !ok || rotation {
			return operationRequest{}, ErrInvalid
		}
	}
	minPixels := int64(1)
	maxPixels := p.MaxImagePixels
	for _, key := range []string{"min_pixels", "max_pixels"} {
		if value, present := img[key]; present {
			n, e := confirmedCount(value)
			if e != nil || n < 1 || int64(n) > p.MaxImagePixels {
				return operationRequest{}, ErrInvalid
			}
			if key == "min_pixels" {
				minPixels = int64(n)
			} else {
				maxPixels = int64(n)
			}
		}
	}
	if minPixels > maxPixels {
		return operationRequest{}, ErrInvalid
	}
	imageURL, ok := img["image"].(string)
	if !ok || (!strings.HasPrefix(imageURL, "data:image/png;base64,") && !strings.HasPrefix(imageURL, "data:image/jpeg;base64,")) {
		return operationRequest{}, ErrInvalid
	}
	encoded := strings.SplitN(imageURL, ",", 2)[1]
	decoded, e := base64.StdEncoding.Strict().DecodeString(encoded)
	if e != nil || len(decoded) > p.MaxImageBytes {
		return operationRequest{}, ErrInvalid
	}
	fact, e := mediafacts.DecodeMediaFact("image", imageURL)
	if e != nil || fact.Pixels > p.MaxImagePixels || (fact.Format != "png" && fact.Format != "jpeg") || !strings.HasPrefix(imageURL, "data:image/"+fact.Format+";base64,") {
		return operationRequest{}, ErrInvalid
	}
	params, ok := root["parameters"].(map[string]any)
	if !ok || !protocolKeys(params, "ocr_options") {
		return operationRequest{}, ErrInvalid
	}
	options, ok := params["ocr_options"].(map[string]any)
	if !ok || !protocolKeys(options, "task") || options["task"] != "advanced_recognition" {
		return operationRequest{}, ErrInvalid
	}
	canonical, e := common.Marshal(root)
	if e != nil {
		return operationRequest{}, ErrInvalid
	}
	root["model"] = p.UpstreamModel
	effective, e := common.Marshal(root)
	if e != nil || int64(len(effective)) > limit {
		return operationRequest{}, ErrInvalid
	}
	quantity, output := 1, 0
	if p.UsagePolicy == "provider_tokens_v1" {
		quantity, output = p.MaxInputTokens, p.MaxOutputTokens
	}
	snapshot, _ := common.Marshal(frozenQwenOCR{p, fact.Width, fact.Height})
	return operationRequest{Model: model, Canonical: canonical, Quantity: quantity, Output: output, MediaSnapshot: string(snapshot)}, nil
}
func (s *Session) QwenOCRBody() ([]byte, error) {
	var body map[string]any
	if common.UnmarshalUnique(s.canonical, &body) != nil {
		return nil, ErrInvalid
	}
	p, e := s.OCRPolicy()
	if e != nil {
		return nil, e
	}
	body["model"] = p.UpstreamModel
	return common.Marshal(body)
}

// QwenOCRUsage validates complete native geometry independently from token usage.
// The frozen policy decides whether a valid result settles requests or tokens.
func (s *Session) QwenOCRUsage(raw []byte) (metering.Usage, error) {
	p, policyErr := s.OCRPolicy()
	u := metering.Usage{Version: "metered_v2", Operation: "ocr", State: "incomplete", Source: "success_request"}
	if policyErr != nil {
		return u, ErrInvalid
	}
	u.Source = p.UsagePolicy
	var f frozenQwenOCR
	var root map[string]any
	if len(raw) == 0 || len(raw) > 4<<20 || !utf8.Valid(raw) || common.UnmarshalUnique([]byte(s.call.MediaSnapshot), &f) != nil || common.UnmarshalUnique(raw, &root) != nil {
		return u, ErrInvalid
	}
	requestID, ok := root["request_id"].(string)
	if !ok || requestID == "" || len(requestID) > 256 {
		return u, ErrInvalid
	}
	u.ProviderRequestID = requestID
	output, ok := root["output"].(map[string]any)
	if !ok {
		return u, ErrInvalid
	}
	choices, ok := output["choices"].([]any)
	if !ok || len(choices) != 1 {
		return u, ErrInvalid
	}
	choice, ok := choices[0].(map[string]any)
	if !ok || choice["finish_reason"] != "stop" {
		return u, ErrInvalid
	}
	message, ok := choice["message"].(map[string]any)
	if !ok || message["role"] != "assistant" {
		return u, ErrInvalid
	}
	content, ok := message["content"].([]any)
	if !ok || len(content) != 1 {
		return u, ErrInvalid
	}
	item, ok := content[0].(map[string]any)
	if !ok {
		return u, ErrInvalid
	}
	result, ok := item["ocr_result"].(map[string]any)
	if !ok {
		return u, ErrInvalid
	}
	words, ok := result["words_info"].([]any)
	if !ok || len(words) > 4096 {
		return u, ErrInvalid
	}
	if len(words) == 0 {
		text, _ := item["text"].(string)
		if f.Policy.BlankPolicy != "allow_empty_complete_v1" || strings.TrimSpace(text) != "" {
			return u, ErrInvalid
		}
	}
	seen := map[string]bool{}
	for _, value := range words {
		word, ok := value.(map[string]any)
		if !ok {
			return u, ErrInvalid
		}
		text, ok := word["text"].(string)
		if !ok || strings.TrimSpace(text) == "" || len(text) > 16384 {
			return u, ErrInvalid
		}
		rect, ok := word["rotate_rect"].([]any)
		if !ok || len(rect) != 5 {
			return u, ErrInvalid
		}
		v := make([]float64, 5)
		for i, n := range rect {
			num, ok := n.(json.Number)
			if !ok {
				return u, ErrInvalid
			}
			x, e := num.Float64()
			if e != nil || math.IsNaN(x) || math.IsInf(x, 0) {
				return u, ErrInvalid
			}
			v[i] = x
		}
		if v[0] < 0 || v[0] > float64(f.Width) || v[1] < 0 || v[1] > float64(f.Height) || v[2] <= 0 || v[3] <= 0 || math.Abs(v[4]) > 90 {
			return u, ErrInvalid
		}
		angle := v[4] * math.Pi / 180
		dx := (math.Abs(v[2]*math.Cos(angle)) + math.Abs(v[3]*math.Sin(angle))) / 2
		dy := (math.Abs(v[2]*math.Sin(angle)) + math.Abs(v[3]*math.Cos(angle))) / 2
		if v[0]-dx < -1 || v[0]+dx > float64(f.Width)+1 || v[1]-dy < -1 || v[1]+dy > float64(f.Height)+1 {
			return u, ErrInvalid
		}
		b, _ := common.Marshal(struct {
			Text      string
			Rectangle []float64
		}{text, v})
		if seen[string(b)] {
			return u, ErrInvalid
		}
		seen[string(b)] = true
	}
	usage, ok := root["usage"].(map[string]any)
	if !ok {
		return u, ErrInvalid
	}
	in, e := confirmedCount(usage["input_tokens"])
	if e != nil {
		return u, ErrInvalid
	}
	out, e := confirmedCount(usage["output_tokens"])
	if e != nil {
		return u, ErrInvalid
	}
	if p.UsagePolicy == "provider_tokens_v1" && (in > p.MaxInputTokens || out > p.MaxOutputTokens) {
		return u, ErrInvalid
	}
	total, e := confirmedCount(usage["total_tokens"])
	if e != nil || int64(in)+int64(out) != int64(total) {
		return u, ErrInvalid
	}
	u.State = "confirmed"
	if p.UsagePolicy == "provider_tokens_v1" {
		u.Measurements = []metering.Measurement{
			{Key: "input", Unit: "token", Quantity: metering.Quantity{Value: strconv.Itoa(in)}},
			{Key: "output", Unit: "token", Quantity: metering.Quantity{Value: strconv.Itoa(out)}},
		}
	} else {
		u.Measurements = []metering.Measurement{{Key: "requests", Unit: "request", Quantity: metering.Quantity{Value: "1"}}}
	}
	return u, nil
}
func (s *Session) RecordOCRObservation(ctx context.Context, raw []byte, valid bool) error {
	sum := sha256.Sum256(raw)
	var response map[string]json.RawMessage
	_ = common.UnmarshalUnique(raw, &response)
	observation, _ := common.Marshal(struct {
		Digest string
		Valid  bool
		Usage  json.RawMessage
	}{hex.EncodeToString(sum[:]), valid, response["usage"]})
	db := s.adapter.db.WithContext(ctx)
	updated := db.Model(&Call{}).Where("call_id = ? AND (ocr_observation = ? OR ocr_observation IS NULL)", s.call.CallID, "").Update("ocr_observation", string(observation))
	if updated.Error != nil {
		return updated.Error
	}
	if updated.RowsAffected == 1 {
		return nil
	}
	var row Call
	if e := db.First(&row, "call_id = ?", s.call.CallID).Error; e != nil {
		return e
	}
	if row.OCRObservation == string(observation) && !row.EvidenceConflict {
		return nil
	}
	if e := db.Model(&Call{}).Where("call_id = ?", row.CallID).Update("evidence_conflict", true).Error; e != nil {
		return e
	}
	return ErrConflict
}
