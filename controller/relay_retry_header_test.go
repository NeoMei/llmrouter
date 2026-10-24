package controller

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRelayRetryPolicyHonorsUpstreamVeto(t *testing.T) {
	oldRanges := operation_setting.AutomaticRetryStatusCodeRanges
	operation_setting.AutomaticRetryStatusCodeRanges = []operation_setting.StatusCodeRange{{Start: 500, End: 503}}
	t.Cleanup(func() { operation_setting.AutomaticRetryStatusCodeRanges = oldRanges })
	for _, tc := range []struct {
		header          string
		retries         int
		specificChannel bool
		want            bool
	}{
		{"false", 3, false, false}, {"", 3, false, true},
		{"true", 0, false, false}, {"true", 3, true, false},
	} {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
		if tc.specificChannel {
			c.Set("specific_channel_id", 7)
		}
		resp := &http.Response{StatusCode: http.StatusServiceUnavailable, Header: http.Header{"X-Should-Retry": {tc.header}}, Body: io.NopCloser(strings.NewReader(`{"error":{"message":"busy","type":"server_error"}}`))}
		apiErr := service.RelayErrorHandler(c.Request.Context(), resp, false)
		require.NotNil(t, apiErr)
		assert.Equal(t, tc.want, shouldRetry(c, apiErr, tc.retries))
	}
}
