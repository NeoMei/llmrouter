package onboarding

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"gopkg.in/yaml.v3"
)

type continueAdapter struct{}

func (continueAdapter) ID() ClientID { return ClientContinue }

func (continueAdapter) Discover(_ Platform, home string) Discovery {
	paths := continuePaths(home)
	for _, path := range paths {
		if fileExists(path) {
			return Discovery{Installed: true, Paths: []string{path}, Automatic: true}
		}
	}
	unknown := filepath.Join(home, ".continue", "config.ts")
	if fileExists(unknown) {
		return Discovery{Installed: true, Paths: []string{unknown}, Automatic: false, Reason: "Continue configuration format is not recognized"}
	}
	return Discovery{Paths: paths, Automatic: true}
}

func (continueAdapter) Plan(_ context.Context, request ConfigureRequest) (ConfigPlan, error) {
	if err := validateConfigureRequest(request); err != nil {
		return ConfigPlan{}, err
	}
	path, strategy, err := selectContinueStrategy(request.Home)
	if err != nil {
		return ConfigPlan{}, err
	}
	if strategy == "manual" {
		return continueManualPlan(request), nil
	}
	existing, _, err := readPlannedFile(path)
	if err != nil {
		return ConfigPlan{}, err
	}
	model := map[string]any{
		"title":         "LLMAPI",
		"provider":      "openai",
		"model":         request.Model,
		"apiBase":       endpointV1(request.ServerURL),
		"apiKey":        "${LLMAPI_TOKEN}",
		"llmapiManaged": "LLMAPI:BEGIN continue",
	}
	var configured []byte
	switch strategy {
	case "json":
		configured, err = mergeContinueJSON(existing, model)
	case "yaml":
		configured, err = mergeContinueYAML(existing, model)
	default:
		err = fmt.Errorf("%w: unsupported Continue strategy", ErrUnavailable)
	}
	if err != nil {
		return continueManualPlan(request), nil
	}
	return ConfigPlan{Client: ClientContinue, Writes: []PlannedWrite{{Path: path, Data: configured, Mode: privateFileMode}}, Instructions: []string{"Set LLMAPI_TOKEN before launching Continue."}, RestartRequired: true}, nil
}

func continueManualPlan(request ConfigureRequest) ConfigPlan {
	model := request.Model
	if model == "" {
		model = "<select-model>"
	}
	instructions := fmt.Sprintf("Continue configuration format is not recognized; add a model manually with provider openai, apiBase %s, apiKey ${LLMAPI_TOKEN}, and model %s.", endpointV1(request.ServerURL), model)
	return ConfigPlan{Client: ClientContinue, Instructions: []string{instructions}}
}

func (continueAdapter) Check(_ context.Context, request CheckRequest) []Check {
	path, strategy, err := selectContinueStrategy(request.Home)
	if err != nil {
		return []Check{{Name: "continue-config", Status: "FAIL", Message: "cannot inspect Continue configuration"}}
	}
	if strategy == "manual" {
		return []Check{{Name: "continue-config", Status: "SKIP", Message: "manual configuration is required for this Continue format"}}
	}
	return []Check{checkManagedConfig("continue-config", path, []string{"LLMAPI:BEGIN continue", "LLMAPI_TOKEN"})}
}

func continuePaths(home string) []string {
	return []string{
		filepath.Join(home, ".continue", "config.json"),
		filepath.Join(home, ".continue", "config.yaml"),
		filepath.Join(home, ".continue", "config.yml"),
	}
}

func selectContinueStrategy(home string) (string, string, error) {
	var found []string
	for _, path := range continuePaths(home) {
		if fileExists(path) {
			found = append(found, path)
		}
	}
	if len(found) > 1 {
		return "", "", fmt.Errorf("%w: multiple Continue configuration formats found", ErrConflict)
	}
	if len(found) == 1 {
		extension := strings.ToLower(filepath.Ext(found[0]))
		if extension == ".json" {
			return found[0], "json", nil
		}
		return found[0], "yaml", nil
	}
	entries, err := os.ReadDir(filepath.Join(home, ".continue"))
	if err == nil {
		for _, entry := range entries {
			if !entry.IsDir() && strings.HasPrefix(strings.ToLower(entry.Name()), "config.") {
				return filepath.Join(home, ".continue", entry.Name()), "manual", nil
			}
		}
	}
	if err != nil && !os.IsNotExist(err) {
		return "", "", fmt.Errorf("%w: inspect Continue configuration", ErrUnsafe)
	}
	return continuePaths(home)[0], "json", nil
}

func mergeContinueJSON(input []byte, model map[string]any) ([]byte, error) {
	root := make(map[string]any)
	if len(strings.TrimSpace(string(input))) != 0 {
		if err := common.UnmarshalUseNumber(input, &root); err != nil {
			return nil, fmt.Errorf("%w: invalid Continue JSON", ErrUsage)
		}
	}
	if root == nil {
		return nil, fmt.Errorf("%w: Continue JSON root is not an object", ErrConflict)
	}
	models := make([]any, 0)
	if value, exists := root["models"]; exists {
		var ok bool
		models, ok = value.([]any)
		if !ok {
			return nil, fmt.Errorf("%w: Continue models is not an array", ErrConflict)
		}
	}
	updated := false
	for index, item := range models {
		entry, ok := item.(map[string]any)
		if !ok || entry["llmapiManaged"] != "LLMAPI:BEGIN continue" {
			continue
		}
		models[index] = model
		updated = true
	}
	if !updated {
		models = append(models, model)
	}
	root["models"] = models
	return common.Marshal(root)
}

func mergeContinueYAML(input []byte, model map[string]any) ([]byte, error) {
	root := make(map[string]any)
	if len(strings.TrimSpace(string(input))) != 0 {
		if err := yaml.Unmarshal(input, &root); err != nil {
			return nil, fmt.Errorf("%w: invalid Continue YAML", ErrUsage)
		}
	}
	if root == nil {
		return nil, fmt.Errorf("%w: Continue YAML root is not an object", ErrConflict)
	}
	models := make([]any, 0)
	if value, exists := root["models"]; exists {
		var ok bool
		models, ok = value.([]any)
		if !ok {
			return nil, fmt.Errorf("%w: Continue models is not an array", ErrConflict)
		}
	}
	updated := false
	for index, item := range models {
		entry, ok := item.(map[string]any)
		if !ok || entry["llmapiManaged"] != "LLMAPI:BEGIN continue" {
			continue
		}
		models[index] = model
		updated = true
	}
	if !updated {
		models = append(models, model)
	}
	root["models"] = models
	configured, err := yaml.Marshal(root)
	if err != nil {
		return nil, fmt.Errorf("%w: encode Continue YAML", ErrUsage)
	}
	return configured, nil
}

var _ ClientAdapter = continueAdapter{}
