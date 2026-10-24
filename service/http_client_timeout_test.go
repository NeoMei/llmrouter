package service

import (
	"context"
	"io"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRelayTransportBoundsResponseHeaderWait(t *testing.T) {
	old := common.RelayResponseHeaderTimeout
	t.Cleanup(func() { common.RelayResponseHeaderTimeout = old })
	for _, tc := range []struct {
		seconds int
		want    time.Duration
	}{
		{1800, 30 * time.Minute}, {0, 0}, {-1, 0}, {math.MaxInt, time.Duration(min(int64(math.MaxInt), math.MaxInt64/int64(time.Second))) * time.Second},
	} {
		common.RelayResponseHeaderTimeout = tc.seconds
		transport := newRelayHTTPTransport()
		assert.Equal(t, tc.want, transport.ResponseHeaderTimeout)
		transport.CloseIdleConnections()
	}
}

func TestRelayHeaderTimeoutDoesNotLimitResponseBody(t *testing.T) {
	oldHeaderTimeout, oldRelayTimeout := common.RelayResponseHeaderTimeout, common.RelayTimeout
	t.Cleanup(func() { common.RelayResponseHeaderTimeout, common.RelayTimeout = oldHeaderTimeout, oldRelayTimeout })
	common.RelayResponseHeaderTimeout, common.RelayTimeout = 1, 0
	const headerWait = 20 * time.Millisecond
	bodyHeadersReceived := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/headers" {
			<-r.Context().Done()
			return
		}
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		select {
		case <-bodyHeadersReceived:
		case <-r.Context().Done():
			return
		}
		// Start this delay after the client has received headers, so the body
		// must remain readable beyond the transport's response-header limit.
		timer := time.NewTimer(2 * headerWait)
		defer timer.Stop()
		select {
		case <-timer.C:
			_, _ = io.WriteString(w, "delayed stream body")
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(server.Close)
	transport := newRelayHTTPTransport()
	// Shorten only the test's configured header timer; no timing/throughput assertion.
	if transport.ResponseHeaderTimeout > 0 {
		transport.ResponseHeaderTimeout = headerWait
	}
	t.Cleanup(transport.CloseIdleConnections)
	client := newRelayHTTPClient(transport)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/headers", nil)
	require.NoError(t, err)
	_, err = client.Do(request)
	require.Error(t, err)
	var netErr net.Error
	require.ErrorAs(t, err, &netErr)
	assert.True(t, netErr.Timeout())
	assert.NoError(t, ctx.Err(), "the header timer must fire before the caller deadline")

	bodyCtx, bodyCancel := context.WithTimeout(context.Background(), time.Second)
	defer bodyCancel()
	request, err = http.NewRequestWithContext(bodyCtx, http.MethodGet, server.URL+"/stream", nil)
	require.NoError(t, err)
	response, err := client.Do(request)
	require.NoError(t, err)
	defer response.Body.Close()
	assert.Zero(t, client.Timeout, "only waiting for headers is bounded, not the whole stream")
	close(bodyHeadersReceived)
	body, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	assert.Equal(t, "delayed stream body", string(body))
	assert.NoError(t, bodyCtx.Err())
}
