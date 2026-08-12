package onboarding

import (
	"context"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
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
			require.Equal(t, 1, strings.Count(result, "LLMROUTER:BEGIN"))
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

func TestContinueRecognizesSchemaRatherThanExtension(t *testing.T) {
	tests := []struct {
		name, file, input, fixture string
	}{
		{name: "arbitrary-json", file: "config.json", fixture: "continue-arbitrary.json"},
		{name: "arbitrary-yaml", file: "config.yaml", input: "unrelated: true\n"},
		{name: "unsupported-yaml-schema", file: "config.yaml", input: "name: Existing\nversion: 1.0.0\nschema: v2\nmodels: []\n"},
		{name: "unsupported-yaml-version", file: "config.yaml", input: "name: Existing\nversion: latest\nschema: v1\nmodels: []\n"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			path := filepath.Join(home, ".continue", tc.file)
			input := []byte(tc.input)
			if tc.fixture != "" {
				input = mustRead(t, filepath.Join("..", "..", "onboarding", "testdata", tc.fixture))
			}
			require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
			require.NoError(t, os.WriteFile(path, input, 0o600))
			adapter, ok := AdapterFor(ClientContinue)
			require.True(t, ok)
			discovery := adapter.Discover(Platform{OS: runtime.GOOS, Arch: runtime.GOARCH}, home)
			require.False(t, discovery.Automatic)
			plan, err := adapter.Plan(t.Context(), ConfigureRequest{Home: home, Platform: Platform{OS: runtime.GOOS, Arch: runtime.GOARCH}, ServerURL: mustServerURL(t), Token: NewSecret("secret"), Model: "model-a"})
			require.NoError(t, err)
			require.Empty(t, plan.Writes)
			require.NotEmpty(t, plan.Instructions)
			require.NotContains(t, strings.Join(plan.Instructions, "\n"), "secret")
			require.Equal(t, input, mustRead(t, path))
		})
	}
}

func TestContinueModernYAMLPreservesSchemaAndUsesCurrentModelShape(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, ".continue", "config.yaml")
	input := mustRead(t, filepath.Join("..", "..", "onboarding", "testdata", "continue-modern.yaml"))
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, input, 0o600))
	adapter, ok := AdapterFor(ClientContinue)
	require.True(t, ok)
	require.True(t, adapter.Discover(Platform{OS: runtime.GOOS, Arch: runtime.GOARCH}, home).Automatic)
	plan, err := adapter.Plan(t.Context(), ConfigureRequest{Home: home, Platform: Platform{OS: runtime.GOOS, Arch: runtime.GOARCH}, ServerURL: mustServerURL(t), Token: NewSecret("secret"), Model: "model-a"})
	require.NoError(t, err)
	require.Len(t, plan.Writes, 1)
	var root map[string]any
	require.NoError(t, yaml.Unmarshal(plan.Writes[0].Data, &root))
	require.Equal(t, "Existing Config", root["name"])
	require.Equal(t, "1.2.3", root["version"])
	require.Equal(t, "v1", root["schema"])
	models := root["models"].([]any)
	require.Len(t, models, 2)
	managed := models[1].(map[string]any)
	require.Equal(t, "LLMROUTER", managed["name"])
	require.NotContains(t, managed, "title")
	require.Equal(t, "model-a", managed["model"])
	output := string(plan.Writes[0].Data)
	require.Contains(t, output, "# keep continue comment and anchors")
	require.Contains(t, output, "&model-defaults")
	require.Contains(t, output, "*model-defaults")
}

func TestContinueRejectsMalformedModelsAndEmptySelection(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, ".continue", "config.yaml")
	input := []byte("name: Existing\nversion: 1.0.0\nschema: v1\nmodels: [42]\n")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, input, 0o600))
	adapter, ok := AdapterFor(ClientContinue)
	require.True(t, ok)
	request := ConfigureRequest{Home: home, Platform: Platform{OS: runtime.GOOS, Arch: runtime.GOARCH}, ServerURL: mustServerURL(t), Token: NewSecret("secret"), Model: "model-a"}
	plan, err := adapter.Plan(t.Context(), request)
	require.NoError(t, err)
	require.Empty(t, plan.Writes)
	require.Equal(t, input, mustRead(t, path))
	request.Model = ""
	plan, err = adapter.Plan(t.Context(), request)
	require.Error(t, err)
	require.Empty(t, plan.Writes)
}

