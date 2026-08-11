package main

import (
	"os"
	"path/filepath"
	"testing"

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
