package openai

import (
	"context"
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

func TestManagedCompactRequiresCompleteArtifactBeforeUsage(t *testing.T) {
	good := `{"id":"cmp","created_at":1700000000,"object":"response.compaction","output":[{"id":"ci","type":"compaction","encrypted_content":"opaque"}],"usage":{"input_tokens":10,"output_tokens":5,"total_tokens":15,"input_tokens_details":{"cached_tokens":2,"cache_write_tokens":3},"output_tokens_details":{"reasoning_tokens":1}}}`
	for _, tc := range []struct {
		name, body   string
		valid        bool
		unknownUsage bool
	}{
		{"native", good, true, false},
		{"native user message", strings.Replace(good, `"output":[`, `"output":[{"type":"message","role":"user","content":[{"type":"input_text","text":"preserved"}]},`, 1), true, false},
		{"missing envelope id", strings.Replace(good, `"id":"cmp",`, "", 1), false, false},
		{"missing timestamp", strings.Replace(good, `"created_at":1700000000,`, "", 1), false, false},
		{"no artifact", strings.Replace(good, `"output":[{"id":"ci","type":"compaction","encrypted_content":"opaque"}]`, `"output":[]`, 1), false, false},
		{"duplicate artifact", strings.Replace(good, `"output":[`, `"output":[{"id":"dup","type":"compaction","encrypted_content":"second"},`, 1), false, false},
		{"missing artifact id", strings.Replace(good, `"id":"ci",`, "", 1), false, false},
		{"bad output item", strings.Replace(good, `"output":[`, `"output":[{"type":"unsupported"},`, 1), false, false},
		{"duplicate counter", strings.Replace(good, `"input_tokens":10`, `"input_tokens":10,"input_tokens":11`, 1), false, false},
		{"body cap", strings.Repeat(" ", 16<<20) + good, false, false},
		{"numeric content discriminator", strings.Replace(good, `"output":[`, `"output":[{"type":"message","role":"user","content":[{"type":123}]},`, 1), false, false},
		{"missing text payload", strings.Replace(good, `"output":[`, `"output":[{"type":"message","role":"user","content":[{"type":"input_text"}]},`, 1), false, false},
		{"numeric text payload", strings.Replace(good, `"output":[`, `"output":[{"type":"message","role":"user","content":[{"type":"input_text","text":123}]},`, 1), false, false},
		{"missing image payload", strings.Replace(good, `"output":[`, `"output":[{"type":"message","role":"user","content":[{"type":"input_image","detail":"auto"}]},`, 1), false, false},
		{"numeric image payload", strings.Replace(good, `"output":[`, `"output":[{"type":"message","role":"user","content":[{"type":"input_image","image_url":123}]},`, 1), false, false},
		{"invalid image detail", strings.Replace(good, `"output":[`, `"output":[{"type":"message","role":"user","content":[{"type":"input_image","image_url":"data:image/png;base64,opaque","detail":123}]},`, 1), false, false},
		{"missing file payload", strings.Replace(good, `"output":[`, `"output":[{"type":"message","role":"user","content":[{"type":"input_file","filename":"doc.pdf"}]},`, 1), false, false},
		{"numeric file payload", strings.Replace(good, `"output":[`, `"output":[{"type":"message","role":"user","content":[{"type":"input_file","file_data":123}]},`, 1), false, false},
		{"numeric file name", strings.Replace(good, `"output":[`, `"output":[{"type":"message","role":"user","content":[{"type":"input_file","file_data":"opaque","filename":123}]},`, 1), false, false},
		{"unknown content type", strings.Replace(good, `"output":[`, `"output":[{"type":"message","role":"user","content":[{"type":"input_audio","data":"opaque"}]},`, 1), false, false},
		{"valid mixed native content", strings.Replace(good, `"output":[`, `"output":[{"type":"message","role":"user","content":[{"type":"input_text","text":""},{"type":"input_image","image_url":"data:image/png;base64,opaque","file_id":null,"detail":"auto"},{"type":"input_image","file_id":"file-image","image_url":null,"detail":"original"},{"type":"input_file","file_data":"opaque-pdf","filename":"doc.pdf","file_id":null},{"type":"input_file","file_url":"https://example.com/doc.pdf"},{"type":"input_file","file_id":"file-doc"}]},`, 1), true, false},
		{"preserved string content", strings.Replace(good, `"output":[`, `"output":[{"type":"message","role":"user","content":"preserved"},`, 1), true, false},
		{"joined usage key", strings.Replace(good, `"input_tokens":10`, `"input_tokens":10,"input_tokens output_tokens":123`, 1), false, true},
		{"joined input details key", strings.Replace(good, `"cached_tokens":2`, `"cached_tokens":2,"cached_tokens cache_write_tokens":123`, 1), false, true},
		{"unknown output details key", strings.Replace(good, `"reasoning_tokens":1`, `"reasoning_tokens":1,"reasoning_tokens extra":123`, 1), false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var observed metering.Usage
			price := metering.Price{Version: "metered_v2", Operation: "responses", Policy: "sum"}
			for _, k := range []string{"input", "output", "cache_read", "cache_write", "reasoning"} {
				price.Terms = append(price.Terms, metering.Term{Key: k, Unit: "token", Price: metering.Rational{Numerator: "1", Denominator: "1"}})
			}
			info := &relaycommon.RelayInfo{PlatformMode: true, NativeBilling: &relaycommon.NativeBillingContext{Platform: true, Price: price, Observe: func(_ context.Context, u metering.Usage) error { observed = u; return nil }}}
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest("POST", "/v1/responses/compact", nil)
			usage, apiErr := OaiResponsesCompactionHandler(c, &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(tc.body))}, info)
			charge, chargeErr := metering.Calculate(price, observed)
			if tc.valid {
				require.NoError(t, chargeErr)
				assert.Equal(t, "15", charge.RatString())
				require.Nil(t, apiErr)
				require.IsType(t, &dto.Usage{}, usage)
				assert.Equal(t, "confirmed", observed.State)
				assert.JSONEq(t, tc.body, w.Body.String())
				values := map[string]string{}
				for _, m := range observed.Measurements {
					values[m.Key] = m.Quantity.Value
				}
				assert.Equal(t, map[string]string{"input": "5", "output": "4", "cache_read": "2", "cache_write": "3", "reasoning": "1"}, values)
			} else {
				assert.Error(t, chargeErr)
				assert.Nil(t, charge)
				if tc.unknownUsage {
					require.Nil(t, apiErr)
					assert.NotEqual(t, "confirmed", observed.State)
					assert.JSONEq(t, tc.body, w.Body.String())
					return
				}
				require.NotNil(t, apiErr)
				assert.NotEqual(t, "confirmed", observed.State)
				assert.Empty(t, w.Body.String())
			}
		})
	}
}

func TestNativeCompactRetainsUnmanagedPassthrough(t *testing.T) {
	body := `{"id":"cmp","created_at":1700000000,"object":"response.compaction","output":[{"type":"message","role":"user","content":[{"type":123}]},{"id":"ci","type":"compaction","encrypted_content":"opaque"}],"usage":{"input_tokens":10,"input_tokens output_tokens":123,"output_tokens":5,"total_tokens":15,"input_tokens_details":{"cached_tokens":2,"cache_write_tokens":3},"output_tokens_details":{"reasoning_tokens":1}}}`
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/v1/responses/compact", nil)
	usage, apiErr := OaiResponsesCompactionHandler(c, &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, &relaycommon.RelayInfo{})
	require.Nil(t, apiErr)
	require.NotNil(t, usage)
	assert.Equal(t, 15, usage.TotalTokens)
	assert.JSONEq(t, body, w.Body.String())
}