func TestContinueLegacyJSONRejectsScalarModelEntry(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, ".continue", "config.json")
	input := []byte(`{"models":[42]}`)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, input, 0o600))
	adapter, ok := AdapterFor(ClientContinue)
	require.True(t, ok)
	plan, err := adapter.Plan(t.Context(), ConfigureRequest{Home: home, Platform: Platform{OS: runtime.GOOS, Arch: runtime.GOARCH}, ServerURL: mustServerURL(t), Token: NewSecret("secret"), Model: "model-a"})
	require.NoError(t, err)
	require.Empty(t, plan.Writes)
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
		require.Contains(t, snippet, "LLMROUTER_TOKEN")
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
	require.Contains(t, string(plan.Writes[0].Data), "https://stage.example/v1")
	require.NotContains(t, string(plan.Writes[0].Data), "/v1/v1")
}

func TestCodexSelectsProviderAndModelWithoutDuplicateKeys(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, ".codex", "config.toml")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, mustRead(t, filepath.Join("..", "..", "onboarding", "testdata", "codex-existing.toml")), 0o600))
	adapter, ok := AdapterFor(ClientCodexCLI)
	require.True(t, ok)
	request := ConfigureRequest{Home: home, Platform: Platform{OS: runtime.GOOS, Arch: runtime.GOARCH}, ServerURL: mustServerURL(t), Token: NewSecret("secret"), Model: "model-a"}
	plan, err := adapter.Plan(t.Context(), request)
	require.NoError(t, err)
	require.Len(t, plan.Writes, 1)
	var root map[string]any
	require.NoError(t, toml.Unmarshal(plan.Writes[0].Data, &root))
	require.Equal(t, "llmrouter", root["model_provider"])
	require.Equal(t, "model-a", root["model"])
	providers := root["model_providers"].(map[string]any)
	require.Contains(t, providers, "native")
	require.Contains(t, providers, "llmrouter")
	output := string(plan.Writes[0].Data)
	require.Contains(t, output, "# keep codex comment and spacing")
	require.Contains(t, output, `approval_policy    = "on-request" # keep inline comment`)
	require.NoError(t, os.WriteFile(path, plan.Writes[0].Data, 0o600))
	repeated, err := adapter.Plan(t.Context(), request)
	require.NoError(t, err)
	require.Equal(t, plan.Writes[0].Data, repeated.Writes[0].Data)
	require.Equal(t, 1, strings.Count(string(repeated.Writes[0].Data), "LLMROUTER:BEGIN"))
}

func TestCodexLexicalEditPreservesMultilineStringsArrayTablesAndQuotedNames(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, ".codex", "config.toml")
	input := mustRead(t, filepath.Join("..", "..", "onboarding", "testdata", "codex-lexical.toml"))
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, input, 0o600))
	adapter, ok := AdapterFor(ClientCodexCLI)
	require.True(t, ok)
	request := ConfigureRequest{Home: home, Platform: Platform{OS: runtime.GOOS, Arch: runtime.GOARCH}, ServerURL: mustServerURL(t), Token: NewSecret("secret"), Model: "model-a"}
	plan, err := adapter.Plan(t.Context(), request)
	require.NoError(t, err)
	require.Len(t, plan.Writes, 1)
	output := string(plan.Writes[0].Data)
	for _, preserved := range []string{
		"# preserve lexical TOML bytes",
		"model = \"text inside multiline string\"",
		"[model_providers.llmrouter]\n\"\"\"",
		"quoted = \"value # inside quotes\"",
		"[[hooks]] # keep array table",
		"script = \"echo # keep\"",
		"# preserve standalone comment after managed keys",
		"[[hooks]]\nname = \"second-hook\"",
		"[\"quoted#table\"] # keep quoted table with hash",
		"\"quoted#key\" = \"keep#value\"",
	} {
		require.Contains(t, output, preserved)
	}
	require.NotContains(t, output, "Old LLMROUTER")
	var root map[string]any
	require.NoError(t, toml.Unmarshal(plan.Writes[0].Data, &root))
	require.Equal(t, "model-a", root["model"])
	require.Equal(t, "llmrouter", root["model_provider"])
	require.Len(t, root["hooks"], 2)
	require.NoError(t, os.WriteFile(path, plan.Writes[0].Data, 0o600))
	repeated, err := adapter.Plan(t.Context(), request)
	require.NoError(t, err)
	require.Equal(t, plan.Writes[0].Data, repeated.Writes[0].Data)
}

