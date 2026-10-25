package relay

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"net/http"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/relay/toolpolicy"
	"github.com/gin-gonic/gin"
)

const maxContractEmbeddingResponseBytes = 64 << 20

// embeddingResponseGate holds the adapter-normalized response until its real
// vectors pass validation. It never exposes headers, writes or flushes to the
// client, including when the adapter formats a native upstream response.
type embeddingResponseGate struct {
	gin.ResponseWriter
	header   http.Header
	body     bytes.Buffer
	status   int
	overflow bool
}

func (w *embeddingResponseGate) Header() http.Header    { return w.header }
func (w *embeddingResponseGate) WriteHeader(status int) { w.status = status }
func (w *embeddingResponseGate) WriteHeaderNow()        {}
func (w *embeddingResponseGate) Flush()                 {}
func (w *embeddingResponseGate) Status() int            { return w.status }
func (w *embeddingResponseGate) Size() int              { return w.body.Len() }
func (w *embeddingResponseGate) Written() bool          { return w.body.Len() > 0 }
func (w *embeddingResponseGate) Write(p []byte) (int, error) {
	if w.body.Len()+len(p) > maxContractEmbeddingResponseBytes {
		w.overflow = true
		return 0, fmt.Errorf("embedding response exceeds size limit")
	}
	return w.body.Write(p)
}
func (w *embeddingResponseGate) WriteString(s string) (int, error) { return w.Write([]byte(s)) }

// normalizeEmbeddingResponse validates vectors and arranges complete indexed
// batches in input order. Whole raw items retain vendor extensions and numeric
// lexemes; float decoding below is validation only, never output serialization.
func normalizeEmbeddingResponse(raw []byte, space dto.EmbeddingSpace, count int) ([]byte, error) {
	if err := toolpolicy.ValidateRawResponseJSON(raw); err != nil {
		return nil, err
	}
	var response struct {
		Model string            `json:"model"`
		Data  []json.RawMessage `json:"data"`
	}
	if err := common.Unmarshal(raw, &response); err != nil {
		return nil, fmt.Errorf("invalid numeric embedding response: %w", err)
	}
	if response.Model != space.Model {
		return nil, fmt.Errorf("embedding response model differs from fixed embedding_space")
	}
	if len(response.Data) != count {
		return nil, fmt.Errorf("embedding response cardinality differs from input batch")
	}
	ordered := make([]json.RawMessage, count)
	needsOrder := false
	for position, rawItem := range response.Data {
		var item struct {
			Index     *int              `json:"index"`
			Embedding []json.RawMessage `json:"embedding"`
		}
		if err := common.Unmarshal(rawItem, &item); err != nil {
			return nil, fmt.Errorf("invalid numeric embedding result: %w", err)
		}
		if item.Index == nil || *item.Index < 0 || *item.Index >= count || ordered[*item.Index] != nil {
			return nil, fmt.Errorf("embedding response indices must be present, unique and within the input batch")
		}
		if len(item.Embedding) != space.Dimensions {
			return nil, fmt.Errorf("embedding response dimensions differ from fixed embedding_space")
		}
		for _, rawValue := range item.Embedding {
			if len(rawValue) == 0 || bytes.Equal(rawValue, []byte("null")) {
				return nil, fmt.Errorf("embedding values must be finite numbers")
			}
			var value float64
			if err := common.Unmarshal(rawValue, &value); err != nil || math.IsNaN(value) || math.IsInf(value, 0) {
				return nil, fmt.Errorf("embedding values must be finite numbers")
			}
		}
		ordered[*item.Index] = rawItem
		needsOrder = needsOrder || *item.Index != position
	}
	if !needsOrder {
		return raw, nil
	}
	var fields map[string]json.RawMessage
	if err := common.Unmarshal(raw, &fields); err != nil {
		return nil, err
	}
	orderedJSON, err := common.Marshal(ordered)
	if err != nil {
		return nil, err
	}
	fields["data"] = orderedJSON
	return common.Marshal(fields)
}
