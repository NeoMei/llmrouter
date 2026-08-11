package onboarding

import (
	"context"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAdaptersPreserveUnrelatedConfiguration(t *testing.T) {
	tests := []struct {
		client             ClientID
		fixture, preserved string
	}{
		{ClientCodexCLI, "codex-existing.toml", `[model_providers.native]`},
		{ClientOpenCode, "opencode-existing.json", `"unrelated"`},
		{ClientContinue, "continue-existing.json", `"tabAutocompleteModel"`},
		{ClientAider, "aider-existing.yml", `dark-mode: true`},
	}
	for _, tc := range tests {
		t.Run(string(tc.client), func(t *testing.T) {
			result := configureFixture(t, tc.client, tc.fixture)
			require.Contains(t, result, tc.preserved)
			require.Equal(t, 1, strings.Count(result, "LLMAPI:BEGIN"))
		})
	}
}

func TestCodexCLIAndDesktopShareConfigButHaveSeparateChecks(t *testing.T) {
	home := t.TempDir()
	req := ConfigureRequest{Home: home, Platform: Platform{OS: runtime.GOOS, Arch: runtime.GOARCH}, ServerURL: mustServerURL(t), Token: NewSecret("secret"), Model: "model-a"}
	cli, ok := AdapterFor(ClientCodexCLI)
	require.True(t, ok)
	desktop, ok := AdapterFor(ClientCodexDesktop)
	require.True(t, ok)
	cliPlan, err := cli.Plan(context.Background(), req)
	require.NoError(t, err)
	desktopPlan, err := desktop.Plan(context.Background(), req)
	require.NoError(t, err)
	require.Equal(t, cliPlan.Writes, desktopPlan.Writes)
	checkRequest := CheckRequest{Home: req.Home, Platform: req.Platform, ServerURL: req.ServerURL, Token: req.Token, Model: req.Model}
	require.NotEqual(t, cli.Check(context.Background(), checkRequest)[0].Name, desktop.Check(context.Background(), checkRequest)[0].Name)
}

func TestContinueUnknownFormatFallsBackWithoutMutation(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, ".continue", "config.ts")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, []byte(`export default { models: [] }`), 0o600))
	adapter, ok := AdapterFor(ClientContinue)
	require.True(t, ok)
	plan, err := adapter.Plan(context.Background(), ConfigureRequest{Home: home, Platform: Platform{OS: runtime.GOOS, Arch: runtime.GOARCH}, ServerURL: mustServerURL(t), Token: NewSecret("secret"), Model: "model-a"})
	require.NoError(t, err)
	require.Empty(t, plan.Writes)
	require.NotEmpty(t, plan.Instructions)
	require.NotContains(t, strings.Join(plan.Instructions, "\n"), "secret")
	require.Equal(t, `export default { models: [] }`, string(mustRead(t, path)))
}

func TestContinueUnknownJSONShapeFallsBackWithoutMutation(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, ".continue", "config.json")
	input := []byte(`{"models":{"unexpected":true},"keep":true}`)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, input, 0o600))
	adapter, ok := AdapterFor(ClientContinue)
	require.True(t, ok)
	plan, err := adapter.Plan(context.Background(), ConfigureRequest{Home: home, Platform: Platform{OS: runtime.GOOS, Arch: runtime.GOARCH}, ServerURL: mustServerURL(t), Token: NewSecret("secret"), Model: "model-a"})
	require.NoError(t, err)
	require.Empty(t, plan.Writes)
	require.NotEmpty(t, plan.Instructions)
	require.Equal(t, input, mustRead(t, path))
}

