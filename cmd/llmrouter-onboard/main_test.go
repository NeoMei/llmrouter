package main

import (
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/internal/onboarding"
	"github.com/stretchr/testify/require"
)

func TestClaudeDesktopInstallUsesSameOriginChecksumInsteadOfAmbientDigest(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("official Claude Desktop install test requires macOS")
	}
	executable, err := os.Executable()
	require.NoError(t, err)
	executable, err = filepath.EvalSymlinks(executable)
	require.NoError(t, err)
	bridge := filepath.Join(filepath.Dir(executable), "llmrouter-mcp-bridge")
	input, err := os.Open(executable)
	require.NoError(t, err)
	output, err := os.OpenFile(bridge, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o700)
	require.NoError(t, err)
	_, err = io.Copy(output, input)
	require.NoError(t, err)
	require.NoError(t, input.Close())
	require.NoError(t, output.Close())
	t.Cleanup(func() { _ = os.Remove(bridge) })
	data, err := os.ReadFile(bridge)
	require.NoError(t, err)
	digest := fmt.Sprintf("%x", sha256.Sum256(data))
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		require.Equal(t, "/install/checksums.txt", request.URL.Path)
		_, _ = fmt.Fprintf(writer, "%s  llmrouter-mcp-bridge-darwin-%s\n", digest, runtime.GOARCH)
	}))
	defer server.Close()
	home := t.TempDir()
	t.Setenv("LLMROUTER_SERVER_URL", server.URL)
	t.Setenv("LLMROUTER_TOKEN", "secret")
	t.Setenv("LLMROUTER_CLIENTS", "claude-desktop")
	t.Setenv("LLMROUTER_MODEL", "model-a")
	t.Setenv("LLMROUTER_MCP_BRIDGE_SHA256", strings.Repeat("0", 64))
	require.Equal(t, 0, run([]string{"--home", home, "install"}))
	credential, err := os.ReadFile(filepath.Join(home, ".llmrouter", "credentials", "claude-desktop.json"))
	require.NoError(t, err)
	require.Contains(t, string(credential), digest)
	require.NotContains(t, string(credential), strings.Repeat("0", 64))
}

func TestInstallConfiguresRegisteredAdapter(t *testing.T) {
	home := t.TempDir()
	t.Setenv("LLMROUTER_SERVER_URL", "https://stage.example")
	t.Setenv("LLMROUTER_TOKEN", "secret")
	t.Setenv("LLMROUTER_CLIENTS", "aider")
	t.Setenv("LLMROUTER_MODEL", "model-a")
	require.Equal(t, 0, run([]string{"--home", home, "install"}))
	data, err := os.ReadFile(filepath.Join(home, ".aider.conf.yml"))
	require.NoError(t, err)
	require.Contains(t, string(data), "LLMROUTER:BEGIN")
}

func TestClaudeDesktopBridgePathMatchesSiblingReleaseArtifact(t *testing.T) {
	platform := onboarding.Platform{OS: "darwin", Arch: "arm64"}
	require.Equal(t,
		filepath.Join("/opt/llmrouter", "llmrouter-mcp-bridge-darwin-arm64"),
		claudeDesktopBridgePath(filepath.Join("/opt/llmrouter", "llmrouter-onboard-darwin-arm64"), platform),
	)
	require.Equal(t,
		filepath.Join("/opt/llmrouter", "llmrouter-mcp-bridge"),
		claudeDesktopBridgePath(filepath.Join("/opt/llmrouter", "llmrouter-onboard"), platform),
	)
	windows := onboarding.Platform{OS: "windows", Arch: "amd64"}
	require.Equal(t,
		filepath.Join("C:/llmrouter", "llmrouter-mcp-bridge-windows-amd64.exe"),
		claudeDesktopBridgePath(filepath.Join("C:/llmrouter", "llmrouter-onboard-windows-amd64.exe"), windows),
	)
}

func TestInstallUsesPageOriginOnlyWhenServerURLIsAbsent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("LLMROUTER_SERVER_URL", "")
	t.Setenv("LLMROUTER_PAGE_ORIGIN", "https://stage.example/v1")
	t.Setenv("LLMROUTER_TOKEN", "secret")
	t.Setenv("LLMROUTER_CLIENTS", "codex-cli")
	t.Setenv("LLMROUTER_MODEL", "model-a")
	require.Equal(t, 0, run([]string{"--home", home, "install"}))
	data, err := os.ReadFile(filepath.Join(home, ".codex", "config.toml"))
	require.NoError(t, err)
	require.Contains(t, string(data), "https://stage.example/v1")
}
