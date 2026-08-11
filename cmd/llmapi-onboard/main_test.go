package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/QuantumNous/new-api/internal/onboarding"
	"github.com/stretchr/testify/require"
)

func TestInstallConfiguresRegisteredAdapter(t *testing.T) {
	home := t.TempDir()
	t.Setenv("LLMAPI_SERVER_URL", "https://stage.example")
	t.Setenv("LLMAPI_TOKEN", "secret")
	t.Setenv("LLMAPI_CLIENTS", "aider")
	t.Setenv("LLMAPI_MODEL", "model-a")
	require.Equal(t, 0, run([]string{"--home", home, "install"}))
	data, err := os.ReadFile(filepath.Join(home, ".aider.conf.yml"))
	require.NoError(t, err)
	require.Contains(t, string(data), "LLMAPI:BEGIN")
}

func TestClaudeDesktopBridgePathMatchesSiblingReleaseArtifact(t *testing.T) {
	platform := onboarding.Platform{OS: "darwin", Arch: "arm64"}
	require.Equal(t,
		filepath.Join("/opt/llmapi", "llmapi-mcp-bridge-darwin-arm64"),
		claudeDesktopBridgePath(filepath.Join("/opt/llmapi", "llmapi-onboard-darwin-arm64"), platform),
	)
	require.Equal(t,
		filepath.Join("/opt/llmapi", "llmapi-mcp-bridge"),
		claudeDesktopBridgePath(filepath.Join("/opt/llmapi", "llmapi-onboard"), platform),
	)
	windows := onboarding.Platform{OS: "windows", Arch: "amd64"}
	require.Equal(t,
		filepath.Join("C:/llmapi", "llmapi-mcp-bridge-windows-amd64.exe"),
		claudeDesktopBridgePath(filepath.Join("C:/llmapi", "llmapi-onboard-windows-amd64.exe"), windows),
	)
}

func TestInstallUsesPageOriginOnlyWhenServerURLIsAbsent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("LLMAPI_SERVER_URL", "")
	t.Setenv("LLMAPI_PAGE_ORIGIN", "https://stage.example/v1")
	t.Setenv("LLMAPI_TOKEN", "secret")
	t.Setenv("LLMAPI_CLIENTS", "codex-cli")
	t.Setenv("LLMAPI_MODEL", "model-a")
	require.Equal(t, 0, run([]string{"--home", home, "install"}))
	data, err := os.ReadFile(filepath.Join(home, ".codex", "config.toml"))
	require.NoError(t, err)
	require.Contains(t, string(data), "https://stage.example/v1")
}
