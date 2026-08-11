package onboarding

import (
	"context"
	"os/exec"
	"path/filepath"
)

type openCodeAdapter struct{}

func (openCodeAdapter) ID() ClientID { return ClientOpenCode }

func (openCodeAdapter) Discover(_ Platform, home string) Discovery {
	path := filepath.Join(home, ".config", "opencode", "opencode.json")
	_, err := exec.LookPath("opencode")
	return Discovery{Installed: fileExists(path) || err == nil, Paths: []string{path}, Automatic: true}
}

func (openCodeAdapter) Plan(_ context.Context, request ConfigureRequest) (ConfigPlan, error) {
	if err := validateConfigureRequest(request); err != nil {
		return ConfigPlan{}, err
	}
	path := filepath.Join(request.Home, ".config", "opencode", "opencode.json")
	existing, _, err := readPlannedFile(path)
	if err != nil {
		return ConfigPlan{}, err
	}
	provider := map[string]any{
		"npm":  "@ai-sdk/openai-compatible",
		"name": "LLMAPI",
		"options": map[string]any{
			"baseURL": endpointV1(request.ServerURL),
			"apiKey":  "{env:LLMAPI_TOKEN}",
		},
	}
	if request.Model != "" {
		provider["models"] = map[string]any{request.Model: map[string]any{"name": request.Model}}
	}
	configured, err := MergeJSONObject(existing, []string{"provider", "llmapi"}, provider)
	if err != nil {
		return ConfigPlan{}, err
	}
	configured, err = MergeJSONObject(configured, []string{"_llmapi"}, "LLMAPI:BEGIN opencode")
	if err != nil {
		return ConfigPlan{}, err
	}
	return ConfigPlan{Client: ClientOpenCode, Writes: []PlannedWrite{{Path: path, Data: configured, Mode: privateFileMode}}, Instructions: []string{"Set LLMAPI_TOKEN before launching OpenCode."}}, nil
}

func (openCodeAdapter) Check(_ context.Context, request CheckRequest) []Check {
	path := filepath.Join(request.Home, ".config", "opencode", "opencode.json")
	return []Check{checkManagedConfig("opencode-config", path, []string{`"llmapi"`, `"@ai-sdk/openai-compatible"`, `"{env:LLMAPI_TOKEN}"`})}
}

var _ ClientAdapter = openCodeAdapter{}