func TestConfigureClientsIsAllOrNothingAndReceiptsStaySecretFree(t *testing.T) {
	home := t.TempDir()
	target := filepath.Join(home, ".config", "opencode", "opencode.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(target), 0o700))
	require.NoError(t, os.WriteFile(target, []byte(`{"keep":true}`), 0o600))
	bad := filepath.Join(home, ".aider.conf.yml")
	require.NoError(t, os.Symlink(filepath.Join(home, "missing"), bad))
	_, err := ConfigureClients(context.Background(), ConfigureRequest{Home: home, Platform: Platform{OS: runtime.GOOS, Arch: runtime.GOARCH}, ServerURL: mustServerURL(t), Token: NewSecret("super-secret"), Model: "model-a"}, []ClientID{ClientOpenCode, ClientAider})
	require.Error(t, err)
	require.Equal(t, `{"keep":true}`, string(mustRead(t, target)))
	require.NoError(t, os.Remove(bad))
	result, err := ConfigureClients(context.Background(), ConfigureRequest{Home: home, Platform: Platform{OS: runtime.GOOS, Arch: runtime.GOARCH}, ServerURL: mustServerURL(t), Token: NewSecret("super-secret"), Model: "model-a"}, []ClientID{ClientOpenCode, ClientAider})
	require.NoError(t, err)
	require.NotContains(t, string(mustRead(t, result.Receipt.Path)), "super-secret")
}

func TestClaudeCodeRejectsNonObjectSettingsWithoutPanicking(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, ".claude", "settings.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, []byte("null"), 0o600))
	adapter, ok := AdapterFor(ClientClaudeCode)
	require.True(t, ok)
	require.NotPanics(t, func() {
		_, err := adapter.Plan(context.Background(), ConfigureRequest{Home: home, Platform: Platform{OS: runtime.GOOS, Arch: runtime.GOARCH}, ServerURL: mustServerURL(t), Token: NewSecret("secret"), Model: "model-a"})
		require.Error(t, err)
	})
}

func TestManualSnippetsNeverContainToken(t *testing.T) {
	for _, id := range []string{"cursor", "cline", "roo-code", "openai-python", "openai-node", "openai-sdk", "langchain"} {
		snippet, err := ManualSnippet(id, mustServerURL(t), "model-a")
		require.NoError(t, err)
		require.Contains(t, snippet, "LLMAPI_TOKEN")
		require.NotContains(t, snippet, "super-secret")
	}
}

func TestAdapterPlanRevalidatesDirectRequests(t *testing.T) {
	adapter, ok := AdapterFor(ClientAider)
	require.True(t, ok)
	unsafeURL, err := url.Parse("https://user:password@stage.example/other")
	require.NoError(t, err)
	_, err = adapter.Plan(context.Background(), ConfigureRequest{Home: t.TempDir(), ServerURL: unsafeURL, Token: NewSecret("secret\nexport PWNED=1"), Model: "model-a"})
	require.Error(t, err)
}

func TestAdapterPlanNormalizesOptionalV1Suffix(t *testing.T) {
	adapter, ok := AdapterFor(ClientCodexCLI)
	require.True(t, ok)
	server, err := url.Parse("https://stage.example/v1")
	require.NoError(t, err)
	plan, err := adapter.Plan(context.Background(), ConfigureRequest{Home: t.TempDir(), ServerURL: server, Token: NewSecret("secret"), Model: "model-a"})
	require.NoError(t, err)
	require.Contains(t, string(plan.Writes[0].Data), `base_url = "https://stage.example/v1"`)
	require.NotContains(t, string(plan.Writes[0].Data), "/v1/v1")
}

func configureFixture(t *testing.T, client ClientID, fixture string) string {
	t.Helper()
	home := t.TempDir()
	data := mustRead(t, filepath.Join("..", "..", "onboarding", "testdata", fixture))
	path := fixturePath(home, client)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, data, 0o600))
	adapter, ok := AdapterFor(client)
	require.True(t, ok)
	plan, err := adapter.Plan(context.Background(), ConfigureRequest{Home: home, Platform: Platform{OS: runtime.GOOS, Arch: runtime.GOARCH}, ServerURL: mustServerURL(t), Token: NewSecret("super-secret"), Model: "model-a"})
	require.NoError(t, err)
	for _, write := range plan.Writes {
		if write.Path == path {
			return string(write.Data)
		}
	}
	t.Fatalf("plan for %s did not include %s", client, path)
	return ""
}

func fixturePath(home string, client ClientID) string {
	switch client {
	case ClientCodexCLI:
		return filepath.Join(home, ".codex", "config.toml")
	case ClientOpenCode:
		return filepath.Join(home, ".config", "opencode", "opencode.json")
	case ClientContinue:
		return filepath.Join(home, ".continue", "config.json")
	case ClientAider:
		return filepath.Join(home, ".aider.conf.yml")
	default:
		return ""
	}
}

func mustServerURL(t *testing.T) *url.URL {
	t.Helper()
	u, err := NormalizeServerURL("https://stage.example/v1")
	require.NoError(t, err)
	return u
}
