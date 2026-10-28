package platformbilling

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"net/url"
	"strings"
	"unicode/utf8"
)

// KnowledgePolicy certifies one selected producer and its complete request caps.
// It is private deployment metadata, never a caller-controlled price or limit.
type KnowledgePolicy struct {
	ChannelKeySHA256  string              `json:"channel_key_sha256"`
	Version           string              `json:"version"`
	ProviderReference string              `json:"provider_reference"`
	PublicModel       string              `json:"public_model"`
	WireOperation     string              `json:"wire_operation"`
	ChannelID         int                 `json:"channel_id"`
	ChannelType       int                 `json:"channel_type"`
	UpstreamModel     string              `json:"upstream_model"`
	HardInputTokens   int                 `json:"hard_input_tokens"`
	VisualTokens      int                 `json:"visual_tokens"`
	MaxItemTokens     int                 `json:"max_item_tokens"`
	MaxBatch          int                 `json:"max_batch"`
	Space             *dto.EmbeddingSpace `json:"embedding_space,omitempty"`
	RateIDs           map[string]string   `json:"rate_ids"`
}
type frozenKnowledge struct {
	Facts  json.RawMessage `json:"media_facts,omitempty"`
	Policy KnowledgePolicy `json:"knowledge_policy"`
	Body   json.RawMessage `json:"effective_body"`
}

func (p KnowledgePolicy) valid(c Config, model, wire string) bool {
	reference, e := url.Parse(p.ProviderReference)
	hash, herr := hex.DecodeString(p.ChannelKeySHA256)
	if e != nil || reference.Scheme != "https" || reference.Host == "" || reference.User != nil || reference.Fragment != "" || herr != nil || len(hash) != 32 || len(p.PublicModel) > 255 || strings.TrimSpace(p.PublicModel) == "" {
		return false
	}
	if len(p.ChannelKeySHA256) != 64 || p.Version != "knowledge_v1" || !strings.HasPrefix(p.ProviderReference, "https://") || p.PublicModel != model || p.WireOperation != wire || p.ChannelID < 1 || p.UpstreamModel == "" || p.HardInputTokens < 1 || p.HardInputTokens > c.MaxInputTokens || len(p.RateIDs) == 0 {
		return false
	}
	for asset, id := range p.RateIDs {
		if (asset != "CREDIT/6" && asset != "CNY/6") || id == "" {
			return false
		}
	}
	switch wire {
	case "rerank.create":
		return p.ChannelType == constant.ChannelTypeAli && p.UpstreamModel == "qwen3-rerank" && p.HardInputTokens <= 120000 && p.MaxItemTokens > 0 && p.MaxItemTokens <= 4000 && p.MaxBatch > 0 && p.MaxBatch <= 500 && p.VisualTokens == 0
	case "embeddings.create":
		return p.ChannelType == constant.ChannelTypeOpenAI && p.MaxItemTokens > 0 && p.MaxItemTokens <= 8192 && p.MaxBatch > 0 && p.MaxBatch <= 2048 && p.Space != nil && p.Space.Model == p.UpstreamModel && p.Space.Version != "" && p.Space.Provider != "" && p.Space.Dimensions > 0 && p.Space.Dimensions <= 65536 && p.Space.InputSemantics == "text" && p.VisualTokens == 0
	case "multimodal.embeddings.create":
		return p.ChannelType == constant.ChannelTypeAli && p.UpstreamModel == "qwen3-vl-embedding" && p.HardInputTokens <= 64000 && p.VisualTokens > 0 && p.VisualTokens <= 10000000 && p.MaxBatch > 0 && p.MaxBatch <= 20 && validKnowledgeSpace(p.Space) && p.Space.Provider == "aliyun" && p.Space.Model == p.UpstreamModel && p.Space.Version != "" && p.Space.InputSemantics == dto.QwenMultimodalInputSemantics
	}
	return false
}
func parseKnowledgeRerank(c Config, p KnowledgePolicy, body []byte, native bool) (operationRequest, error) {
	result := operationRequest{}
	if !p.valid(c, p.PublicModel, "rerank.create") || !utf8.Valid(body) || int64(len(body)) > c.MaxBodyBytes {
		return result, ErrInvalid
	}
	var original map[string]any
	if common.UnmarshalUnique(body, &original) != nil {
		return result, ErrInvalid
	}
	request := original
	if native {
		if !protocolKeys(original, "model input parameters") {
			return result, ErrInvalid
		}
		input, ok := original["input"].(map[string]any)
		if !ok || !protocolKeys(input, "query documents") {
			return result, ErrInvalid
		}
		request = map[string]any{"model": original["model"], "query": input["query"], "documents": input["documents"]}
		if raw, exists := original["parameters"]; exists {
			params, ok := raw.(map[string]any)
			if !ok || !protocolKeys(params, "top_n return_documents") {
				return result, ErrInvalid
			}
			for k, v := range params {
				request[k] = v
			}
		}
	}
	if !protocolKeys(request, "model query documents top_n return_documents") || request["model"] != p.PublicModel {
		return result, ErrInvalid
	}
	query, ok := request["query"].(string)
	if !ok || strings.TrimSpace(query) == "" || len(query) > p.MaxItemTokens {
		return result, ErrInvalid
	}
	docs, ok := request["documents"].([]any)
	if !ok || len(docs) < 1 || len(docs) > p.MaxBatch {
		return result, ErrInvalid
	}
	// UTF-8 bytes bound text tokens conservatively for this certified tokenizer.
	// Repeated query tokens are included once per document, using bounded int64.
	total := int64(len(query)) * int64(len(docs))
	for _, v := range docs {
		s, ok := v.(string)
		if !ok || strings.TrimSpace(s) == "" || len(s) > p.MaxItemTokens {
			return result, ErrInvalid
		}
		total += int64(len(s))
	}
	if total > int64(p.HardInputTokens) {
		return result, ErrInvalid
	}
	if v, exists := request["top_n"]; exists {
		n, e := confirmedCount(v)
		if e != nil || n < 1 || n > len(docs) {
			return result, ErrInvalid
		}
	}
	if v, exists := request["return_documents"]; exists {
		if _, ok := v.(bool); !ok {
			return result, ErrInvalid
		}
	}
	canonical, e := common.Marshal(original)
	if e != nil {
		return result, ErrInvalid
	}
	effective, e := common.Marshal(request)
	if e != nil {
		return result, ErrInvalid
	}
	snapshot, e := common.Marshal(frozenKnowledge{Policy: p, Body: effective})
	if e != nil {
		return result, ErrInvalid
	}
	return operationRequest{Model: p.PublicModel, Canonical: canonical, Quantity: p.HardInputTokens, MediaSnapshot: string(snapshot)}, nil
}

