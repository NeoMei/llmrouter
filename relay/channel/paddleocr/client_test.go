package paddleocr

import (
	"context"
	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPaddleWire(t *testing.T) {
	for _, urlInput := range []bool{false, true} {
		t.Run(map[bool]string{false: "multipart", true: "url"}[urlInput], func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, "Bearer fixture", r.Header.Get("Authorization"))
				assert.Equal(t, "/api/v2/ocr/jobs", r.URL.Path)
				if urlInput {
					var v map[string]any
					require.NoError(t, common.DecodeJson(r.Body, &v))
					assert.Equal(t, "https://owned.example/resource", v["fileUrl"])
					assert.Equal(t, map[string]any{"useTableRecognition": false}, v["optionalPayload"])
				} else {
					require.NoError(t, r.ParseMultipartForm(1024))
					assert.Equal(t, "PaddleOCR-VL-1.6", r.FormValue("model"))
					assert.JSONEq(t, `{"useTableRecognition":false}`, r.FormValue("optionalPayload"))
					file, _, err := r.FormFile("file")
					require.NoError(t, err)
					file.Close()
				}
				_, err := w.Write([]byte(`{"code":0,"data":{"jobId":"job-one"}}`))
				require.NoError(t, err)
			}))
			defer server.Close()
			in := Input{Model: "PaddleOCR-VL-1.6", Filename: "document.pdf", File: []byte("fixture"), OptionalPayload: []byte(`{"useTableRecognition":false}`)}
			if urlInput {
				in.FileURL = "https://owned.example/resource"
			}
			id, _, err := (Client{server.Client()}).Submit(context.Background(), server.URL+"/api/v2/ocr/jobs", "fixture", in)
			require.NoError(t, err)
			assert.Equal(t, "job-one", id)
		})
	}
}
func TestPaddleCodeAndStatus(t *testing.T) {
	for _, body := range []string{`{"code":7,"msg":"reject","data":{"jobId":"must-not-accept"}}`, `{"data":{"jobId":"missing-code"}}`} {
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(body)) }))
		_, _, err := (Client{s.Client()}).Submit(context.Background(), s.URL, "fixture", Input{OptionalPayload: []byte(`{}`)})
		require.Error(t, err)
		s.Close()
	}
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/jobs/owned", r.URL.Path)
		_, _ = w.Write([]byte(`{"code":0,"data":{"state":"DONE","extractProgress":{"totalPages":2,"extractedPages":2},"resultUrl":{"jsonUrl":"https://result.example/pages"}}}`))
	}))
	defer s.Close()
	status, _, err := (Client{s.Client()}).Status(context.Background(), s.URL+"/jobs", "fixture", "owned")
	require.NoError(t, err)
	assert.Equal(t, "done", status.State)
	assert.Equal(t, 2, status.ExtractProgress.TotalPages)
}
