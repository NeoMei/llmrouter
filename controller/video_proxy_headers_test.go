package controller

import (
	"bytes"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/constant"
	"github.com/gin-gonic/gin"
)

func TestCopyVideoResponseHeadersAllowsOnlyMediaMetadata(t *testing.T) {
	upstream := http.Header{
		"Content-Type":                {"video/mp4"},
		"Content-Disposition":         {"inline; filename=clip.mp4"},
		"Etag":                        {`"video-v1"`},
		"Last-Modified":               {"Sun, 10 Aug 2026 00:00:00 GMT"},
		"Accept-Ranges":               {"bytes"},
		"Content-Range":               {"bytes 0-9/10"},
		"Set-Cookie":                  {"new_api_refresh=attacker; Path=/; HttpOnly"},
		"Connection":                  {"keep-alive"},
		"Transfer-Encoding":           {"chunked"},
		"Content-Length":              {"10"},
		"Location":                    {"https://attacker.example/"},
		"Access-Control-Allow-Origin": {"*"},
	}
	recorder := httptest.NewRecorder()

	copyVideoResponseHeaders(recorder.Header(), upstream)

	for key, want := range map[string]string{
		"Content-Type":        "video/mp4",
		"Content-Disposition": "inline; filename=clip.mp4",
		"Etag":                `"video-v1"`,
		"Last-Modified":       "Sun, 10 Aug 2026 00:00:00 GMT",
		"Accept-Ranges":       "bytes",
		"Content-Range":       "bytes 0-9/10",
	} {
		if got := recorder.Header().Get(key); got != want {
			t.Fatalf("%s = %q, want %q", key, got, want)
		}
	}
	for _, key := range []string{
		"Set-Cookie", "Connection", "Transfer-Encoding", "Content-Length",
		"Location", "Access-Control-Allow-Origin",
	} {
		if got := recorder.Header().Values(key); len(got) != 0 {
			t.Fatalf("unsafe upstream header %s was forwarded: %q", key, got)
		}
	}
}

func TestCopyVideoResponseHeadersPreventsActiveContentSniffing(t *testing.T) {
	upstream := http.Header{"Content-Type": {"text/html; charset=utf-8"}}
	recorder := httptest.NewRecorder()

	copyVideoResponseHeaders(recorder.Header(), upstream)

	if got := recorder.Header().Get("Content-Type"); got != "application/octet-stream" {
		t.Fatalf("Content-Type = %q, want application/octet-stream", got)
	}
	if got := recorder.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("X-Content-Type-Options = %q, want nosniff", got)
	}
}

func TestWriteVideoDataURLRejectsActiveContent(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	dataURL := "data:text/html;base64," + base64.StdEncoding.EncodeToString([]byte("<script>alert(1)</script>"))

	err := writeVideoDataURL(context, dataURL)

	if err == nil {
		t.Fatal("expected active data URL content to be rejected")
	}
	if recorder.Body.Len() != 0 {
		t.Fatalf("response body was written before rejection: %q", recorder.Body.String())
	}
}

func TestCopyVideoContentStopsAtConfiguredByteLimit(t *testing.T) {
	previousLimit := constant.MaxFileDownloadMB
	constant.MaxFileDownloadMB = 1
	t.Cleanup(func() { constant.MaxFileDownloadMB = previousLimit })
	recorder := httptest.NewRecorder()
	payload := bytes.Repeat([]byte{'v'}, (1<<20)+1)

	err := copyVideoContent(recorder, bytes.NewReader(payload))

	if err == nil {
		t.Fatal("expected oversized video stream to be rejected")
	}
	if recorder.Body.Len() != 1<<20 {
		t.Fatalf("copied %d bytes, want hard ceiling %d", recorder.Body.Len(), 1<<20)
	}
}

func TestWriteVideoDataURLRejectsOversizedPayloadBeforeDecode(t *testing.T) {
	previousLimit := constant.MaxFileDownloadMB
	constant.MaxFileDownloadMB = 1
	t.Cleanup(func() { constant.MaxFileDownloadMB = previousLimit })
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	payload := bytes.Repeat([]byte{'v'}, (1<<20)+1)
	dataURL := "data:video/mp4;base64," + base64.StdEncoding.EncodeToString(payload)

	err := writeVideoDataURL(context, dataURL)

	if err == nil {
		t.Fatal("expected oversized data URL to be rejected")
	}
	if recorder.Body.Len() != 0 {
		t.Fatalf("response body was written before rejection: %d bytes", recorder.Body.Len())
	}
}