func TestAiderSelectsOpenAIModelAndPreservesYAML(t *testing.T) {
	configured := configureFixture(t, ClientAider, "aider-existing.yml")
	var root map[string]any
	require.NoError(t, yaml.Unmarshal([]byte(configured), &root))
	require.Equal(t, true, root["dark-mode"])
	require.Equal(t, "openai/model-a", root["model"])
	require.NotEmpty(t, root["env-file"])
	require.Contains(t, configured, "# keep aider comment and anchors")
	require.Contains(t, configured, "&shared-settings")
	require.Contains(t, configured, "*shared-settings")
}

func TestAiderNodeEditReplacesCompleteManagedValuesAndPreservesStructure(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, ".aider.conf.yml")
	require.NoError(t, os.WriteFile(path, mustRead(t, filepath.Join("..", "..", "onboarding", "testdata", "aider-node-existing.yml")), 0o600))
	adapter, ok := AdapterFor(ClientAider)
	require.True(t, ok)
	request := ConfigureRequest{Home: home, Platform: Platform{OS: runtime.GOOS, Arch: runtime.GOARCH}, ServerURL: mustServerURL(t), Token: NewSecret("secret"), Model: "model-a"}
	plan, err := adapter.Plan(t.Context(), request)
	require.NoError(t, err)
	require.Len(t, plan.Writes, 2)
	configured := string(plan.Writes[0].Data)
	for _, preserved := range []string{
		"# preserve aider document comment",
		"&defaults",
		"*defaults",
		"# preserve nested inline comment",
		"copied-env: *aider-env",
		"message: | # preserve unrelated multiline scalar",
		"env-file: fake",
		"model: fake",
	} {
		require.Contains(t, configured, preserved)
	}
	require.NotContains(t, configured, "must-not-survive")
	require.NotContains(t, configured, "/old/env")
	var root map[string]any
	require.NoError(t, yaml.Unmarshal([]byte(configured), &root))
	require.Equal(t, "openai/model-a", root["model"])
	require.NotEmpty(t, root["env-file"])
	require.Equal(t, root["env-file"], root["copied-env"])
	require.NoError(t, os.WriteFile(path, plan.Writes[0].Data, 0o600))
	require.NoError(t, os.MkdirAll(filepath.Dir(plan.Writes[1].Path), 0o700))
	require.NoError(t, os.WriteFile(plan.Writes[1].Path, plan.Writes[1].Data, 0o600))
	repeated, err := adapter.Plan(t.Context(), request)
	require.NoError(t, err)
	require.Equal(t, plan.Writes, repeated.Writes)
}

func TestContinueYAMLRejectsDuplicateMappingKeysWithoutWrites(t *testing.T) {
	tests := []struct {
		name, input string
	}{
		{name: "duplicate-schema", input: "name: Existing\nversion: 1.0.0\nschema: v1\nschema: v1\nmodels: []\n"},
		{name: "duplicate-models", input: "name: Existing\nversion: 1.0.0\nschema: v1\nmodels: []\nmodels: []\n"},
		{name: "duplicate-model-field", input: "name: Existing\nversion: 1.0.0\nschema: v1\nmodels:\n  - name: Native\n    provider: openai\n    provider: openai\n    model: native\n"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			path := filepath.Join(home, ".continue", "config.yaml")
			require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
			require.NoError(t, os.WriteFile(path, []byte(tc.input), 0o600))
			adapter, ok := AdapterFor(ClientContinue)
			require.True(t, ok)
			require.False(t, adapter.Discover(Platform{OS: runtime.GOOS, Arch: runtime.GOARCH}, home).Automatic)
			plan, err := adapter.Plan(t.Context(), ConfigureRequest{Home: home, Platform: Platform{OS: runtime.GOOS, Arch: runtime.GOARCH}, ServerURL: mustServerURL(t), Token: NewSecret("secret"), Model: "model-a"})
			require.NoError(t, err)
			require.Empty(t, plan.Writes)
			require.NotEmpty(t, plan.Instructions)
			require.NotContains(t, strings.Join(plan.Instructions, "\n"), "secret")
			require.Equal(t, tc.input, string(mustRead(t, path)))
		})
	}
}

