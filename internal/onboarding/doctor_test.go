package onboarding

import (
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDoctorSkipsInferenceByDefaultAndRunsSelectedSmokeProtocols(t *testing.T) {
	var mu sync.Mutex
	seen := make(map[string]int)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen[r.Method+" "+r.URL.Path]++
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/healthz":
			_, _ = io.WriteString(w, `ok`)
		case "/v1/models":
			_, _ = io.WriteString(w, `{"data":[{"id":"model-a"}]}`)
		default:
			_, _ = io.WriteString(w, `{}`)
		}
	}))
	defer srv.Close()
	u := mustLoopbackURL(t, srv.URL)
	home := t.TempDir()
	clients := []ClientID{ClientCodexCLI, ClientClaudeCode, ClientOpenCode}
	_, err := ConfigureClients(t.Context(), ConfigureRequest{Home: home, Platform: Platform{OS: runtime.GOOS, Arch: runtime.GOARCH}, ServerURL: u, Token: NewSecret("doctor-secret"), Model: "model-a"}, clients)
	require.NoError(t, err)

	report := RunDoctor(t.Context(), DoctorOptions{Home: home, ServerURL: u, Token: NewSecret("doctor-secret"), Clients: clients, Model: "model-a"})
	require.Zero(t, report.Failed)
	require.Equal(t, 3, report.Skipped)
	mu.Lock()
	require.Zero(t, seen["POST /v1/responses"])
	require.Zero(t, seen["POST /v1/messages"])
	require.Zero(t, seen["POST /v1/chat/completions"])
	mu.Unlock()
	requireNoSensitiveDoctorText(t, report, "doctor-secret", "Reply with OK")

	report = RunDoctor(t.Context(), DoctorOptions{Home: home, ServerURL: u, Token: NewSecret("doctor-secret"), Clients: clients, Model: "model-a", Smoke: true})
	require.Zero(t, report.Failed)
	require.Zero(t, report.Skipped)
	mu.Lock()
	require.Equal(t, 1, seen["POST /v1/responses"])
	require.Equal(t, 1, seen["POST /v1/messages"])
	require.Equal(t, 1, seen["POST /v1/chat/completions"])
	mu.Unlock()
	requireNoSensitiveDoctorText(t, report, "doctor-secret", "Reply with OK")
}

func TestDoctorReportsMissingModelWithoutLeakingResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			w.WriteHeader(http.StatusOK)
			return
		}
		if r.URL.Path == "/v1/models" {
			_, _ = io.WriteString(w, `{"data":[]}`)
			return
		}
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, "doctor-secret")
	}))
	defer srv.Close()
	report := RunDoctor(t.Context(), DoctorOptions{Home: t.TempDir(), ServerURL: mustLoopbackURL(t, srv.URL), Token: NewSecret("doctor-secret"), Clients: []ClientID{ClientCodexCLI}, Model: "missing"})
	require.Greater(t, report.Failed, 0)
	requireNoSensitiveDoctorText(t, report, "doctor-secret")
}
