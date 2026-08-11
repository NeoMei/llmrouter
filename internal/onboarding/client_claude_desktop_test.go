package onboarding

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/require"
)

func TestClaudeDesktopMergePreservesExistingServersAndHidesToken(t *testing.T) {
	home := t.TempDir()
	writeClaudeDesktopFixture(t, home, `{"theme":"dark","mcpServers":{"filesystem":{"command":"fs"}}}`)
	receipt := configureClaudeDesktop(t, home, "https://stage.example", "super-secret")
	config := readClaudeDesktopConfig(t, home)
	require.Contains(t, config, `"filesystem"`)
	require.Contains(t, config, `"llmapi"`)
	require.Contains(t, config, `"theme":"dark"`)
	require.NotContains(t, config, "super-secret")
	require.NotContains(t, string(mustRead(t, receipt.Path)), "super-secret")

	credentialPath := filepath.Join(home, ".llmapi", "credentials", "claude-desktop.json")
	credential := string(mustRead(t, credentialPath))
	require.Contains(t, credential, "super-secret")
	require.Contains(t, credential, `"models":["model-a"]`)
	require.Contains(t, credential, `"timeout_seconds":30`)
	require.Len(t, receipt.Files, 2)
	for _, path := range []string{claudeDesktopConfigPath(Platform{OS: "darwin"}, home), credentialPath} {
		info, err := os.Stat(path)
		require.NoError(t, err)
		require.Zero(t, info.Mode().Perm()&0o077)
	}
}

func TestClaudeDesktopUsesBridgeConfigArgumentWithoutEnvironmentToken(t *testing.T) {
	home := t.TempDir()
	receipt := configureClaudeDesktop(t, home, "https://stage.example", "super-secret")
	var root map[string]any
	require.NoError(t, common.UnmarshalUseNumber(mustRead(t, claudeDesktopConfigPath(Platform{OS: "darwin"}, home)), &root))
	entry := root["mcpServers"].(map[string]any)["llmapi"].(map[string]any)
	require.Equal(t, filepath.Join(home, "bin", "llmapi-mcp-bridge"), entry["command"])
	require.Equal(t, []any{"--config", filepath.Join(home, ".llmapi", "credentials", "claude-desktop.json")}, entry["args"])
	require.NotContains(t, entry, "env")
	require.NotContains(t, string(mustRead(t, receipt.Path)), "super-secret")
}

func TestClaudeDesktopWindowsPathAndLinuxUnsupported(t *testing.T) {
	home := t.TempDir()
	appData := filepath.Join(home, "AppData", "Roaming")
	t.Setenv("APPDATA", appData)
	adapter, ok := AdapterFor(ClientClaudeDesktop)
	require.True(t, ok)
	windowsPath := claudeDesktopConfigPath(Platform{OS: "windows", Arch: "amd64"}, home)
	require.Equal(t, filepath.Join(appData, "Claude", "claude_desktop_config.json"), windowsPath)
	windowsRequest := claudeDesktopRequest(t, home, Platform{OS: "windows", Arch: "amd64"}, "secret")
	_, err := adapter.Plan(t.Context(), windowsRequest)
	require.Error(t, err)
	windowsRequest.BridgePath += ".exe"
	require.NoError(t, os.WriteFile(windowsRequest.BridgePath, []byte("bridge"), 0o700))
	windowsPlan, err := adapter.Plan(t.Context(), windowsRequest)
	require.NoError(t, err)
	require.Len(t, windowsPlan.Writes, 2)
	plan, err := adapter.Plan(t.Context(), claudeDesktopRequest(t, home, Platform{OS: "linux", Arch: "amd64"}, "secret"))
	require.ErrorIs(t, err, ErrUnavailable)
	require.Empty(t, plan.Writes)
	discovery := adapter.Discover(Platform{OS: "linux", Arch: "amd64"}, home)
	require.False(t, discovery.Automatic)
	require.Contains(t, strings.ToLower(discovery.Reason), "unsupported")
}

