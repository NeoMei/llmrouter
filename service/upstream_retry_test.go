package service

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRelayErrorHandlerHonorsUpstreamRetryVeto(t *testing.T) {
	for _, body := range []string{`{"error":{"message":"do not resend","type":"server_error"}}`, `{"message":"do not resend"}`, `unavailable`} {
		for _, header := range []string{"false", "true", "", "False", "false,true"} {
			t.Run(body+"/"+header, func(t *testing.T) {
				response := &http.Response{StatusCode: http.StatusServiceUnavailable, Header: http.Header{"X-Should-Retry": {header}}, Body: io.NopCloser(strings.NewReader(body))}
				apiErr := RelayErrorHandler(context.Background(), response, true)
				require.NotNil(t, apiErr)
				assert.Equal(t, header == "false", types.IsSkipRetryError(apiErr))
				assert.Equal(t, http.StatusServiceUnavailable, apiErr.StatusCode)
				assert.Contains(t, apiErr.Error(), body)
			})
		}
	}
}
