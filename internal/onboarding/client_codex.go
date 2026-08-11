package onboarding

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
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
	path := filepath.Join(request.Home, ".codex", "config.toml")
	existing, _, err := readPlannedFile(path)
	if err != nil {
		return ConfigPlan{}, err
	}
	if len(existing) != 0 {
		if err := validateConfigSyntax(path, existing); err != nil {
			return ConfigPlan{}, fmt.Errorf("%w: invalid Codex TOML", ErrUsage)
		}
	}
	content := strings.Join([]string{
		"[model_providers.llmapi]",
		`name = "LLMAPI"`,
		"base_url = " + strconv.Quote(endpointV1(request.ServerURL)),
		`wire_api = "responses"`,
		`env_key = "LLMAPI_TOKEN"`,
	}, "\n")
	configured, err := ApplyManagedBlock(string(existing), "codex", content)
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
	return []Check{checkManagedConfig(name, path, []string{"[model_providers.llmapi]", `wire_api = "responses"`, `env_key = "LLMAPI_TOKEN"`})}
}

var _ ClientAdapter = codexAdapter{}
