package helper

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type deadlineRecorder struct {
	*httptest.ResponseRecorder
	deadlines []time.Time
}

func (r *deadlineRecorder) SetWriteDeadline(deadline time.Time) error {
	r.deadlines = append(r.deadlines, deadline)
	return nil
}

func TestSetEventStreamHeadersBoundsStreamWritesByBudget(t *testing.T) {
	recorder := &deadlineRecorder{ResponseRecorder: httptest.NewRecorder()}
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", nil)
	SetEventStreamHeaders(c)

	before := time.Now()
	require.NoError(t, StringData(c, strings.Repeat("A", 3600<<10)))

	require.NotEmpty(t, recorder.deadlines)
	assert.False(t, recorder.deadlines[0].Before(before.Add(streamWriteTimeout+450*time.Second)))
	assert.Contains(t, recorder.Body.String(), "data: AAAA")
}

type headerCommitWriter struct {
	*httptest.ResponseRecorder
	headerStarted chan struct{}
	once          sync.Once
}

func (w *headerCommitWriter) WriteHeader(code int) {
	// Let the scanner start while the first keepalive snapshots the headers.
	w.once.Do(func() { close(w.headerStarted) })
	w.ResponseRecorder.WriteHeader(code)
}

func TestStreamScannerHandler_RequestKeepAliveHandoff(t *testing.T) {
	settings := operation_setting.GetGeneralSetting()
	previousEnabled, previousInterval := settings.PingIntervalEnabled, settings.PingIntervalSeconds
	settings.PingIntervalEnabled, settings.PingIntervalSeconds = true, 60
	t.Cleanup(func() {
		settings.PingIntervalEnabled, settings.PingIntervalSeconds = previousEnabled, previousInterval
	})

	for _, test := range []struct {
		name        string
		interval    time.Duration
		waitForPing bool
	}{
		{name: "before first keepalive", interval: time.Hour},
		{name: "during header commit", interval: time.Millisecond, waitForPing: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			writer := &headerCommitWriter{
				ResponseRecorder: httptest.NewRecorder(),
				headerStarted:    make(chan struct{}),
			}
			c, _ := gin.CreateTestContext(writer)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
			stop := StartStreamKeepAlive(c, test.interval)
			defer stop()
			if test.waitForPing {
				select {
				case <-writer.headerStarted:
				case <-time.After(time.Second):
					t.Fatal("keepalive did not start")
				}
			}

			resp := &http.Response{
				Header: http.Header{
					"X-Reasoning-Included": {"true"},
					"X-Codex-Turn-State":   {"turn-state"},
				},
				Body: io.NopCloser(strings.NewReader("data: chunk\n\ndata: [DONE]\n\n")),
			}
			info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{}}
			var received []string
			StreamScannerHandler(c, resp, info, func(data string, _ *StreamResult) {
				received = append(received, data)
				assert.NoError(t, StringData(c, data))
			})
			stop()

			require.NotNil(t, info.StreamStatus)
			assert.True(t, info.StreamStatus.IsNormalEnd())
			assert.Equal(t, []string{"chunk"}, received)
			assert.True(t, strings.HasSuffix(writer.Body.String(), "data: chunk\n\n"))
			if test.waitForPing {
				assert.True(t, strings.HasPrefix(writer.Body.String(), ": PING\n\n"))
			} else {
				assert.Equal(t, "data: chunk\n\n", writer.Body.String())
				assert.Equal(t, "true", writer.Result().Header.Get("X-Reasoning-Included"))
				assert.Equal(t, "turn-state", writer.Result().Header.Get("X-Codex-Turn-State"))
			}
		})
	}
}

func TestStreamWriteBudget(t *testing.T) {
	start := time.Unix(1_800_000_000, 0)
	var budget StreamWriteBudget
	steps := []struct {
		name  string
		at    time.Duration
		bytes int
		want  time.Duration
	}{
		{"small event", 0, 8 << 10, 31 * time.Second},
		{"image event gets the time a slow client needs", 0, 3600 << 10, 481 * time.Second},
		{"next write keeps the image budget", 60 * time.Second, 8 << 10, 482 * time.Second},
		{"budget starts over once it ran out", 600 * time.Second, 8 << 10, 631 * time.Second},
	}
	for _, step := range steps {
		assert.Equal(t, start.Add(step.want), budget.Deadline(start.Add(step.at), step.bytes), step.name)
	}
}
