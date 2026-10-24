package openai

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/constant"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/service/responsesstate"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type responsesDeliveryWriter struct {
	*httptest.ResponseRecorder
	failWrite     int
	writes        int
	writeErr      error
	flushErr      error
	cancel        context.CancelFunc
	cancelOnWrite bool
}

func (w *responsesDeliveryWriter) Write(p []byte) (int, error) {
	w.writes++
	if w.writes == w.failWrite {
		return 0, w.writeErr
	}
	n, err := w.ResponseRecorder.Write(p)
	if w.cancelOnWrite && w.cancel != nil {
		w.cancel()
	}
	return n, err
}
func (w *responsesDeliveryWriter) Flush() {
	w.ResponseRecorder.Flush()
	if w.cancel != nil {
		w.cancel()
	}
}
func (w *responsesDeliveryWriter) FlushError() error { return w.flushErr }

func TestResponsesStreamFinalDelivery(t *testing.T) {
	previous := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() { constant.StreamingTimeout = previous })
	for _, tc := range []struct {
		name          string
		failWrite     int
		failFlush     bool
		cancelOnWrite bool
		cancelOnFlush bool
		wantError     bool
		eof           bool
	}{
		{name: "delivered completion followed by cancellation", cancelOnFlush: true},
		{name: "completed followed by clean EOF", eof: true},
		{name: "event write failure", failWrite: 1, wantError: true},
		{name: "data write failure", failWrite: 2, wantError: true},
		{name: "delimiter write failure", failWrite: 3, wantError: true},
		{name: "flush failure", failFlush: true, wantError: true},
		{name: "cancel before final flush", cancelOnWrite: true, wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			writeErr := errors.New("downstream delivery failed")
			w := &responsesDeliveryWriter{ResponseRecorder: httptest.NewRecorder(), failWrite: tc.failWrite, writeErr: writeErr, cancelOnWrite: tc.cancelOnWrite}
			if tc.failFlush {
				w.flushErr = writeErr
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tc.cancelOnFlush || tc.cancelOnWrite {
				w.cancel = cancel
			}
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(ctx)
			t.Setenv("RESPONSES_STATE_ENABLED", "1")
			responsesstate.ResetForTest()
			t.Cleanup(responsesstate.ResetForTest)
			store, err := responsesstate.Default()
			require.NoError(t, err)
			scope := responsesstate.Scope{TenantID: 1, UserID: 1, TokenID: 2, RouteID: 3, TargetID: 4, ChannelID: 5, Model: "model-a"}
			c.Set(string(constant.ContextKeyResponsesContinuity), responsesstate.ContinuityContext{Scope: scope, CurrentInput: []byte(`[]`)})
			info := &relaycommon.RelayInfo{DisablePing: true, ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "model-a"}}
			body := "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_delivery\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":7,\"output_tokens\":5,\"total_tokens\":12}}}\n\ndata: [DONE]\n\n"
			if tc.eof {
				body = strings.TrimSuffix(body, "data: [DONE]\n\n")
			}
			usage, apiErr := OaiResponsesStreamHandler(c, info, &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))})
			_, persistErr := store.Load(context.Background(), scope, "resp_delivery")
			if tc.wantError || tc.cancelOnFlush {
				assert.ErrorIs(t, persistErr, responsesstate.ErrNotFound)
			} else {
				require.NoError(t, persistErr)
			}
			if tc.wantError {
				if tc.failWrite > 0 || tc.failFlush {
					assert.True(t, types.IsSkipRetryError(apiErr))
				}
				require.NotNil(t, apiErr)
				assert.Nil(t, usage)
				return
			}
			require.Nil(t, apiErr)
			require.NotNil(t, usage)
			assert.Equal(t, 7, usage.PromptTokens)
			assert.Equal(t, 5, usage.CompletionTokens)
			if tc.eof {
				assert.Equal(t, relaycommon.StreamEndReasonEOF, info.StreamStatus.EndReason)
			} else {
				assert.Equal(t, relaycommon.StreamEndReasonDone, info.StreamStatus.EndReason)
			}
			assert.False(t, info.StreamStatus.HasErrors())
		})
	}
}

func TestResponsesStreamRequiresDeliveredTerminal(t *testing.T) {
	previous := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() { constant.StreamingTimeout = previous })
	for _, body := range []string{"data: [DONE]\n\n", "", "data: null\n\ndata: [DONE]\n\n"} {
		t.Run(body, func(t *testing.T) {
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
			info := &relaycommon.RelayInfo{DisablePing: true, ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "model-a"}}
			usage, apiErr := OaiResponsesStreamHandler(c, info, &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))})
			require.NotNil(t, apiErr)
			assert.Nil(t, usage)
			assert.Empty(t, rec.Body.String())
		})
	}
}
