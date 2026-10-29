package router

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOwnedTaskReadRequiresMatchingOperationRoute(t *testing.T) {
	read := "uh_read_" + strings.Repeat("b", 64)
	t.Run("video call", func(t *testing.T) {
		f := newWanVideoGatewayFixture(t, "720")
		require.Equal(t, http.StatusOK, f.submit(t, "720P").Code)
		status := f.read(t, "", read)
		require.Equal(t, http.StatusOK, status.Code, status.Body.String())
		for _, suffix := range []string{"", "/result"} {
			req := httptest.NewRequest(http.MethodGet, "/platform/v1/ocr/jobs/wan-call"+suffix, nil)
			req.Header.Set("Authorization", "Bearer "+read)
			w := httptest.NewRecorder()
			f.engine.ServeHTTP(w, req)
			assert.Equal(t, http.StatusNotFound, w.Code, "route %s", suffix)
		}
	})
	t.Run("ocr call", func(t *testing.T) {
		f := newOCRGatewayFixture(t, "success", "", "", "", "")
		ct, body := ocrMultipart(t, f.image)
		require.Equal(t, http.StatusOK, f.request(t, "POST", "/platform/api/v2/ocr/jobs", "uh_model_"+strings.Repeat("a", 64), ct, body).Code)
		status := f.request(t, "GET", "/platform/v1/ocr/jobs/ocr-call", read, "", nil)
		require.Equal(t, http.StatusOK, status.Code, status.Body.String())
		for _, suffix := range []string{"", "/result"} {
			w := f.request(t, "GET", "/platform/v1/videos/ocr-call"+suffix, read, "", nil)
			assert.Equal(t, http.StatusNotFound, w.Code, "route %s", suffix)
		}
	})
}
