package onboarding

import (
	"context"
	"github.com/stretchr/testify/require"
	"net/url"
	"path/filepath"
	"testing"
)

func TestClaudeCodePlanDoesNotPersistToken(t *testing.T) {
	home := t.TempDir()
	server, err := url.Parse("https://gateway.example")
	require.NoError(t, err)
	plan, err := (claudeCodeAdapter{}).Plan(context.Background(), ConfigureRequest{Home: home, ServerURL: server, Token: NewSecret("secret-token"), Model: "model-a"})
	require.NoError(t, err)
	require.Len(t, plan.Writes, 2)
	var settings, profile []byte
	for _, write := range plan.Writes {
		if write.Path == filepath.Join(home, ".claude", "settings.json") {
			settings = write.Data
		} else {
			profile = write.Data
		}
	}
	require.NotContains(t, string(settings), "secret-token")
	require.NotContains(t, string(settings), "ANTHROPIC_AUTH_TOKEN")
	require.Contains(t, string(profile), `ANTHROPIC_AUTH_TOKEN="${LLMROUTER_TOKEN}"`)
}
