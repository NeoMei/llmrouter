package controller

import (
	"bytes"
	"net/http"

	"github.com/gin-gonic/gin"
)

// pendingResponseGate withholds a task adaptor's success response until its
// billing settlement and task handoff have committed. Task adaptors call
// c.JSON(200, ...) inside DoResponse, before RelayTask can settle the
// reservation; a settlement or handoff error must not leave the client with a
// committed success response for an untracked upstream job.
type pendingResponseGate struct {
	gin.ResponseWriter
	header  http.Header
	body    bytes.Buffer
	status  int
	written bool
}

func newPendingResponseGate(base gin.ResponseWriter) *pendingResponseGate {
	return &pendingResponseGate{ResponseWriter: base, header: base.Header().Clone(), status: http.StatusOK}
}

func (w *pendingResponseGate) Header() http.Header { return w.header }
func (w *pendingResponseGate) WriteHeader(status int) {
	if status > 0 && w.status != status {
		if w.written {
			return
		}
		w.status = status
	}
	w.written = true
}
func (w *pendingResponseGate) WriteHeaderNow() {}
func (w *pendingResponseGate) Flush()          {}
func (w *pendingResponseGate) Status() int     { return w.status }
func (w *pendingResponseGate) Size() int       { return w.body.Len() }
func (w *pendingResponseGate) Written() bool   { return w.written }
func (w *pendingResponseGate) Write(p []byte) (int, error) {
	w.written = true
	return w.body.Write(p)
}
func (w *pendingResponseGate) WriteString(s string) (int, error) {
	w.written = true
	return w.body.WriteString(s)
}

// reset clears a failed attempt's buffered response. A retry must not inherit
// its status, headers, or empty-but-written response state.
func (w *pendingResponseGate) reset() {
	w.header = w.ResponseWriter.Header().Clone()
	w.body.Reset()
	w.status = http.StatusOK
	w.written = false
}

// commit flushes the confirmed response to the real client.
func (w *pendingResponseGate) commit() {
	dst := w.ResponseWriter.Header()
	for key, values := range w.header {
		dst[key] = append([]string(nil), values...)
	}
	w.ResponseWriter.WriteHeader(w.status)
	w.ResponseWriter.WriteHeaderNow()
	if w.body.Len() > 0 {
		_, _ = w.ResponseWriter.Write(w.body.Bytes())
	}
}
