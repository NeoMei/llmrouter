package onboarding

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/pelletier/go-toml/v2"
)

type codexAdapter struct{ id ClientID }

func (adapter codexAdapter) ID() ClientID { return adapter.id }

func (adapter codexAdapter) Discover(platform Platform, home string) Discovery {
	path := filepath.Join(home, ".codex", "config.toml")
	_, binaryErr := exec.LookPath("codex")
	installed := fileExists(path) || binaryErr == nil
	if adapter.id == ClientCodexDesktop {
		installed = installed || (platform.OS == "darwin" && fileExists("/Applications/Codex.app/Contents/MacOS/Codex"))
	}
	return Discovery{Installed: installed, Paths: []string{path}, Automatic: true}
}

func (adapter codexAdapter) Plan(_ context.Context, request ConfigureRequest) (ConfigPlan, error) {
	if err := validateConfigureRequest(request); err != nil {
		return ConfigPlan{}, err
	}
	if request.Model == "" {
		return ConfigPlan{}, fmt.Errorf("%w: Codex requires a selected model", ErrUsage)
	}
	path := filepath.Join(request.Home, ".codex", "config.toml")
	existing, _, err := readPlannedFile(request.Home, path)
	if err != nil {
		return ConfigPlan{}, err
	}
	withoutManaged, err := RemoveManagedBlock(string(existing), "codex")
	if err != nil {
		return ConfigPlan{}, err
	}
	root := make(map[string]any)
	if withoutManaged != "" {
		if err := toml.Unmarshal([]byte(withoutManaged), &root); err != nil {
			return ConfigPlan{}, fmt.Errorf("%w: invalid Codex TOML", ErrUsage)
		}
	}
	root["model_provider"] = "llmapi"
	root["model"] = request.Model
	providers, exists := root["model_providers"].(map[string]any)
	if !exists {
		if _, present := root["model_providers"]; present {
			return ConfigPlan{}, fmt.Errorf("%w: Codex model_providers is not a table", ErrConflict)
		}
		providers = make(map[string]any)
		root["model_providers"] = providers
	}
	providers["llmapi"] = map[string]any{
		"name": "LLMAPI", "base_url": endpointV1(request.ServerURL),
		"wire_api": "responses", "env_key": "LLMAPI_TOKEN",
	}
	encoded, err := toml.Marshal(root)
	if err != nil {
		return ConfigPlan{}, fmt.Errorf("%w: encode Codex TOML", ErrUsage)
	}
	configured, err := ApplyManagedBlock(string(encoded), "codex", "# Managed keys: model, model_provider, model_providers.llmapi")
	if err != nil {
		return ConfigPlan{}, err
	}
	if err := validateConfigSyntax(path, []byte(configured)); err != nil {
		return ConfigPlan{}, fmt.Errorf("%w: Codex configuration conflicts with the managed provider", ErrConflict)
	}
	instructions := []string{"Set LLMAPI_TOKEN in the environment before launching Codex."}
	if adapter.id == ClientCodexDesktop {
		instructions = []string{"Set LLMAPI_TOKEN in the desktop launch environment, then restart Codex Desktop."}
	}
	return ConfigPlan{Client: adapter.id, Writes: []PlannedWrite{{Path: path, Data: []byte(configured), Mode: privateFileMode}}, Instructions: instructions, RestartRequired: adapter.id == ClientCodexDesktop}, nil
}

func (adapter codexAdapter) Check(_ context.Context, request CheckRequest) []Check {
	name := "codex-cli-config"
	if adapter.id == ClientCodexDesktop {
		name = "codex-desktop-config"
	}
	path := filepath.Join(request.Home, ".codex", "config.toml")
	check := semanticConfigCheck(name, checkCodexConfig(request, path))
	credential := Check{Name: "codex-cli-credential", Status: "FAIL", Message: "LLMAPI_TOKEN is not visible to the Codex CLI process"}
	if adapter.id == ClientCodexDesktop {
		credential = Check{Name: "codex-desktop-credential", Status: "SKIP", Message: "desktop launch credential visibility requires application-level verification"}
	} else if value, ok := os.LookupEnv("LLMAPI_TOKEN"); ok && value != "" && value == request.Token.Reveal() {
		credential = Check{Name: "codex-cli-credential", Status: "PASS", Message: "LLMAPI_TOKEN is visible to the Codex CLI process"}
	}
	return []Check{check, credential}
}

func checkCodexConfig(request CheckRequest, path string) error {
	if request.ServerURL == nil || request.Model == "" {
		return fmt.Errorf("expected Codex settings are incomplete")
	}
	data, err := loadManagedConfig(request.Home, path)
	if err != nil {
		return err
	}
	var root map[string]any
	if err := toml.Unmarshal(data, &root); err != nil {
		return err
	}
	if root["model_provider"] != "llmapi" || root["model"] != request.Model {
		return fmt.Errorf("Codex selection mismatch")
	}
	providers, ok := root["model_providers"].(map[string]any)
	if !ok {
		return fmt.Errorf("Codex providers missing")
	}
	provider, ok := providers["llmapi"].(map[string]any)
	if !ok || provider["name"] != "LLMAPI" || provider["base_url"] != endpointV1(request.ServerURL) || provider["wire_api"] != "responses" || provider["env_key"] != "LLMAPI_TOKEN" {
		return fmt.Errorf("Codex provider mismatch")
	}
	return nil
}

var _ ClientAdapter = codexAdapter{}
