package minimax

import (
	"bytes"
	"context"
	"errors"
	"github.com/QuantumNous/new-api/constant"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/service/accountpool"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// This body blocks after its first real packet until Close; returning from the
// handler proves scanner cancellation and join, rather than a cooperative EOF.
type speechBlockingBody struct {
	initial *bytes.Reader
	closed  chan struct{}
	once    sync.Once
}

func (b *speechBlockingBody) Read(p []byte) (int, error) {
	n, e := b.initial.Read(p)
	if n > 0 {
		return n, nil
	}
	if e == io.EOF {
		<-b.closed
		return 0, errors.New("closed")
	}
	return n, e
}
func (b *speechBlockingBody) Close() error { b.once.Do(func() { close(b.closed) }); return nil }

type speechWriter struct {
	*httptest.ResponseRecorder
	cancel    context.CancelFunc
	fail      bool
	deadlines []time.Time
}

func (w *speechWriter) Write(p []byte) (int, error) {
	if w.fail {
		return 0, errors.New("client write failure")
	}
	n, e := w.ResponseRecorder.Write(p)
	if w.cancel != nil {
		w.cancel()
	}
	return n, e
}
func (w *speechWriter) SetWriteDeadline(d time.Time) error {
	w.deadlines = append(w.deadlines, d)
	return nil
}
func TestNativeSpeechStreamStopsAndJoins(t *testing.T) {
	for _, mode := range []string{"write-failure", "cancel", "idle-timeout"} {
		t.Run(mode, func(t *testing.T) {
			old := constant.StreamingTimeout
			constant.StreamingTimeout = 1
			t.Cleanup(func() { constant.StreamingTimeout = old })
			settings := operation_setting.GetGeneralSetting()
			oldEnabled, oldInterval := settings.PingIntervalEnabled, settings.PingIntervalSeconds
			settings.PingIntervalEnabled, settings.PingIntervalSeconds = true, 1
			t.Cleanup(func() { settings.PingIntervalEnabled, settings.PingIntervalSeconds = oldEnabled, oldInterval })
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			writer := &speechWriter{ResponseRecorder: httptest.NewRecorder(), fail: mode == "write-failure"}
			if mode == "cancel" {
				writer.cancel = cancel
			}
			c, _ := gin.CreateTestContext(writer)
			c.Request = httptest.NewRequest("POST", "/v1/audio/speech", nil).WithContext(ctx)
			c.Set("binary_stream_content_type", "audio/mpeg")
			accountpool.DeferAllStatusNativeOutcome(c)
			require.NoError(t, accountpool.BeginSend(c))
			body := &speechBlockingBody{initial: bytes.NewReader([]byte("data: {\"base_resp\":{\"status_code\":0},\"data\":{\"audio\":\"494433\",\"status\":1}}\n\n")), closed: make(chan struct{})}
			resp := &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: body}
			require.NoError(t, accountpool.Observe(c, resp, nil))
			info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{}, IsStream: true, DisablePing: true, OutputAudioFormat: "mp3"}
			usage, apiErr := handleTTSResponse(c, resp, info)
			require.NotNil(t, apiErr)
			assert.Nil(t, usage)
			assert.False(t, info.ActualUsageValidated)
			select {
			case <-body.closed:
			default:
				t.Fatal("upstream was not closed")
			}
			require.NotEmpty(t, writer.deadlines)
			assert.Equal(t, "audio/mpeg", writer.Header().Get("Content-Type"))
			assert.NotContains(t, writer.Body.String(), "data:")
			assert.NotContains(t, writer.Body.String(), "PING")
			if mode != "write-failure" {
				assert.Equal(t, "ID3", writer.Body.String())
			}
			assert.False(t, accountpool.RetryAllowed(c))
		})
	}
}
