package onboarding

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/internal/onboarding/mcpbridge"
)

const claudeDesktopBridgeTimeoutSeconds = 30

type claudeDesktopAdapter struct{}

func (claudeDesktopAdapter) ID() ClientID { return ClientClaudeDesktop }

func (claudeDesktopAdapter) Discover(platform Platform, home string) Discovery {
	if platform.OS != "darwin" && platform.OS != "windows" {
		return Discovery{Automatic: false, Reason: "official Claude Desktop is unsupported on Linux"}
	}
	paths := []string{claudeDesktopConfigPath(platform, home), claudeDesktopCredentialPath(home)}
	return Discovery{Installed: fileExists(paths[0]), Paths: paths, Automatic: true}
}

func (claudeDesktopAdapter) Plan(_ context.Context, request ConfigureRequest) (ConfigPlan, error) {
	if request.Platform.OS != "darwin" && request.Platform.OS != "windows" {
		return ConfigPlan{}, fmt.Errorf("%w: official Claude Desktop is supported only on macOS and Windows", ErrUnavailable)
	}
	if err := validateConfigureRequest(request); err != nil {
		return ConfigPlan{}, err
	}
	if request.Model == "" {
		return ConfigPlan{}, fmt.Errorf("%w: Claude Desktop requires a selected model allow-list", ErrUsage)
	}
	bridgePath, err := verifyClaudeDesktopBridge(request.Home, request.BridgePath, request.BridgeSHA256, request.Platform)
	if err != nil {
		return ConfigPlan{}, err
	}
	configPath := claudeDesktopConfigPath(request.Platform, request.Home)
	credentialPath := claudeDesktopCredentialPath(request.Home)
	existing, _, err := readPlannedFile(request.Home, configPath)
	if err != nil {
		return ConfigPlan{}, err
	}
	entry := map[string]any{"command": bridgePath, "args": []string{"--config", credentialPath}}
	configured, err := MergeJSONObject(existing, []string{"mcpServers", "llmapi"}, entry)
	if err != nil {
		return ConfigPlan{}, err
	}
	credential := mcpbridge.Config{
		Origin: serverOrigin(request.ServerURL), Token: request.Token.Reveal(), Models: []string{request.Model}, TimeoutSeconds: claudeDesktopBridgeTimeoutSeconds,
		BridgeSHA256: strings.ToLower(request.BridgeSHA256),
	}
	if err := credential.Validate(); err != nil {
		return ConfigPlan{}, fmt.Errorf("%w: invalid Claude Desktop bridge credential", ErrUsage)
	}
	credentialData, err := common.Marshal(credential)
	if err != nil {
		return ConfigPlan{}, fmt.Errorf("%w: encode Claude Desktop bridge credential", ErrUnsafe)
	}
	return ConfigPlan{
		Client: ClientClaudeDesktop,
		Writes: []PlannedWrite{
			{Path: configPath, Data: configured, Mode: privateFileMode},
			{Path: credentialPath, Data: credentialData, Mode: privateFileMode},
		},
		Instructions:    []string{"Claude Desktop gains bounded MCP tool access only; this does not replace Claude Desktop's built-in Claude model. Restart Claude Desktop."},
		RestartRequired: true,
	}, nil
}

func (claudeDesktopAdapter) Check(_ context.Context, request CheckRequest) []Check {
	if request.Platform.OS != "darwin" && request.Platform.OS != "windows" {
		return []Check{{Name: "claude-desktop-config", Status: "SKIP", Message: "official Claude Desktop is unsupported on Linux"}}
	}
	err := checkClaudeDesktopConfig(request)
	return []Check{semanticConfigCheck("claude-desktop-config", err)}
}

func checkClaudeDesktopConfig(request CheckRequest) error {
	_, _, _, err := loadClaudeDesktopBridge(request)
	return err
}

