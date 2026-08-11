package onboarding

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"

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
	existing, _, err := readPlannedFile(path)
	if err != nil {
		return ConfigPlan{}, err
	}
	environment := map[string]any{
		"ANTHROPIC_BASE_URL":   serverOrigin(request.ServerURL),
		"ANTHROPIC_AUTH_TOKEN": request.Token.Reveal(),
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
	return ConfigPlan{Client: ClientClaudeCode, Writes: []PlannedWrite{{Path: path, Data: configured, Mode: privateFileMode}}, RestartRequired: true}, nil
}

func (claudeCodeAdapter) Check(_ context.Context, request CheckRequest) []Check {
	path := filepath.Join(request.Home, ".claude", "settings.json")
	return []Check{checkManagedConfig("claude-code-config", path, []string{`"ANTHROPIC_BASE_URL"`, `"ANTHROPIC_AUTH_TOKEN"`})}
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
