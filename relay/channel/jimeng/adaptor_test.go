package jimeng

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}

func TestDoRequestAppliesReplayMetadata(t *testing.T) {
	payload := []byte(`{"req_key":"jimeng-test","prompt":"hello"}`)
	body, size, getBody, closer, err := relaycommon.NewOutboundJSONBody(payload)
	require.NoError(t, err)
	defer closer.Close()

	service.InitHttpClient()
	client := service.GetHttpClient()
	previous := client.Transport
	client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		assert.Equal(t, int64(len(payload)), req.ContentLength)
		require.NotNil(t, req.GetBody)
		replay, err := req.GetBody()
		require.NoError(t, err)
		defer replay.Close()
		got, err := io.ReadAll(replay)
		require.NoError(t, err)
		assert.Equal(t, payload, got)
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       http.NoBody,
			Request:    req,
		}, nil
	})
	t.Cleanup(func() { client.Transport = previous })

	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", http.NoBody)
	resp, err := (&Adaptor{}).DoRequest(ctx, &relaycommon.RelayInfo{
		ChannelMeta:             &relaycommon.ChannelMeta{ChannelBaseUrl: "https://example.invalid", ApiKey: "ak|sk"},
		UpstreamRequestBodySize: size,
		UpstreamRequestGetBody:  getBody,
	}, body)
	require.NoError(t, err)
	require.NoError(t, resp.(*http.Response).Body.Close())
}
