package mcpbridge

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/require"
)

func TestHTTPClientNeverFollowsSameOriginCrossOriginOrDowngradeRedirects(t *testing.T) {
	tests := []struct {
		name string
		tls  bool
	}{
		{name: "same-origin"},
		{name: "cross-origin"},
		{name: "https-downgrade", tls: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			targetHits := 0
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				targetHits++
				require.Empty(t, request.Header.Get("Authorization"))
				_, _ = io.WriteString(w, `{"data":[{"id":"model-a"}]}`)
			}))
			defer target.Close()

			var redirect *httptest.Server
			handler := http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				location := target.URL + "/redirected"
				if tc.name == "same-origin" {
					location = "/redirected"
					if request.URL.Path == "/redirected" {
						targetHits++
						require.Equal(t, "Bearer bridge-secret", request.Header.Get("Authorization"))
						_, _ = io.WriteString(w, `{"data":[{"id":"model-a"}]}`)
						return
					}
				}
				http.Redirect(w, request, location, http.StatusTemporaryRedirect)
			})
			if tc.tls {
				redirect = httptest.NewTLSServer(handler)
			} else {
				redirect = httptest.NewServer(handler)
			}
			defer redirect.Close()

			callerRedirects := 0
			caller := redirect.Client()
			caller.CheckRedirect = func(*http.Request, []*http.Request) error {
				callerRedirects++
				return errors.New("caller redirect hook")
			}
			client := newTestHTTPClient(t, redirect.URL, []string{"model-a"}, caller)
			_, err := client.ListModels(t.Context())
			require.Error(t, err)
			require.Zero(t, targetHits)
			require.Zero(t, callerRedirects)
			require.ErrorContains(t, caller.CheckRedirect(nil, nil), "caller redirect hook")
		})
	}
}

func TestHTTPClientListsOnlyConfiguredUpstreamModels(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		require.Equal(t, http.MethodGet, request.Method)
		require.Equal(t, "/v1/models", request.URL.Path)
		require.Equal(t, "Bearer bridge-secret", request.Header.Get("Authorization"))
		_, _ = io.WriteString(w, `{"data":[{"id":"model-b"},{"id":"model-a"},{"id":"model-c"}]}`)
	}))
	defer server.Close()
	client := newTestHTTPClient(t, server.URL, []string{"model-a", "model-c"}, server.Client())
	models, err := client.ListModels(t.Context())
	require.NoError(t, err)
	require.Equal(t, []string{"model-a", "model-c"}, models)
}

func TestHTTPClientChatIsBoundedNonStreamingAndUsesNoArbitraryFields(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		require.Equal(t, http.MethodPost, request.Method)
		require.Equal(t, "/v1/chat/completions", request.URL.Path)
		require.Equal(t, "Bearer bridge-secret", request.Header.Get("Authorization"))
		var body map[string]any
		require.NoError(t, common.DecodeJson(request.Body, &body))
		require.Equal(t, "model-a", body["model"])
		require.Equal(t, float64(16), body["max_tokens"])
		require.Equal(t, false, body["stream"])
		require.Len(t, body, 4)
		messages := body["messages"].([]any)
		require.Equal(t, "hello", messages[0].(map[string]any)["content"])
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"answer"}}]}`)
	}))
	defer server.Close()
	client := newTestHTTPClient(t, server.URL, []string{"model-a"}, server.Client())
	output, err := client.Chat(t.Context(), ChatRequest{Model: "model-a", Prompt: "hello", MaxOutputTokens: 16})
	require.NoError(t, err)
	require.Equal(t, "answer", output)
}

func TestHTTPClientRejectsUnknownModelsOversizedDataAndRedactsErrors(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		requests++
		if request.URL.Path == "/v1/models" {
			w.WriteHeader(http.StatusBadGateway)
			_, _ = io.WriteString(w, "bridge-secret secret upstream body")
			return
		}
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"`+strings.Repeat("x", MaxOutputBytes+1)+`"}}]}`)
	}))
	defer server.Close()
	client := newTestHTTPClient(t, server.URL, []string{"model-a"}, server.Client())
	_, err := client.Chat(t.Context(), ChatRequest{Model: "model-b", Prompt: "private prompt", MaxOutputTokens: 16})
	require.Error(t, err)
	require.Zero(t, requests)
	require.NotContains(t, err.Error(), "private prompt")

	_, err = client.ListModels(t.Context())
	require.Error(t, err)
	require.NotContains(t, err.Error(), "bridge-secret")
	require.NotContains(t, err.Error(), "upstream body")

	_, err = client.Chat(t.Context(), ChatRequest{Model: "model-a", Prompt: "private prompt", MaxOutputTokens: 16})
	require.Error(t, err)
	require.NotContains(t, err.Error(), "private prompt")
}

func newTestHTTPClient(t *testing.T, origin string, models []string, transport *http.Client) *HTTPClient {
	t.Helper()
	client, err := NewHTTPClient(Config{Origin: origin, Token: "bridge-secret", Models: models, TimeoutSeconds: 30, BridgeSHA256: testBridgeSHA256}, transport)
	require.NoError(t, err)
	return client
}
