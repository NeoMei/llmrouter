package onboarding

import (
	"bufio"
	"context"
	"crypto/sha256"
	"fmt"
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

func TestMain(m *testing.M) {
	if len(os.Args) == 3 && os.Args[1] == "--config" {
		os.Exit(runFakeMCPBridge(os.Args[2]))
	}
	os.Exit(m.Run())
}

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
	require.Contains(t, credential, `"bridge_sha256":"`)
	require.Equal(t, checksumFile(t, filepath.Join(canonicalTestHome(t, home), "bin", "llmapi-mcp-bridge")), receipt.Meta.BridgeSHA256)
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
	canonicalHome, err := filepath.EvalSymlinks(home)
	require.NoError(t, err)
	require.Equal(t, filepath.Join(canonicalHome, "bin", "llmapi-mcp-bridge"), entry["command"])
	require.Equal(t, []any{"--config", filepath.Join(home, ".llmapi", "credentials", "claude-desktop.json")}, entry["args"])
	require.NotContains(t, entry, "env")
	require.NotContains(t, string(mustRead(t, receipt.Path)), "super-secret")
}

func TestClaudeDesktopBridgeRequiresMatchingChecksumAndTrustedPath(t *testing.T) {
	home := t.TempDir()
	adapter, ok := AdapterFor(ClientClaudeDesktop)
	require.True(t, ok)
	request := claudeDesktopRequest(t, home, Platform{OS: "darwin", Arch: "arm64"}, "secret")
	request.BridgeSHA256 = ""
	_, err := adapter.Plan(t.Context(), request)
	require.Error(t, err)
	request.BridgeSHA256 = strings.Repeat("0", 64)
	_, err = adapter.Plan(t.Context(), request)
	require.Error(t, err)
	request.BridgeSHA256 = checksumFile(t, request.BridgePath)
	require.NoError(t, os.Chmod(filepath.Dir(request.BridgePath), 0o770))
	_, err = adapter.Plan(t.Context(), request)
	require.Error(t, err)
}

func TestClaudeDesktopBridgeRejectsWritableFileAndSymlinkComponents(t *testing.T) {
	home := t.TempDir()
	adapter, ok := AdapterFor(ClientClaudeDesktop)
	require.True(t, ok)
	request := claudeDesktopRequest(t, home, Platform{OS: "darwin", Arch: "arm64"}, "secret")
	require.NoError(t, os.Chmod(request.BridgePath, 0o722))
	_, err := adapter.Plan(t.Context(), request)
	require.Error(t, err)
	require.NoError(t, os.Chmod(request.BridgePath, 0o700))

	bin := filepath.Dir(request.BridgePath)
	realBin := bin + "-real"
	require.NoError(t, os.Rename(bin, realBin))
	require.NoError(t, os.Symlink(realBin, bin))
	request.BridgePath = filepath.Join(bin, "llmapi-mcp-bridge")
	_, err = adapter.Plan(t.Context(), request)
	require.Error(t, err)
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
	windowsRequest.BridgeSHA256 = checksumFile(t, windowsRequest.BridgePath)
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

func TestMCPDoctorSkipsOfficialDesktopOnLinux(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/v1/models" {
			_, _ = io.WriteString(writer, `{"data":[{"id":"model-a"}]}`)
			return
		}
		writer.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	report := RunDoctor(t.Context(), DoctorOptions{
		Home: t.TempDir(), Platform: Platform{OS: "linux", Arch: "amd64"}, ServerURL: mustLoopbackURL(t, server.URL),
		Token: NewSecret("secret"), Clients: []ClientID{ClientClaudeDesktop}, Model: "model-a", Smoke: true,
	})
	requireCheck(t, report, "mcp-lifecycle", "SKIP")
	requireCheck(t, report, "inference-mcp", "SKIP")
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
	desktopPath := claudeDesktopConfigPath(request.Platform, home)
	desktop := string(mustRead(t, desktopPath))
	require.NoError(t, os.WriteFile(desktopPath, []byte(`{"mcpServers":{},`+desktop[1:]), 0o600))
	checks = adapter.Check(context.Background(), request)
	require.Equal(t, "FAIL", checks[0].Status)
	require.NoError(t, os.WriteFile(desktopPath, []byte(desktop), 0o600))

	credentialPath := filepath.Join(home, ".llmapi", "credentials", "claude-desktop.json")
	credential := strings.Replace(string(mustRead(t, credentialPath)), "model-a", "model-b", 1)
	require.NoError(t, os.WriteFile(credentialPath, []byte(credential), 0o600))
	checks = adapter.Check(context.Background(), request)
	require.Equal(t, "FAIL", checks[0].Status)
	require.NotContains(t, checks[0].Message, "super-secret")
}