// KnowledgeTarget is shared by catalog projection and execution; channel family
// alone can never certify a particular upstream model or semantic space.
func (a *Adapter) KnowledgeTarget(model, wire string, channelID, channelType int, upstream, key string) bool {
	p, ok := a.config.KnowledgePolicies[wire][model]
	sum := sha256.Sum256([]byte(key))
	return p.ChannelKeySHA256 == hex.EncodeToString(sum[:]) && ok && p.valid(a.config, model, wire) && p.ChannelID == channelID && p.ChannelType == channelType && p.UpstreamModel == upstream
}
func (s *Session) KnowledgeBody(channel int, upstream string, key ...string) ([]byte, error) {
	var frozen frozenKnowledge
	if common.UnmarshalUnique([]byte(s.call.MediaSnapshot), &frozen) != nil || frozen.Policy.ChannelID != channel || frozen.Policy.UpstreamModel != upstream || frozen.Policy.PublicModel != s.call.Model || frozen.Policy.WireOperation != s.WireOperation().ID || len(frozen.Body) == 0 {
		return nil, ErrInvalid
	}
	if len(key) != 1 {
		return nil, ErrInvalid
	}
	sum := sha256.Sum256([]byte(key[0]))
	if frozen.Policy.ChannelKeySHA256 != hex.EncodeToString(sum[:]) {
		return nil, ErrInvalid
	}
	var request map[string]any
	if common.UnmarshalUnique(frozen.Body, &request) != nil {
		return nil, ErrInvalid
	}
	request["model"] = upstream
	return common.Marshal(request)
}

// KnowledgeBodyForRelay exposes the already-admitted canonical DTO to internal
// routing while the original envelope remains in the call digest.
func (s *Session) KnowledgeBodyForRelay() ([]byte, error) {
	var f frozenKnowledge
	if common.UnmarshalUnique([]byte(s.call.MediaSnapshot), &f) != nil || len(f.Body) == 0 {
		return nil, ErrInvalid
	}
	if s.WireOperation().ID == "multimodal.embeddings.create" && s.WireOperation().Mode == "knowledge-native-v1" {
		var r dto.MultimodalEmbeddingRequest
		if common.UnmarshalUnique(f.Body, &r) != nil {
			return nil, ErrInvalid
		}
		native := dto.QwenMultimodalEmbeddingRequest{Model: r.Model}
		native.Input.Contents = r.Input
		native.Parameters.Dimension = r.Dimensions
		native.Parameters.FPS = r.FPS
		native.Parameters.EnableFusion = r.EnableFusion
		return common.Marshal(native)
	}
	return append([]byte(nil), f.Body...), nil
}

// KnowledgePreparation is provided by the gateway's trusted media runtime after
// credential verification, before hashing or reservation. Returned bytes are the
// only media content dispatched. It must not invoke a model supplier.
type KnowledgePreparation func(context.Context, []byte, KnowledgePolicy) ([]byte, json.RawMessage, error)

func (a *Adapter) AdmitPreparedKnowledge(ctx context.Context, credential, task, id string, wire WireOperation, contentType string, body []byte, prepare KnowledgePreparation) (*Session, error) {
	return a.admitWireOperation(ctx, credential, task, id, wire, contentType, body, prepare)
}

func (a *Adapter) KnowledgeSpace(model, wire string) *dto.EmbeddingSpace {
	p := a.config.KnowledgePolicies[wire][model]
	if p.Space == nil {
		return nil
	}
	space := *p.Space
	return &space
}
