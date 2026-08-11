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
			strategy := "yaml"
			if strings.EqualFold(filepath.Ext(path), ".json") {
				strategy = "json"
			}
			data, _, err := readPlannedFile(home, path)
			if err != nil || !continueConfigSupported(strategy, data) {
				return Discovery{Installed: true, Paths: []string{path}, Automatic: false, Reason: "Continue configuration schema is not recognized"}
			}
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
	existing, _, err := readPlannedFile(request.Home, path)
	if err != nil {
		return ConfigPlan{}, err
	}
	var configured []byte
	switch strategy {
	case "json":
		configured, err = mergeContinueJSON(existing, map[string]any{
			"title": "LLMAPI", "provider": "openai", "model": request.Model,
			"apiBase": endpointV1(request.ServerURL), "apiKey": "${LLMAPI_TOKEN}",
			"llmapiManaged": "LLMAPI:BEGIN continue",
		})
	case "yaml":
		configured, err = mergeContinueYAML(existing, map[string]any{
			"name": "LLMAPI", "provider": "openai", "model": request.Model,
			"apiBase": endpointV1(request.ServerURL), "apiKey": "${LLMAPI_TOKEN}",
		})
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
	data, _, readErr := readPlannedFile(request.Home, path)
	if readErr != nil {
		return []Check{semanticConfigCheck("continue-config", readErr)}
	}
	if !continueConfigSupported(strategy, data) {
		return []Check{{Name: "continue-config", Status: "SKIP", Message: "manual configuration is required for this Continue schema"}}
	}
	return []Check{semanticConfigCheck("continue-config", checkContinueConfig(request, path, strategy))}
}

func continueConfigSupported(strategy string, data []byte) bool {
	model := map[string]any{"name": "LLMAPI", "title": "LLMAPI", "provider": "openai", "model": "model", "apiBase": "https://example.invalid/v1", "apiKey": "${LLMAPI_TOKEN}", "llmapiManaged": "LLMAPI:BEGIN continue"}
	var err error
	if strategy == "json" {
		_, err = mergeContinueJSON(data, model)
	} else {
		delete(model, "title")
		delete(model, "llmapiManaged")
		_, err = mergeContinueYAML(data, model)
	}
	return err == nil
}

func checkContinueConfig(request CheckRequest, path, strategy string) error {
	if request.ServerURL == nil || request.Model == "" {
		return fmt.Errorf("expected Continue settings are incomplete")
	}
	data, err := loadManagedConfig(request.Home, path)
	if err != nil {
		return err
	}
	var root map[string]any
	if strategy == "json" {
		if err := common.UnmarshalUseNumber(data, &root); err != nil || root == nil {
			return fmt.Errorf("invalid Continue JSON")
		}
	} else if err := yaml.Unmarshal(data, &root); err != nil || root == nil {
		return fmt.Errorf("invalid Continue YAML")
	}
	if strategy == "yaml" {
		name, nameOK := root["name"].(string)
		version, versionOK := root["version"].(string)
		if !nameOK || strings.TrimSpace(name) == "" || !versionOK || !validContinueVersion(version) || root["schema"] != "v1" {
			return fmt.Errorf("unsupported Continue YAML schema")
		}
	}
	models, ok := root["models"].([]any)
	if !ok {
		return fmt.Errorf("Continue models missing")
	}
	for _, item := range models {
		model, ok := item.(map[string]any)
		if !ok {
			continue
		}
		managed := model["llmapiManaged"] == "LLMAPI:BEGIN continue"
		if strategy == "yaml" {
			managed = model["name"] == "LLMAPI"
		}
		if managed && model["provider"] == "openai" && model["model"] == request.Model && model["apiBase"] == endpointV1(request.ServerURL) && model["apiKey"] == "${LLMAPI_TOKEN}" {
			if strategy == "json" && model["title"] != "LLMAPI" {
				return fmt.Errorf("Continue legacy model title mismatch")
			}
			return nil
		}
	}
	return fmt.Errorf("Continue managed model mismatch")
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
	return filepath.Join(home, ".continue", "config.yaml"), "yaml", nil
}

func mergeContinueJSON(input []byte, model map[string]any) ([]byte, error) {
	root := make(map[string]any)
	if len(strings.TrimSpace(string(input))) == 0 {
		return nil, fmt.Errorf("%w: legacy Continue JSON requires a models array", ErrUnavailable)
	}
	if err := common.UnmarshalUseNumber(input, &root); err != nil {
		return nil, fmt.Errorf("%w: invalid Continue JSON", ErrUsage)
	}
	if root == nil {
		return nil, fmt.Errorf("%w: Continue JSON root is not an object", ErrConflict)
	}
	value, exists := root["models"]
	if !exists {
		return nil, fmt.Errorf("%w: legacy Continue JSON requires a models array", ErrUnavailable)
	}
	models, ok := value.([]any)
	if !ok {
		return nil, fmt.Errorf("%w: Continue models is not an array", ErrConflict)
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
	root := map[string]any{
		"name": "LLMAPI", "version": "0.0.1", "schema": "v1", "models": []any{},
	}
	if len(strings.TrimSpace(string(input))) != 0 {
		root = make(map[string]any)
		if err := yaml.Unmarshal(input, &root); err != nil {
			return nil, fmt.Errorf("%w: invalid Continue YAML", ErrUsage)
		}
	}
	if root == nil {
		return nil, fmt.Errorf("%w: Continue YAML root is not an object", ErrConflict)
	}
	name, nameOK := root["name"].(string)
	version, versionOK := root["version"].(string)
	schema, schemaOK := root["schema"].(string)
	if !nameOK || strings.TrimSpace(name) == "" || !versionOK || !validContinueVersion(version) || !schemaOK || schema != "v1" {
		return nil, fmt.Errorf("%w: unsupported Continue YAML schema", ErrUnavailable)
	}
	value, exists := root["models"]
	if !exists {
		return nil, fmt.Errorf("%w: Continue YAML requires a models array", ErrUnavailable)
	}
	models, ok := value.([]any)
	if !ok {
		return nil, fmt.Errorf("%w: Continue models is not an array", ErrConflict)
	}
	updated := false
	for index, item := range models {
		entry, ok := item.(map[string]any)
		if !ok || entry["name"] != "LLMAPI" || entry["provider"] != "openai" {
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
	return append([]byte("# LLMAPI:BEGIN continue\n# LLMAPI:END continue\n"), configured...), nil
}

func validContinueVersion(version string) bool {
	parts := strings.Split(version, ".")
	if len(parts) != 3 {
		return false
	}
	for _, part := range parts {
		if part == "" {
			return false
		}
		for _, character := range part {
			if character < '0' || character > '9' {
				return false
			}
		}
	}
	return true
}

var _ ClientAdapter = continueAdapter{}
