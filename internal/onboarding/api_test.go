package onboarding

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAPIProbesUseCorrectEndpointsAndBoundedBodies(t *testing.T) {
	seen := make(chan string, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen <- r.Method + " " + r.URL.Path
		require.LessOrEqual(t, r.ContentLength, int64(4096))
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/v1/models" {
			_, _ = io.WriteString(w, `{"data":[{"id":"model-a"}]}`)
			return
		}
		_, _ = io.WriteString(w, `{}`)
	}))
	defer srv.Close()
	client := NewAPIClient(mustLoopbackURL(t, srv.URL), NewSecret("token"), srv.Client())
	require.NoError(t, client.ProbeResponses(context.Background(), "model-a"))
	require.NoError(t, client.ProbeMessages(context.Background(), "model-a"))
	require.Equal(t, "POST /v1/responses", <-seen)
	require.Equal(t, "POST /v1/messages", <-seen)
}

func TestAPIClientListsModelsAndRedactsErrors(t *testing.T) {
	const token = "super-secret"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Contains(t, r.UserAgent(), OnboardingVersion)
		require.Equal(t, "Bearer "+token, r.Header.Get("Authorization"))
		if r.URL.Path == "/v1/models" {
			_, _ = io.WriteString(w, `{"data":[{"id":"model-a"},{"id":"model-b"}]}`)
			return
		}
		w.Header().Set("X-Request-Id", "request-123")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, token+" response payload must stay private")
	}))
	defer srv.Close()
	client := NewAPIClient(mustLoopbackURL(t, srv.URL), NewSecret(token), srv.Client())
	models, err := client.ListModels(context.Background())
	require.NoError(t, err)
	require.Equal(t, []string{"model-a", "model-b"}, models)
	err = client.ProbeChatCompletions(context.Background(), "model-a")
	require.Error(t, err)
	require.Contains(t, err.Error(), "status 401")
	require.Contains(t, err.Error(), "request-123")
	require.NotContains(t, err.Error(), token)
	require.NotContains(t, err.Error(), "response payload")
}

func TestAPIErrorRejectsCredentialShapedRequestID(t *testing.T) {
	const token = "request-secret"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Request-Id", token)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	client := NewAPIClient(mustLoopbackURL(t, srv.URL), NewSecret(token), srv.Client())
	err := client.ProbeResponses(context.Background(), "model-a")
	require.Error(t, err)
	require.NotContains(t, err.Error(), token)
}

func TestAPIErrorRejectsPromptTextRequestID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Request-Id", "Reply with OK.")
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()
	client := NewAPIClient(mustLoopbackURL(t, srv.URL), NewSecret("token"), srv.Client())
	err := client.ProbeResponses(t.Context(), "model-a")
	require.Error(t, err)
	require.NotContains(t, err.Error(), "Reply with OK.")
}

func TestModelCatalogRequiresNonEmptyListAndPreservesCapabilities(t *testing.T) {
	responses := []string{
		`{}`,
		`{"data":{}}`,
		`{"data":[]}`,
	}
	for _, response := range responses {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, response) }))
		client := NewAPIClient(mustLoopbackURL(t, srv.URL), NewSecret("token"), srv.Client())
		_, err := client.ListModelCatalog(t.Context())
		require.Error(t, err)
		srv.Close()
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"data":[{"id":"model-a","supported_endpoint_types":["openai","openai-response","anthropic"]}]}`)
	}))
	defer srv.Close()
	client := NewAPIClient(mustLoopbackURL(t, srv.URL), NewSecret("token"), srv.Client())
	catalog, err := client.ListModelCatalog(t.Context())
	require.NoError(t, err)
	require.Equal(t, []string{"openai", "openai-response", "anthropic"}, catalog[0].SupportedEndpointTypes)
}

func mustLoopbackURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := NormalizeServerURL(raw)
	require.NoError(t, err)
	return u
}

func requireNoSensitiveDoctorText(t *testing.T, report DoctorReport, values ...string) {
	t.Helper()
	for _, check := range report.Checks {
		for _, value := range values {
			require.False(t, strings.Contains(check.Name, value))
			require.False(t, strings.Contains(check.Message, value))
		}
	}
}
