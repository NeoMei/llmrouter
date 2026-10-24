package channel

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/service/accountpool"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUnsafeCreateTransportReturnsExecutionUncertainty(t *testing.T) {
	service.InitHttpClient()
	for _, path := range []string{"/v1/videos", "/v1/edits", "/v1/video/generations"} {
		for _, networkFailure := range []bool{false, true} {
			t.Run(path+"/"+map[bool]string{false: "upstream-5xx", true: "unknown-network"}[networkFailure], func(t *testing.T) {
				var sends atomic.Int32
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					sends.Add(1)
					if networkFailure {
						connection, _, err := w.(http.Hijacker).Hijack()
						require.NoError(t, err)
						connection.Close()
						return
					}
					w.WriteHeader(502)
				}))
				defer upstream.Close()
				c, _ := gin.CreateTestContext(httptest.NewRecorder())
				c.Request = httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{}`))
				req, err := http.NewRequest(http.MethodPost, upstream.URL, strings.NewReader(`{}`))
				require.NoError(t, err)
				_, err = DoRequest(c, req, &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{}})
				var apiErr *types.NewAPIError
				require.ErrorAs(t, err, &apiErr)
				assert.Equal(t, types.ErrorCode("upstream_execution_uncertain"), apiErr.GetErrorCode())
				assert.False(t, accountpool.RetryAllowed(c))
				assert.Equal(t, int32(1), sends.Load())
			})
		}
	}

}

func TestCancelledTransportDoesNotSend(t *testing.T) {
	service.InitHttpClient()
	var sends atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { sends.Add(1); w.WriteHeader(200) }))
	defer upstream.Close()
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{}`))
	ctx, cancel := context.WithCancel(c.Request.Context())
	cancel()
	c.Request = c.Request.WithContext(ctx)
	req, err := http.NewRequest(http.MethodPost, upstream.URL, strings.NewReader(`{}`))
	require.NoError(t, err)
	_, err = DoRequest(c, req, &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{}})
	require.ErrorIs(t, err, context.Canceled)
	assert.Zero(t, sends.Load())
	common.CleanupBodyStorage(c)
}

func TestFinalProviderBodyCannotIntroduceUnownedResource(t *testing.T) {
	service.InitHttpClient()
	var sends atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { sends.Add(1); w.WriteHeader(200) }))
	defer upstream.Close()
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"messages":[]}`))
	accountpool.State(c).Selected = &model.UpstreamAccountSelection{}
	req, err := http.NewRequest(http.MethodPost, upstream.URL, strings.NewReader(`{"messages":[],"previous_response_id":"introduced-by-override"}`))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	_, err = DoRequest(c, req, &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{}})
	require.Error(t, err)
	assert.Zero(t, sends.Load())
	assert.Zero(t, accountpool.State(c).Sends)
	assert.False(t, accountpool.RetryAllowed(c))
}

func TestPooledRealtimeRejectsBeforeDial(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/realtime", nil)
	accountpool.State(c).Selected = &model.UpstreamAccountSelection{}
	// A nil adaptor would panic if URL/header/dial processing were reached.
	connection, err := DoWssRequest(nil, c, &relaycommon.RelayInfo{}, nil)
	require.Nil(t, connection)
	require.Error(t, err)
	var apiErr *types.NewAPIError
	require.ErrorAs(t, err, &apiErr)
	assert.True(t, types.IsSkipRetryError(apiErr))
	assert.Zero(t, accountpool.State(c).Sends)
}

type latePooledWebsocketAdaptor struct{ getBodyAdaptor }

func (a *latePooledWebsocketAdaptor) SetupRequestHeader(c *gin.Context, _ *http.Header, _ *relaycommon.RelayInfo) error {
	accountpool.State(c).Selected = &model.UpstreamAccountSelection{}
	return nil
}

func TestLatePooledRealtimeRejectsBeforeDial(t *testing.T) {
	var sends atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { sends.Add(1); w.WriteHeader(500) }))
	defer upstream.Close()
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/realtime", nil)
	adaptor := &latePooledWebsocketAdaptor{getBodyAdaptor{requestURL: "ws" + strings.TrimPrefix(upstream.URL, "http")}}
	connection, err := DoWssRequest(adaptor, c, &relaycommon.RelayInfo{}, nil)
	require.Nil(t, connection)
	var apiErr *types.NewAPIError
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, types.ErrorCodeUnsupportedOperation, apiErr.GetErrorCode())
	assert.Zero(t, sends.Load())
	assert.Zero(t, accountpool.State(c).Sends)
}
