package main

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestInstallFailsExplicitlyUntilAdaptersAreRegistered(t *testing.T) {
	t.Setenv("LLMAPI_SERVER_URL", "https://stage.example")
	t.Setenv("LLMAPI_TOKEN", "secret")
	t.Setenv("LLMAPI_CLIENTS", "aider")
	require.Equal(t, 1, run([]string{"install"}))
}

func TestInstallUsesPageOriginOnlyWhenServerURLIsAbsent(t *testing.T) {
	t.Setenv("LLMAPI_SERVER_URL", "")
	t.Setenv("LLMAPI_PAGE_ORIGIN", "https://stage.example/v1")
	t.Setenv("LLMAPI_TOKEN", "secret")
	t.Setenv("LLMAPI_CLIENTS", "aider")
	require.Equal(t, 1, run([]string{"install"}))
}
