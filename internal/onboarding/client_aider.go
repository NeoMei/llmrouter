package onboarding

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

type aiderAdapter struct{}

func (aiderAdapter) ID() ClientID { return ClientAider }

func (aiderAdapter) Discover(_ Platform, home string) Discovery {
	config := filepath.Join(home, ".aider.conf.yml")
	environment := filepath.Join(home, ".config", "llmapi", "aider.env")
	_, err := exec.LookPath("aider")
	return Discovery{Installed: fileExists(config) || err == nil, Paths: []string{config, environment}, Automatic: true}
}

func (aiderAdapter) Plan(_ context.Context, request ConfigureRequest) (ConfigPlan, error) {
	if err := validateConfigureRequest(request); err != nil {
		return ConfigPlan{}, err
	}
	if request.Model == "" {
		return ConfigPlan{}, fmt.Errorf("%w: Aider requires a selected model", ErrUsage)
	}
	configPath := filepath.Join(request.Home, ".aider.conf.yml")
	environmentPath := filepath.Join(request.Home, ".config", "llmapi", "aider.env")
	config, _, err := readPlannedFile(request.Home, configPath)
	if err != nil {
		return ConfigPlan{}, err
	}
	withoutManaged, err := RemoveManagedBlock(string(config), "aider")
	if err != nil {
		return ConfigPlan{}, err
	}
	var document yaml.Node
	if strings.TrimSpace(withoutManaged) != "" {
		if err := yaml.Unmarshal([]byte(withoutManaged), &document); err != nil || len(document.Content) != 1 || document.Content[0].Kind != yaml.MappingNode {
			return ConfigPlan{}, fmt.Errorf("%w: invalid Aider YAML", ErrUsage)
		}
	}
	lines := strings.SplitAfter(withoutManaged, "\n")
	filtered := make([]string, 0, len(lines))
	for _, line := range lines {
		if isAiderManagedKeyLine(line) {
			continue
		}
		filtered = append(filtered, line)
	}
	configured, err := ApplyManagedBlock(strings.Join(filtered, ""), "aider", "env-file: "+strconv.Quote(environmentPath)+"\nmodel: "+strconv.Quote("openai/"+request.Model))
	if err != nil {
		return ConfigPlan{}, err
	}
	if err := validateConfigSyntax(configPath, []byte(configured)); err != nil {
		return ConfigPlan{}, fmt.Errorf("%w: generated Aider YAML is invalid", ErrConflict)
	}
	environment, _, err := readPlannedFile(request.Home, environmentPath)
	if err != nil {
		return ConfigPlan{}, err
	}
	environmentBlock := strings.Join([]string{
		"OPENAI_API_BASE=" + strconv.Quote(endpointV1(request.ServerURL)),
		"OPENAI_API_KEY=" + strconv.Quote(request.Token.Reveal()),
	}, "\n")
	configuredEnvironment, err := ApplyManagedBlock(string(environment), "aider-env", environmentBlock)
	if err != nil {
		return ConfigPlan{}, err
	}
	return ConfigPlan{Client: ClientAider, Writes: []PlannedWrite{
		{Path: configPath, Data: []byte(configured), Mode: privateFileMode},
		{Path: environmentPath, Data: []byte(configuredEnvironment), Mode: privateFileMode},
	}, RestartRequired: true}, nil
}

func isAiderManagedKeyLine(line string) bool {
	if strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t") {
		return false
	}
	code, _, _ := strings.Cut(line, "#")
	key, _, ok := strings.Cut(code, ":")
	if !ok {
		return false
	}
	key = strings.TrimSpace(key)
	return key == "env-file" || key == "model"
}

func (aiderAdapter) Check(_ context.Context, request CheckRequest) []Check {
	config := filepath.Join(request.Home, ".aider.conf.yml")
	environment := filepath.Join(request.Home, ".config", "llmapi", "aider.env")
	return []Check{
		semanticConfigCheck("aider-config", checkAiderConfig(request, config, environment)),
		semanticConfigCheck("aider-environment", checkAiderEnvironment(request, environment)),
	}
}

func checkAiderConfig(request CheckRequest, configPath, environmentPath string) error {
	if request.Model == "" {
		return fmt.Errorf("expected Aider model is missing")
	}
	data, err := loadManagedConfig(request.Home, configPath)
	if err != nil {
		return err
	}
	var root map[string]any
	if err := yaml.Unmarshal(data, &root); err != nil || root == nil {
		return fmt.Errorf("invalid Aider YAML")
	}
	if root["env-file"] != environmentPath || root["model"] != "openai/"+request.Model {
		return fmt.Errorf("Aider model or environment mismatch")
	}
	return nil
}

func checkAiderEnvironment(request CheckRequest, environmentPath string) error {
	if request.ServerURL == nil || request.Token.Reveal() == "" {
		return fmt.Errorf("expected Aider environment is incomplete")
	}
	data, err := loadManagedConfig(request.Home, environmentPath)
	if err != nil {
		return err
	}
	managed, err := aiderManagedEnvironment(string(data))
	if err != nil {
		return err
	}
	values := make(map[string]string)
	for _, line := range strings.Split(managed, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		key, raw, ok := strings.Cut(line, "=")
		if !ok || key == "" {
			return fmt.Errorf("invalid Aider environment")
		}
		value, err := strconv.Unquote(raw)
		if err != nil {
			return fmt.Errorf("invalid Aider environment value")
		}
		values[key] = value
	}
	if len(values) != 2 || values["OPENAI_API_BASE"] != endpointV1(request.ServerURL) || values["OPENAI_API_KEY"] != request.Token.Reveal() {
		return fmt.Errorf("Aider environment mismatch")
	}
	return nil
}

func aiderManagedEnvironment(input string) (string, error) {
	begin := "# LLMAPI:BEGIN aider-env"
	end := "# LLMAPI:END aider-env"
	inBlock := false
	found := false
	var content strings.Builder
	for _, line := range strings.SplitAfter(input, "\n") {
		text := strings.TrimSuffix(line, "\n")
		switch text {
		case begin:
			if inBlock || found {
				return "", fmt.Errorf("duplicate Aider environment block")
			}
			inBlock, found = true, true
		case end:
			if !inBlock {
				return "", fmt.Errorf("unmatched Aider environment block")
			}
			inBlock = false
		default:
			if inBlock {
				content.WriteString(line)
			}
		}
	}
	if inBlock || !found {
		return "", fmt.Errorf("Aider environment block is unavailable")
	}
	return content.String(), nil
}

var _ ClientAdapter = aiderAdapter{}
