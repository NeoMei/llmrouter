package onboarding

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/QuantumNous/new-api/common"
)

type claudeCodeAdapter struct{}

func (claudeCodeAdapter) ID() ClientID { return ClientClaudeCode }

func (claudeCodeAdapter) Discover(_ Platform, home string) Discovery {
	path := filepath.Join(home, ".claude", "settings.json")
	_, err := exec.LookPath("claude")
	return Discovery{Installed: fileExists(path) || err == nil, Paths: []string{path}, Automatic: true}
}

func (claudeCodeAdapter) Plan(_ context.Context, request ConfigureRequest) (ConfigPlan, error) {
	if err := validateConfigureRequest(request); err != nil {
		return ConfigPlan{}, err
	}
	path := filepath.Join(request.Home, ".claude", "settings.json")
	existing, _, err := readPlannedFile(request.Home, path)
	if err != nil {
		return ConfigPlan{}, err
	}
	environment := map[string]any{
		"ANTHROPIC_BASE_URL": serverOrigin(request.ServerURL),
	}
	if request.Model != "" {
		environment["ANTHROPIC_MODEL"] = request.Model
		environment["ANTHROPIC_DEFAULT_HAIKU_MODEL"] = request.Model
		environment["ANTHROPIC_DEFAULT_SONNET_MODEL"] = request.Model
		environment["ANTHROPIC_DEFAULT_OPUS_MODEL"] = request.Model
	}
	configured, err := mergeJSONMap(existing, []string{"env"}, environment)
	if err != nil {
		return ConfigPlan{}, err
	}
	profile := filepath.Join(request.Home, ".zshrc")
	if shell := os.Getenv("SHELL"); strings.Contains(shell, "bash") {
		profile = filepath.Join(request.Home, ".bashrc")
	}
	existingProfile, _, err := readPlannedFile(request.Home, profile)
	if err != nil {
		return ConfigPlan{}, err
	}
	managedProfile, err := ApplyManagedBlock(string(existingProfile), "claude-code-env", `export ANTHROPIC_AUTH_TOKEN="${LLMROUTER_TOKEN}"`)
	if err != nil {
		return ConfigPlan{}, err
	}
	return ConfigPlan{Client: ClientClaudeCode, Writes: []PlannedWrite{{Path: path, Data: configured, Mode: privateFileMode}, {Path: profile, Data: []byte(managedProfile), Mode: privateFileMode}}, Instructions: []string{"Set LLMROUTER_TOKEN before launching Claude Code."}, RestartRequired: true}, nil
}

func (claudeCodeAdapter) Check(_ context.Context, request CheckRequest) []Check {
	path := filepath.Join(request.Home, ".claude", "settings.json")
	checks := []Check{semanticConfigCheck("claude-code-config", checkClaudeCodeConfig(request, path))}
	if os.Getenv("LLMROUTER_TOKEN") == request.Token.Reveal() && request.Token.Reveal() != "" {
		checks = append(checks, Check{Name: "claude-code-credential", Status: "PASS", Message: "LLMROUTER_TOKEN is visible"})
	} else {
		checks = append(checks, Check{Name: "claude-code-credential", Status: "FAIL", Message: "LLMROUTER_TOKEN is not visible"})
	}
	return checks
}

func checkClaudeCodeConfig(request CheckRequest, path string) error {
	if request.ServerURL == nil || request.Token.Reveal() == "" {
		return fmt.Errorf("expected Claude Code settings are incomplete")
	}
	data, err := loadManagedConfig(request.Home, path)
	if err != nil {
		return err
	}
	var root map[string]any
	if err := common.UnmarshalUseNumber(data, &root); err != nil || root == nil {
		return fmt.Errorf("invalid Claude Code settings")
	}
	environment, ok := root["env"].(map[string]any)
	if !ok || environment["ANTHROPIC_BASE_URL"] != serverOrigin(request.ServerURL) {
		return fmt.Errorf("Claude Code gateway mismatch")
	}
	if request.Model != "" {
		for _, key := range []string{"ANTHROPIC_MODEL", "ANTHROPIC_DEFAULT_HAIKU_MODEL", "ANTHROPIC_DEFAULT_SONNET_MODEL", "ANTHROPIC_DEFAULT_OPUS_MODEL"} {
			if environment[key] != request.Model {
				return fmt.Errorf("Claude Code model mismatch")
			}
		}
	}
	return nil
}

func mergeJSONMap(input []byte, keys []string, additions map[string]any) ([]byte, error) {
	current := make(map[string]any)
	if len(input) != 0 {
		if err := common.UnmarshalUseNumber(input, &current); err != nil {
			return nil, fmt.Errorf("%w: invalid Claude Code JSON", ErrUsage)
		}
	}
	if current == nil {
		return nil, fmt.Errorf("%w: Claude Code settings root is not an object", ErrConflict)
	}
	nested := current
	for _, key := range keys {
		value, exists := nested[key]
		if !exists {
			child := make(map[string]any)
			nested[key] = child
			nested = child
			continue
		}
		child, ok := value.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("%w: Claude Code settings path is not an object", ErrConflict)
		}
		nested = child
	}
	for key, value := range additions {
		nested[key] = value
	}
	return common.Marshal(current)
}

var _ ClientAdapter = claudeCodeAdapter{}
