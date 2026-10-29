package platformbilling

import (
	"bytes"
	"io"
	"net/http"
	"reflect"
	"strings"

	"github.com/QuantumNous/new-api/common"
)

// This private prototype is not wired into the existing relay. A future
// integration must validate the destination and bind the response to this send.
// It compares the body consumed at the send boundary to the frozen request,
// then rebuilds replay bytes from the same validated body.
func (s *Session) validateKnowledgeOutbound(channel int, upstream, key string, req *http.Request) error {
	if s == nil || s.adapter == nil || req == nil || req.Method != http.MethodPost || req.Body == nil || len(req.Header.Values("Authorization")) != 1 || req.Header.Get("Authorization") != "Bearer "+key {
		return ErrInvalid
	}
	for name := range req.Header {
		if strings.EqualFold(name, "Authorization") && name != "Authorization" {
			return ErrInvalid
		}
	}
	wire := s.WireOperation()
	if wire.ID != "embeddings.create" && wire.ID != "rerank.create" {
		return ErrInvalid
	}
	frozen, err := s.KnowledgeBody(channel, upstream, key)
	if err != nil {
		return ErrInvalid
	}
	var admitted map[string]any
	if common.UnmarshalUnique(frozen, &admitted) != nil {
		return ErrInvalid
	}
	var expected map[string]any
	if wire.ID == "embeddings.create" {
		expected = admitted
		// Empty optional strings are omitted by the provider DTO's JSON encoding.
		if expected["user"] == "" {
			delete(expected, "user")
		}
	} else {
		parameters := map[string]any{}
		if top, ok := admitted["top_n"]; ok {
			parameters["top_n"] = top
		}
		if ret, ok := admitted["return_documents"]; ok {
			parameters["return_documents"] = ret
		} else if wire.Mode != "knowledge-native-v1" {
			parameters["return_documents"] = true
		}
		expected = map[string]any{"model": upstream, "input": map[string]any{"query": admitted["query"], "documents": admitted["documents"]}, "parameters": parameters}
	}
	defer req.Body.Close()
	limit := s.adapter.config.MaxBodyBytes + 4096
	if limit < 4096 || limit > 16<<20 {
		return ErrInvalid
	}
	raw, err := io.ReadAll(io.LimitReader(req.Body, limit+1))
	if err != nil || int64(len(raw)) > limit {
		return ErrInvalid
	}
	var actual map[string]any
	if common.UnmarshalUnique(raw, &actual) != nil || !reflect.DeepEqual(expected, actual) {
		return ErrInvalid
	}
	req.Body = io.NopCloser(bytes.NewReader(raw))
	// Go's transport may replay a POST after a stale connection when GetBody
	// and an idempotency header coexist. One reservation permits one attempt.
	req.GetBody = nil
	for name := range req.Header {
		if strings.EqualFold(name, "Idempotency-Key") || strings.EqualFold(name, "X-Idempotency-Key") {
			delete(req.Header, name)
		}
	}
	req.ContentLength = int64(len(raw))
	return nil
}

// ValidateAliEmbeddingOutbound binds the bytes and destination of this one
// certified managed operation immediately before opening the supplier socket.
func (s *Session) ValidateAliEmbeddingOutbound(channel int, upstream, key string, req *http.Request) error {
	if !s.AliEmbeddingV4() || req == nil || req.URL == nil || req.Host != req.URL.Host || req.Header.Get("X-DashScope-Plugin") != "" {
		return ErrInvalid
	}
	var frozen frozenKnowledge
	if common.UnmarshalUnique([]byte(s.call.MediaSnapshot), &frozen) != nil || req.URL.String() != frozen.Policy.Origin+"/compatible-mode/v1/embeddings" {
		return ErrInvalid
	}
	return s.validateKnowledgeOutbound(channel, upstream, key, req)
}

func (s *Session) ValidateAliRerankOutbound(channel int, upstream, key string, req *http.Request) error {
	if !s.AliRerank() || req == nil || req.URL == nil || req.Host != req.URL.Host || req.URL.String() != "https://dashscope.aliyuncs.com/api/v1/services/rerank/text-rerank/text-rerank" || req.Header.Get("X-DashScope-Plugin") != "" {
		return ErrInvalid
	}
	return s.validateKnowledgeOutbound(channel, upstream, key, req)
}