func TestClaudeDesktopDoctorChecksExactSecretFreeConfiguration(t *testing.T) {
	home := t.TempDir()
	configureClaudeDesktop(t, home, "https://stage.example", "super-secret")
	adapter, ok := AdapterFor(ClientClaudeDesktop)
	require.True(t, ok)
	request := CheckRequest{Home: home, Platform: Platform{OS: "darwin", Arch: "arm64"}, ServerURL: mustServerURL(t), Token: NewSecret("super-secret"), Model: "model-a"}
	checks := adapter.Check(context.Background(), request)
	require.Len(t, checks, 1)
	require.Equal(t, "PASS", checks[0].Status)
	require.NotContains(t, checks[0].Message, "super-secret")

	credentialPath := filepath.Join(home, ".llmapi", "credentials", "claude-desktop.json")
	credential := strings.Replace(string(mustRead(t, credentialPath)), "model-a", "model-b", 1)
	require.NoError(t, os.WriteFile(credentialPath, []byte(credential), 0o600))
	checks = adapter.Check(context.Background(), request)
	require.Equal(t, "FAIL", checks[0].Status)
	require.NotContains(t, checks[0].Message, "super-secret")
}

func TestMCPDoctorProtocolRequiresOpenAIChatCapability(t *testing.T) {
	require.True(t, modelSupportsProtocol(ModelInfo{ID: "chat", SupportedEndpointTypes: []string{"openai"}}, probeMCP))
	require.False(t, modelSupportsProtocol(ModelInfo{ID: "messages", SupportedEndpointTypes: []string{"anthropic"}}, probeMCP))
}

func TestMCPDoctorSmokeUsesBoundedChatCompletions(t *testing.T) {
	chatRequests := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/healthz":
			writer.WriteHeader(http.StatusOK)
		case "/v1/models":
			_, _ = io.WriteString(writer, `{"data":[{"id":"model-a","supported_endpoint_types":["openai"]}]}`)
		case "/v1/chat/completions":
			chatRequests++
			_, _ = io.WriteString(writer, `{}`)
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	report := RunDoctor(t.Context(), DoctorOptions{
		Home: t.TempDir(), ServerURL: mustLoopbackURL(t, server.URL), Token: NewSecret("secret"),
		Clients: []ClientID{ClientClaudeDesktop}, Model: "model-a", Smoke: true,
	})
	requireCheck(t, report, "model-protocol-mcp", "PASS")
	requireCheck(t, report, "inference-mcp", "PASS")
	require.Equal(t, 1, chatRequests)
	requireNoSensitiveDoctorText(t, report, "secret", "Reply with OK")
}

func configureClaudeDesktop(t *testing.T, home, origin, token string) Receipt {
	t.Helper()
	server, err := NormalizeServerURL(origin)
	require.NoError(t, err)
	request := claudeDesktopRequest(t, home, Platform{OS: "darwin", Arch: "arm64"}, token)
	request.ServerURL = server
	result, err := ConfigureClients(t.Context(), request, []ClientID{ClientClaudeDesktop})
	require.NoError(t, err)
	require.True(t, result.RestartRequired)
	return result.Receipt
}

func claudeDesktopRequest(t *testing.T, home string, platform Platform, token string) ConfigureRequest {
	t.Helper()
	bridge := filepath.Join(home, "bin", "llmapi-mcp-bridge")
	require.NoError(t, os.MkdirAll(filepath.Dir(bridge), 0o700))
	require.NoError(t, os.WriteFile(bridge, []byte("bridge"), 0o700))
	return ConfigureRequest{
		Home: home, Platform: platform, ServerURL: mustServerURL(t), Token: NewSecret(token), Model: "model-a", BridgePath: bridge,
	}
}

func writeClaudeDesktopFixture(t *testing.T, home, fixture string) {
	t.Helper()
	path := claudeDesktopConfigPath(Platform{OS: "darwin", Arch: "arm64"}, home)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, []byte(fixture), 0o600))
}

func readClaudeDesktopConfig(t *testing.T, home string) string {
	t.Helper()
	return string(mustRead(t, claudeDesktopConfigPath(Platform{OS: "darwin", Arch: "arm64"}, home)))
}