func TestContinueMarkerRemovalPreservesUnrelatedBlankCommentLines(t *testing.T) {
	input := "# paragraph one\n#\n# LLMROUTER:BEGIN continue\n#\n# paragraph two\n#  LLMROUTER:END continue"
	require.Equal(t, "# paragraph one\n#\n#\n# paragraph two\n#  LLMROUTER:END continue", withoutContinueMarkerComment(input))
	model := map[string]any{
		"name": "LLMROUTER", "provider": "openai", "model": "model-a", "apiBase": "https://stage.example/v1", "apiKey": "${LLMROUTER_TOKEN}",
	}
	configured, err := mergeContinueYAML([]byte("# paragraph one\n#\n# paragraph two\nname: Existing\nversion: 1.0.0\nschema: v1\nmodels:\n  # LLMROUTER:BEGIN continue\n  # managed paragraph one\n  #\n  # managed paragraph two\n  - name: LLMROUTER\n    provider: openai\n    model: old\n    apiBase: https://old.example/v1\n    apiKey: ${LLMROUTER_TOKEN}\n  # LLMROUTER:END continue\n  # trailing one\n  #\n  # trailing two\n"), model)
	require.NoError(t, err)
	output := string(configured)
	require.Contains(t, output, "# paragraph one\n#\n# paragraph two")
	require.Contains(t, output, "# managed paragraph one\n    #\n    # managed paragraph two")
	require.Contains(t, output, "# trailing one\n#\n# trailing two")
	require.Equal(t, 1, strings.Count(output, "LLMROUTER:BEGIN continue"))
	require.Equal(t, 1, strings.Count(output, "LLMROUTER:END continue"))
	repeated, err := mergeContinueYAML(configured, model)
	require.NoError(t, err)
	require.Equal(t, configured, repeated)
}

func TestOpenCodeAndContinueChecksRequireVisibleCredential(t *testing.T) {
	t.Setenv("LLMROUTER_TOKEN", "")
	for _, client := range []ClientID{ClientOpenCode, ClientContinue} {
		t.Run(string(client), func(t *testing.T) {
			home := t.TempDir()
			request := ConfigureRequest{Home: home, Platform: Platform{OS: runtime.GOOS, Arch: runtime.GOARCH}, ServerURL: mustServerURL(t), Token: NewSecret("hidden-token"), Model: "model-a"}
			_, err := ConfigureClients(t.Context(), request, []ClientID{client})
			require.NoError(t, err)
			adapter, ok := AdapterFor(client)
			require.True(t, ok)
			checks := adapter.Check(t.Context(), CheckRequest{Home: home, Platform: request.Platform, ServerURL: request.ServerURL, Token: request.Token, Model: request.Model})
			require.Len(t, checks, 2)
			require.Equal(t, "PASS", checks[0].Status)
			require.Equal(t, "FAIL", checks[1].Status)
		})
	}
}

func TestAiderCheckIgnoresUnrelatedValidDotenvSyntax(t *testing.T) {
	home := t.TempDir()
	environment := filepath.Join(home, ".config", "llmrouter", "aider.env")
	unrelated := "export KEEP=plain\nSINGLE='quoted value'\nUNQUOTED=value\n"
	require.NoError(t, os.MkdirAll(filepath.Dir(environment), 0o700))
	require.NoError(t, os.WriteFile(environment, []byte(unrelated), 0o600))
	request := ConfigureRequest{Home: home, Platform: Platform{OS: runtime.GOOS, Arch: runtime.GOARCH}, ServerURL: mustServerURL(t), Token: NewSecret("secret"), Model: "model-a"}
	_, err := ConfigureClients(t.Context(), request, []ClientID{ClientAider})
	require.NoError(t, err)
	adapter, ok := AdapterFor(ClientAider)
	require.True(t, ok)
	checks := adapter.Check(t.Context(), CheckRequest{Home: home, Platform: request.Platform, ServerURL: request.ServerURL, Token: request.Token, Model: request.Model})
	require.Equal(t, "PASS", checks[1].Status)
	require.Contains(t, string(mustRead(t, environment)), unrelated)
}

