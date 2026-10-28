package ali

import (
	"context"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/pkg/metering"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestKnowledgeRerankActualEvidence(t *testing.T) {
	for _, tc := range []struct {
		name, usage, results string
		valid                bool
	}{
		{"three", `{"total_tokens":3}`, `[{"index":0,"relevance_score":0.9}]`, true},
		{"zero", `{"total_tokens":0}`, `[{"index":0,"relevance_score":0.9}]`, true},
		{"missing", `{}`, `[{"index":0,"relevance_score":0.9}]`, false},
		{"duplicate", `{"total_tokens":3,"total_tokens":3}`, `[{"index":0,"relevance_score":0.9}]`, false},
		{"extra", `{"total_tokens":3,"image_tokens":1}`, `[{"index":0,"relevance_score":0.9}]`, false},
		{"invalidresult", `{"total_tokens":3}`, `[{"index":2,"relevance_score":0.9}]`, false},
		{"wrong-document", `{"total_tokens":3}`, `[{"index":0,"relevance_score":0.9,"document":{"text":"foreign"}}]`, false},
		{"neg", `{"total_tokens":-1}`, `[{"index":0,"relevance_score":0.9}]`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest("POST", "/v1/rerank", nil)
			var observed []metering.Usage
			info := &relaycommon.RelayInfo{PlatformMode: true, Request: &dto.RerankRequest{Model: "qwen3-rerank", Query: "q", Documents: []any{"a"}, TopN: common.GetPointer(1)}, NativeBilling: &relaycommon.NativeBillingContext{Platform: true, Price: metering.Price{Version: "metered_v2", Operation: "rerank", Policy: "provider_reported_v1", Terms: []metering.Term{{Key: "input", Unit: "token", Price: metering.Rational{Numerator: "1", Denominator: "2"}}}}, Observe: func(_ context.Context, u metering.Usage) error { observed = append(observed, u); return nil }}}
			err, _ := RerankHandler(c, &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"output":{"results":` + tc.results + `},"usage":` + tc.usage + `}`))}, info)
			if tc.valid {
				require.Nil(t, err)
				require.Len(t, observed, 1)
				assert.Equal(t, "confirmed", observed[0].State)
				require.Len(t, observed[0].Measurements, 1)
				assert.Equal(t, "input", observed[0].Measurements[0].Key)
			} else {
				require.NotNil(t, err)
				for _, u := range observed {
					assert.NotEqual(t, "confirmed", u.State)
				}
			}
		})
	}
}
