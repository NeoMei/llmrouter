package onboarding

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
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
	configPath := filepath.Join(request.Home, ".aider.conf.yml")
	environmentPath := filepath.Join(request.Home, ".config", "llmapi", "aider.env")
	config, _, err := readPlannedFile(configPath)
	if err != nil {
		return ConfigPlan{}, err
	}
	if len(config) != 0 {
		if err := validateConfigSyntax(configPath, config); err != nil {
			return ConfigPlan{}, fmt.Errorf("%w: invalid Aider YAML", ErrUsage)
		}
	}
	if hasUnmanagedYAMLKey(string(config), "env-file") {
		return ConfigPlan{}, fmt.Errorf("%w: Aider env-file is already configured outside the LLMAPI block", ErrConflict)
	}
	configured, err := ApplyManagedBlock(string(config), "aider", "env-file: "+strconv.Quote(environmentPath))
	if err != nil {
		return ConfigPlan{}, err
	}
	if err := validateConfigSyntax(configPath, []byte(configured)); err != nil {
		return ConfigPlan{}, fmt.Errorf("%w: generated Aider YAML is invalid", ErrConflict)
	}
	environment, _, err := readPlannedFile(environmentPath)
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

func (aiderAdapter) Check(_ context.Context, request CheckRequest) []Check {
	config := filepath.Join(request.Home, ".aider.conf.yml")
	environment := filepath.Join(request.Home, ".config", "llmapi", "aider.env")
	return []Check{
		checkManagedConfig("aider-config", config, []string{"LLMAPI:BEGIN aider", "env-file:"}),
		checkManagedConfig("aider-environment", environment, []string{"OPENAI_API_BASE", "OPENAI_API_KEY"}),
	}
}

func hasUnmanagedYAMLKey(input, key string) bool {
	without, err := RemoveManagedBlock(input, "aider")
	if err != nil {
		return true
	}
	for _, line := range strings.Split(without, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, key+":") {
			return true
		}
	}
	return false
}

var _ ClientAdapter = aiderAdapter{}