func loadClaudeDesktopBridge(request CheckRequest) (string, string, mcpbridge.Config, error) {
	if request.ServerURL == nil || request.Token.Reveal() == "" || request.Model == "" {
		return "", "", mcpbridge.Config{}, fmt.Errorf("expected Claude Desktop settings are incomplete")
	}
	configPath := claudeDesktopConfigPath(request.Platform, request.Home)
	credentialPath := claudeDesktopCredentialPath(request.Home)
	data, err := loadManagedConfig(request.Home, configPath)
	if err != nil {
		return "", "", mcpbridge.Config{}, err
	}
	var root map[string]any
	if err := common.UnmarshalUnique(data, &root); err != nil || root == nil {
		return "", "", mcpbridge.Config{}, fmt.Errorf("invalid Claude Desktop configuration")
	}
	servers, ok := root["mcpServers"].(map[string]any)
	if !ok {
		return "", "", mcpbridge.Config{}, fmt.Errorf("Claude Desktop MCP servers are unavailable")
	}
	entry, ok := servers["llmapi"].(map[string]any)
	if !ok || len(entry) != 2 {
		return "", "", mcpbridge.Config{}, fmt.Errorf("Claude Desktop LLMAPI server is invalid")
	}
	command, ok := entry["command"].(string)
	if !ok {
		return "", "", mcpbridge.Config{}, fmt.Errorf("Claude Desktop bridge command is invalid")
	}
	args, ok := entry["args"].([]any)
	if !ok || len(args) != 2 || args[0] != "--config" || args[1] != credentialPath {
		return "", "", mcpbridge.Config{}, fmt.Errorf("Claude Desktop bridge arguments are invalid")
	}
	credential, err := mcpbridge.LoadConfig(credentialPath)
	if err != nil {
		return "", "", mcpbridge.Config{}, err
	}
	if _, err := verifyClaudeDesktopBridge(request.Home, command, credential.BridgeSHA256, request.Platform); err != nil {
		return "", "", mcpbridge.Config{}, err
	}
	if credential.Origin != serverOrigin(request.ServerURL) || credential.Token != request.Token.Reveal() || credential.TimeoutSeconds != claudeDesktopBridgeTimeoutSeconds || len(credential.Models) != 1 || credential.Models[0] != request.Model {
		return "", "", mcpbridge.Config{}, fmt.Errorf("Claude Desktop bridge credential mismatch")
	}
	return command, credentialPath, credential, nil
}

func claudeDesktopConfigPath(platform Platform, home string) string {
	if platform.OS == "windows" {
		appData := os.Getenv("APPDATA")
		if appData == "" {
			appData = filepath.Join(home, "AppData", "Roaming")
		}
		return filepath.Join(appData, "Claude", "claude_desktop_config.json")
	}
	return filepath.Join(home, "Library", "Application Support", "Claude", "claude_desktop_config.json")
}

func claudeDesktopCredentialPath(home string) string {
	return filepath.Join(home, ".llmapi", "credentials", "claude-desktop.json")
}

func verifyClaudeDesktopBridge(home, path, expectedSHA256 string, platform Platform) (string, error) {
	if path == "" || hasControl(path) || !filepath.IsAbs(path) || !validSHA256(expectedSHA256) {
		return "", fmt.Errorf("%w: installed Claude Desktop bridge path is required", ErrUnavailable)
	}
	path = filepath.Clean(path)
	root := filepath.VolumeName(path) + string(filepath.Separator)
	if err := rejectSymlinksBelow(root, path); err != nil {
		return "", fmt.Errorf("%w: installed Claude Desktop bridge path is unsafe", ErrUnsafe)
	}
	name := strings.ToLower(filepath.Base(path))
	installedName := "llmapi-mcp-bridge"
	artifactName := installedName + "-" + platform.OS + "-" + platform.Arch
	if platform.OS == "windows" {
		installedName += ".exe"
		artifactName += ".exe"
	}
	validName := name == installedName || name == artifactName
	info, err := os.Lstat(path)
	if !validName || err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o022 != 0 || validateSecureOwner(info) != nil {
		return "", fmt.Errorf("%w: installed Claude Desktop bridge is unavailable", ErrUnavailable)
	}
	if platform.OS != "windows" && info.Mode().Perm()&0o111 == 0 {
		return "", fmt.Errorf("%w: installed Claude Desktop bridge is not executable", ErrUnavailable)
	}
	anchor := root
	if absoluteHome, homeErr := filepath.Abs(home); homeErr == nil {
		if rel, relErr := filepath.Rel(filepath.Clean(absoluteHome), path); relErr == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			anchor = filepath.Clean(absoluteHome)
		}
	}
	for parent := filepath.Dir(path); ; parent = filepath.Dir(parent) {
		parentInfo, parentErr := os.Lstat(parent)
		if parentErr != nil || !parentInfo.IsDir() || parentInfo.Mode()&os.ModeSymlink != 0 || parentInfo.Mode().Perm()&0o022 != 0 {
			return "", fmt.Errorf("%w: installed Claude Desktop bridge parent is unsafe", ErrUnsafe)
		}
		if parent == anchor {
			break
		}
		if parent == filepath.Dir(parent) {
			return "", fmt.Errorf("%w: installed Claude Desktop bridge is outside the trusted path", ErrUnsafe)
		}
	}
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("%w: installed Claude Desktop bridge is unreadable", ErrUnsafe)
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return "", fmt.Errorf("%w: installed Claude Desktop bridge changed during verification", ErrUnsafe)
	}
	digest := sha256.New()
	written, err := io.Copy(digest, io.LimitReader(file, 128<<20+1))
	if err != nil || written > 128<<20 || !strings.EqualFold(hex.EncodeToString(digest.Sum(nil)), expectedSHA256) {
		return "", fmt.Errorf("%w: installed Claude Desktop bridge checksum mismatch", ErrUnavailable)
	}
	return path, nil
}

func validSHA256(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(value) == 64 && len(decoded) == sha256.Size
}

var _ ClientAdapter = claudeDesktopAdapter{}