func TestMCPDoctorLaunchesConfiguredBridgeForLifecycleAndOptionalSmoke(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/healthz":
			writer.WriteHeader(http.StatusOK)
		case "/v1/models":
			_, _ = io.WriteString(writer, `{"data":[{"id":"model-a","supported_endpoint_types":["openai"]}]}`)
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	home := t.TempDir()
	request := claudeDesktopRequest(t, home, Platform{OS: "darwin", Arch: "arm64"}, "secret")
	request.ServerURL = mustLoopbackURL(t, server.URL)
	_, err := ConfigureClients(t.Context(), request, []ClientID{ClientClaudeDesktop})
	require.NoError(t, err)
	report := RunDoctor(t.Context(), DoctorOptions{
		Home: home, Platform: Platform{OS: "darwin", Arch: "arm64"}, ServerURL: mustLoopbackURL(t, server.URL),
		Token: NewSecret("secret"), Clients: []ClientID{ClientClaudeDesktop}, Model: "model-a",
	})
	requireCheck(t, report, "mcp-lifecycle", "PASS")
	requireCheck(t, report, "inference-mcp", "SKIP")
	for _, check := range report.Checks {
		require.NotEqual(t, "model-protocol-mcp", check.Name)
	}
	report = RunDoctor(t.Context(), DoctorOptions{
		Home: home, Platform: Platform{OS: "darwin", Arch: "arm64"}, ServerURL: mustLoopbackURL(t, server.URL),
		Token: NewSecret("secret"), Clients: []ClientID{ClientClaudeDesktop}, Model: "model-a", Smoke: true,
	})
	requireCheck(t, report, "mcp-lifecycle", "PASS")
	requireCheck(t, report, "inference-mcp", "PASS")
	requireNoSensitiveDoctorText(t, report, "secret", "Reply with OK")
}

func TestMCPDoctorRedactsBridgeStderrAndCredential(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/v1/models" {
			_, _ = io.WriteString(writer, `{"data":[{"id":"model-a"}]}`)
			return
		}
		writer.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	home := t.TempDir()
	request := claudeDesktopRequest(t, home, Platform{OS: "darwin", Arch: "arm64"}, "stderr-secret")
	request.ServerURL = mustLoopbackURL(t, server.URL)
	_, err := ConfigureClients(t.Context(), request, []ClientID{ClientClaudeDesktop})
	require.NoError(t, err)
	report := RunDoctor(t.Context(), DoctorOptions{
		Home: home, Platform: request.Platform, ServerURL: request.ServerURL, Token: request.Token,
		Clients: []ClientID{ClientClaudeDesktop}, Model: "model-a",
	})
	requireCheck(t, report, "mcp-lifecycle", "FAIL")
	requireNoSensitiveDoctorText(t, report, "stderr-secret")
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
	canonicalHome := canonicalTestHome(t, home)
	bridge := filepath.Join(canonicalHome, "bin", "llmapi-mcp-bridge")
	require.NoError(t, os.MkdirAll(filepath.Dir(bridge), 0o700))
	executable, err := os.Executable()
	require.NoError(t, err)
	copyFile(t, executable, bridge)
	return ConfigureRequest{
		Home: home, Platform: platform, ServerURL: mustServerURL(t), Token: NewSecret(token), Model: "model-a", BridgePath: bridge,
		BridgeSHA256: checksumFile(t, bridge),
	}
}

func canonicalTestHome(t *testing.T, home string) string {
	t.Helper()
	canonicalHome, err := filepath.EvalSymlinks(home)
	require.NoError(t, err)
	return canonicalHome
}

func checksumFile(t *testing.T, path string) string {
	t.Helper()
	return fmt.Sprintf("%x", sha256.Sum256(mustRead(t, path)))
}

func copyFile(t *testing.T, source, destination string) {
	t.Helper()
	input, err := os.Open(source)
	require.NoError(t, err)
	defer input.Close()
	output, err := os.OpenFile(destination, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o700)
	require.NoError(t, err)
	_, err = io.Copy(output, input)
	require.NoError(t, err)
	require.NoError(t, output.Close())
}

func runFakeMCPBridge(configPath string) int {
	config, err := os.ReadFile(configPath)
	if err != nil {
		return 1
	}
	if strings.Contains(string(config), "stderr-secret") {
		fmt.Fprintln(os.Stderr, "stderr-secret from fake bridge")
		return 1
	}
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		var request map[string]any
		if common.UnmarshalUseNumber(scanner.Bytes(), &request) != nil {
			return 1
		}
		method, _ := request["method"].(string)
		id, hasID := request["id"]
		if !hasID {
			continue
		}
		var result any
		switch method {
		case "initialize":
			result = map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]any{"name": "fake", "version": "1"}}
		case "tools/list":
			result = map[string]any{"tools": []map[string]any{{"name": "llmapi_list_models"}, {"name": "llmapi_chat"}}}
		case "tools/call":
			result = map[string]any{"content": []map[string]string{{"type": "text", "text": "ok"}}}
		default:
			return 1
		}
		response, _ := common.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
		_, _ = os.Stdout.Write(append(response, '\n'))
	}
	if scanner.Err() != nil {
		return 1
	}
	return 0
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