func TestAdapterChecksRequireExactSemanticConfiguration(t *testing.T) {
	t.Setenv("LLMROUTER_TOKEN", "secret")
	for _, client := range []ClientID{ClientCodexCLI, ClientClaudeCode, ClientOpenCode, ClientContinue, ClientAider} {
		t.Run(string(client), func(t *testing.T) {
			home := t.TempDir()
			request := ConfigureRequest{Home: home, Platform: Platform{OS: runtime.GOOS, Arch: runtime.GOARCH}, ServerURL: mustServerURL(t), Token: NewSecret("secret"), Model: "model-a"}
			_, err := ConfigureClients(t.Context(), request, []ClientID{client})
			require.NoError(t, err)
			adapter, ok := AdapterFor(client)
			require.True(t, ok)
			wrongURL, err := NormalizeServerURL("https://other.example")
			require.NoError(t, err)
			checks := adapter.Check(t.Context(), CheckRequest{Home: home, Platform: request.Platform, ServerURL: wrongURL, Token: request.Token, Model: "other-model"})
			require.Condition(t, func() bool {
				for _, check := range checks {
					if check.Status == "FAIL" {
						return true
					}
				}
				return false
			}, "semantic check passed stale %s configuration", client)
		})
	}
}

func TestAdapterChecksIgnoreUnrelatedTextThatLooksManaged(t *testing.T) {
	t.Setenv("LLMROUTER_TOKEN", "secret")
	tests := []struct {
		client ClientID
		path   func(string) string
		data   string
	}{
		{ClientCodexCLI, func(home string) string { return filepath.Join(home, ".codex", "config.toml") }, "# model_provider = 'llmrouter' model = 'model-a' base_url = 'https://stage.example/v1' env_key = 'LLMROUTER_TOKEN'\n"},
		{ClientClaudeCode, func(home string) string { return filepath.Join(home, ".claude", "settings.json") }, `{"note":"ANTHROPIC_BASE_URL ANTHROPIC_AUTH_TOKEN model-a"}`},
		{ClientOpenCode, func(home string) string { return filepath.Join(home, ".config", "opencode", "opencode.json") }, `{"note":"llmrouter @ai-sdk/openai-compatible {env:LLMROUTER_TOKEN}"}`},
		{ClientContinue, func(home string) string { return filepath.Join(home, ".continue", "config.json") }, `{"models":[{"note":"LLMROUTER:BEGIN continue LLMROUTER_TOKEN model-a"}]}`},
		{ClientAider, func(home string) string { return filepath.Join(home, ".aider.conf.yml") }, "note: 'env-file model openai/model-a OPENAI_API_KEY'\n"},
	}
	for _, tc := range tests {
		t.Run(string(tc.client), func(t *testing.T) {
			home := t.TempDir()
			path := tc.path(home)
			require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
			require.NoError(t, os.WriteFile(path, []byte(tc.data), 0o600))
			adapter, ok := AdapterFor(tc.client)
			require.True(t, ok)
			checks := adapter.Check(t.Context(), CheckRequest{Home: home, Platform: Platform{OS: runtime.GOOS, Arch: runtime.GOARCH}, ServerURL: mustServerURL(t), Token: NewSecret("secret"), Model: "model-a"})
			require.Equal(t, "FAIL", checks[0].Status)
		})
	}
}

func TestCodexDesktopCredentialCheckIsExplicitlyPending(t *testing.T) {
	home := t.TempDir()
	request := ConfigureRequest{Home: home, Platform: Platform{OS: runtime.GOOS, Arch: runtime.GOARCH}, ServerURL: mustServerURL(t), Token: NewSecret("secret"), Model: "model-a"}
	_, err := ConfigureClients(t.Context(), request, []ClientID{ClientCodexDesktop})
	require.NoError(t, err)
	adapter, ok := AdapterFor(ClientCodexDesktop)
	require.True(t, ok)
	checks := adapter.Check(t.Context(), CheckRequest{Home: home, Platform: request.Platform, ServerURL: request.ServerURL, Token: request.Token, Model: request.Model})
	require.Len(t, checks, 2)
	require.Equal(t, "PASS", checks[0].Status)
	require.Equal(t, "SKIP", checks[1].Status)
	require.Equal(t, "codex-desktop-credential", checks[1].Name)
}

func TestPlanningReadRejectsIntermediateParentSymlink(t *testing.T) {
	home := t.TempDir()
	external := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(external, "opencode"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(external, "opencode", "opencode.json"), []byte(`{"provider":{}}`), 0o600))
	require.NoError(t, os.Symlink(external, filepath.Join(home, ".config")))
	adapter, ok := AdapterFor(ClientOpenCode)
	require.True(t, ok)
	plan, err := adapter.Plan(t.Context(), ConfigureRequest{Home: home, Platform: Platform{OS: runtime.GOOS, Arch: runtime.GOARCH}, ServerURL: mustServerURL(t), Token: NewSecret("secret"), Model: "model-a"})
	require.Error(t, err)
	require.Empty(t, plan.Writes)
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
