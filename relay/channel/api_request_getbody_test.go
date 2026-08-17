package channel

import (
	"bytes"
	"context"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
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

type getBodyTaskAdaptor struct {
	TaskAdaptor
	baseURL     string
	capturedReq *http.Request
}

func (a *getBodyTaskAdaptor) BuildRequestURL(*relaycommon.RelayInfo) (string, error) {
	return a.baseURL, nil
}

func (a *getBodyTaskAdaptor) BuildRequestHeader(_ *gin.Context, req *http.Request, _ *relaycommon.RelayInfo) error {
	a.capturedReq = req
	return nil
}

func TestDoTaskApiRequestPreservesNativeGetBody(t *testing.T) {
	service.InitHttpClient()
	payload := []byte(`{"model":"gpt-test","prompt":"hello"}`)
	type receivedBody struct {
		body []byte
		err  error
	}
	received := make(chan receivedBody, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body, err := io.ReadAll(req.Body)
		received <- receivedBody{body: body, err: err}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest(http.MethodPost, "/tasks", bytes.NewReader(payload))
	adaptor := &getBodyTaskAdaptor{baseURL: server.URL}
	resp, err := DoTaskApiRequest(adaptor, ctx, &relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{},
	}, bytes.NewReader(payload))
	require.NoError(t, err)
	defer resp.Body.Close()
	got := <-received
	require.NoError(t, got.err)
	assert.Equal(t, payload, got.body)

	require.NotNil(t, adaptor.capturedReq)
	require.NotNil(t, adaptor.capturedReq.GetBody)
	for range 2 {
		replay, err := adaptor.capturedReq.GetBody()
		require.NoError(t, err)
		got, err := io.ReadAll(replay)
		require.NoError(t, err)
		require.NoError(t, replay.Close())
		assert.Equal(t, payload, got)
	}
}

func TestApplyUpstreamBodyMetadata(t *testing.T) {
	getBody := func() (io.ReadCloser, error) {
		return io.NopCloser(strings.NewReader("payload")), nil
	}
	req, err := http.NewRequest(http.MethodPost, "https://example.invalid", io.NopCloser(strings.NewReader("payload")))
	require.NoError(t, err)
	info := &relaycommon.RelayInfo{UpstreamRequestBodySize: 7, UpstreamRequestGetBody: getBody}
	ApplyUpstreamBodyMetadata(req, info)
	assert.Equal(t, int64(7), req.ContentLength)
	require.NotNil(t, req.GetBody)
	replay, err := req.GetBody()
	require.NoError(t, err)
	defer replay.Close()
	got, err := io.ReadAll(replay)
	require.NoError(t, err)
	assert.Equal(t, "payload", string(got))
}

func TestApplyUpstreamBodyMetadataKeepsNativeGetBody(t *testing.T) {
	req, err := http.NewRequest(http.MethodPost, "https://example.invalid", bytes.NewReader([]byte("native")))
	require.NoError(t, err)
	native := req.GetBody
	ApplyUpstreamBodyMetadata(req, &relaycommon.RelayInfo{
		UpstreamRequestBodySize: 8,
		UpstreamRequestGetBody: func() (io.ReadCloser, error) {
			return io.NopCloser(strings.NewReader("fallback")), nil
		},
	})
	require.NotNil(t, req.GetBody)
	reader, err := req.GetBody()
	require.NoError(t, err)
	defer reader.Close()
	got, err := io.ReadAll(reader)
	require.NoError(t, err)
	assert.Equal(t, "native", string(got))
	assert.Equal(t, int64(len("native")), req.ContentLength)
	require.NotNil(t, native)
}

func TestUpstreamRequestTransportCanReplayBodyTwice(t *testing.T) {
	payload := []byte(`{"model":"gpt-test","input":"hello"}`)
	assertTransportReceivesIndependentReplays(t, payload, "application/json")
}

func TestUpstreamMultipartRequestTransportCanReplayBodyTwice(t *testing.T) {
	var payload bytes.Buffer
	writer := multipart.NewWriter(&payload)
	require.NoError(t, writer.WriteField("model", "whisper-1"))
	part, err := writer.CreateFormFile("file", "audio.txt")
	require.NoError(t, err)
	_, err = io.WriteString(part, "audio payload")
	require.NoError(t, err)
	require.NoError(t, writer.Close())

	assertTransportReceivesIndependentReplays(t, payload.Bytes(), writer.FormDataContentType())
}

func TestCanceledUpstreamRequestKeepsCompleteReplayBody(t *testing.T) {
	payload := []byte("complete payload")
	body, size, getBody, closer, err := relaycommon.NewOutboundJSONBody(payload)
	require.NoError(t, err)
	defer closer.Close()

	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://example.invalid", body)
	require.NoError(t, err)
	ApplyUpstreamBodyMetadata(req, &relaycommon.RelayInfo{
		UpstreamRequestBodySize: size,
		UpstreamRequestGetBody:  getBody,
	})
	cancel()

	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return nil, req.Context().Err()
	})}
	_, err = client.Do(req)
	require.ErrorIs(t, err, context.Canceled)

	replay, err := req.GetBody()
	require.NoError(t, err)
	defer replay.Close()
	got, err := io.ReadAll(replay)
	require.NoError(t, err)
	assert.Equal(t, payload, got)
}

func assertTransportReceivesIndependentReplays(t *testing.T, payload []byte, contentType string) {
	t.Helper()
	body, size, getBody, closer, err := relaycommon.NewOutboundJSONBody(payload)
	require.NoError(t, err)
	defer closer.Close()

	req, err := http.NewRequest(http.MethodPost, "https://example.invalid", body)
	require.NoError(t, err)
	req.Header.Set("Content-Type", contentType)
	ApplyUpstreamBodyMetadata(req, &relaycommon.RelayInfo{
		UpstreamRequestBodySize: size,
		UpstreamRequestGetBody:  getBody,
	})

	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		original, err := io.ReadAll(req.Body)
		require.NoError(t, err)
		assert.Equal(t, payload, original)
		assert.Equal(t, int64(len(payload)), req.ContentLength)

		for range 2 {
			replay, err := req.GetBody()
			require.NoError(t, err)
			got, readErr := io.ReadAll(replay)
			require.NoError(t, readErr)
			require.NoError(t, replay.Close())
			assert.Equal(t, payload, got)
		}

		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       http.NoBody,
			Request:    req,
		}, nil
	})}
	resp, err := client.Do(req)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
}
