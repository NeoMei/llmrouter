package onboarding

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestResolveBridgeChecksumUsesExactSameOriginArtifact(t *testing.T) {
	const expected = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		require.Equal(t, "/install/checksums.txt", request.URL.Path)
		require.Empty(t, request.Header.Get("Authorization"))
		_, _ = io.WriteString(writer,
			"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb  llmrouter-onboard-darwin-arm64\n"+
				expected+"  llmrouter-mcp-bridge-darwin-arm64\n")
	}))
	defer server.Close()
	digest, err := ResolveBridgeChecksum(t.Context(), mustLoopbackURL(t, server.URL), Platform{OS: "darwin", Arch: "arm64"}, server.Client())
	require.NoError(t, err)
	require.Equal(t, expected, digest)
	require.Equal(t, "llmrouter-mcp-bridge-windows-amd64.exe", bridgeArtifactName(Platform{OS: "windows", Arch: "amd64"}))
}

func TestResolveBridgeChecksumRejectsRedirectsWithoutMutatingCaller(t *testing.T) {
	targetHits := 0
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { targetHits++ }))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.Redirect(writer, request, target.URL+"/checksums.txt", http.StatusFound)
	}))
	defer redirect.Close()
	callerRedirects := 0
	caller := redirect.Client()
	caller.CheckRedirect = func(*http.Request, []*http.Request) error {
		callerRedirects++
		return errors.New("caller hook")
	}
	_, err := ResolveBridgeChecksum(t.Context(), mustLoopbackURL(t, redirect.URL), Platform{OS: "darwin", Arch: "arm64"}, caller)
	require.Error(t, err)
	require.Zero(t, targetHits)
	require.Zero(t, callerRedirects)
	require.ErrorContains(t, caller.CheckRedirect(nil, nil), "caller hook")
}

func TestResolveBridgeChecksumRejectsSameOriginAndDowngradeRedirects(t *testing.T) {
	t.Run("same-origin", func(t *testing.T) {
		redirectedHits := 0
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			if request.URL.Path == "/redirected" {
				redirectedHits++
				return
			}
			http.Redirect(writer, request, "/redirected", http.StatusTemporaryRedirect)
		}))
		defer server.Close()
		_, err := ResolveBridgeChecksum(t.Context(), mustLoopbackURL(t, server.URL), Platform{OS: "darwin", Arch: "arm64"}, server.Client())
		require.Error(t, err)
		require.Zero(t, redirectedHits)
	})
	t.Run("https-downgrade", func(t *testing.T) {
		targetHits := 0
		target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { targetHits++ }))
		defer target.Close()
		server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			http.Redirect(writer, request, target.URL+"/checksums.txt", http.StatusTemporaryRedirect)
		}))
		defer server.Close()
		origin, err := NormalizeServerURL(server.URL)
		require.NoError(t, err)
		_, err = ResolveBridgeChecksum(t.Context(), origin, Platform{OS: "darwin", Arch: "arm64"}, server.Client())
		require.Error(t, err)
		require.Zero(t, targetHits)
	})
}

func TestResolveBridgeChecksumRejectsMalformedDuplicateMissingAndOversizedFiles(t *testing.T) {
	target := bridgeArtifactName(Platform{OS: "darwin", Arch: "arm64"})
	tests := []struct {
		name, body string
	}{
		{name: "malformed digest", body: strings.Repeat("z", 64) + "  " + target + "\n"},
		{name: "malformed separator", body: strings.Repeat("a", 64) + " " + target + "\n"},
		{name: "path filename", body: strings.Repeat("a", 64) + "  ../" + target + "\n"},
		{name: "duplicate target", body: strings.Repeat("a", 64) + "  " + target + "\n" + strings.Repeat("b", 64) + "  " + target + "\n"},
		{name: "missing target", body: strings.Repeat("a", 64) + "  llmrouter-onboard-darwin-arm64\n"},
		{name: "oversized", body: strings.Repeat("a", maxChecksumsBytes+1)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(writer, tc.body)
			}))
			defer server.Close()
			_, err := ResolveBridgeChecksum(t.Context(), mustLoopbackURL(t, server.URL), Platform{OS: "darwin", Arch: "arm64"}, server.Client())
			require.Error(t, err)
		})
	}
}
