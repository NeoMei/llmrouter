package relay

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/gin-gonic/gin"
)

const maxNativeRerankResponseBytes = 64 << 20

// nativeRerankResponseWriter withholds the adapter's canonical response until
// the native wrapper and actual ranking indices/scores have been validated.
type nativeRerankResponseWriter struct {
	gin.ResponseWriter
	header   http.Header
	body     bytes.Buffer
	status   int
	overflow bool
}

func (w *nativeRerankResponseWriter) Header() http.Header    { return w.header }
func (w *nativeRerankResponseWriter) WriteHeader(status int) { w.status = status }
func (w *nativeRerankResponseWriter) WriteHeaderNow()        {}
func (w *nativeRerankResponseWriter) Flush()                 {}
func (w *nativeRerankResponseWriter) Status() int            { return w.status }
func (w *nativeRerankResponseWriter) Size() int              { return w.body.Len() }
func (w *nativeRerankResponseWriter) Written() bool          { return w.body.Len() > 0 }
func (w *nativeRerankResponseWriter) Write(raw []byte) (int, error) {
	if w.body.Len()+len(raw) > maxNativeRerankResponseBytes {
		w.overflow = true
		return 0, fmt.Errorf("rerank response exceeds size limit")
	}
	return w.body.Write(raw)
}
func (w *nativeRerankResponseWriter) WriteString(s string) (int, error) { return w.Write([]byte(s)) }

func nativeRerankResponse(raw []byte, request *dto.RerankRequest) ([]byte, error) {
	var canonical struct {
		Results []json.RawMessage `json:"results"`
		Usage   json.RawMessage   `json:"usage"`
	}
	if err := common.Unmarshal(raw, &canonical); err != nil {
		return nil, err
	}
	count := len(request.Documents)
	if request.TopN != nil {
		count = *request.TopN
	}
	if err := dto.ValidateRerankResults(canonical.Results, len(request.Documents), count); err != nil {
		return nil, err
	}
	for i, rawItem := range canonical.Results {
		if request.ReturnDocuments != nil && !*request.ReturnDocuments {
			var fields map[string]json.RawMessage
			if err := common.Unmarshal(rawItem, &fields); err != nil {
				return nil, err
			}
			delete(fields, "document")
			var err error
			canonical.Results[i], err = common.Marshal(fields)
			if err != nil {
				return nil, err
			}
		}
	}
	return common.Marshal(struct {
		Output struct {
			Results []json.RawMessage `json:"results"`
		} `json:"output"`
		Usage json.RawMessage `json:"usage,omitempty"`
	}{Output: struct {
		Results []json.RawMessage `json:"results"`
	}{canonical.Results}, Usage: canonical.Usage})
}
