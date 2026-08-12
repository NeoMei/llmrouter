package onboarding

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"

	"github.com/QuantumNous/new-api/common"
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
	existing, _, err := readPlannedFile(request.Home, path)
	if err != nil {
		return ConfigPlan{}, err
	}
	provider := map[string]any{
		"npm":  "@ai-sdk/openai-compatible",
		"name": "LLMROUTER",
		"options": map[string]any{
			"baseURL": endpointV1(request.ServerURL),
			"apiKey":  "{env:LLMROUTER_TOKEN}",
		},
	}
	if request.Model != "" {
		provider["models"] = map[string]any{request.Model: map[string]any{"name": request.Model}}
	}
	configured, err := MergeJSONObject(existing, []string{"provider", "llmrouter"}, provider)
	if err != nil {
		return ConfigPlan{}, err
	}
	configured, err = MergeJSONObject(configured, []string{"_llmapi"}, "LLMROUTER:BEGIN opencode")
	if err != nil {
		return ConfigPlan{}, err
	}
	return ConfigPlan{Client: ClientOpenCode, Writes: []PlannedWrite{{Path: path, Data: configured, Mode: privateFileMode}}, Instructions: []string{"Set LLMROUTER_TOKEN before launching OpenCode."}}, nil
}

func (openCodeAdapter) Check(_ context.Context, request CheckRequest) []Check {
	path := filepath.Join(request.Home, ".config", "opencode", "opencode.json")
	return []Check{
		semanticConfigCheck("opencode-config", checkOpenCodeConfig(request, path)),
		environmentCredentialCheck("opencode-credential", request.Token),
	}
}

func checkOpenCodeConfig(request CheckRequest, path string) error {
	if request.ServerURL == nil {
		return fmt.Errorf("expected OpenCode settings are incomplete")
	}
	data, err := loadManagedConfig(request.Home, path)
	if err != nil {
		return err
	}
	var root map[string]any
	if err := common.UnmarshalUseNumber(data, &root); err != nil || root == nil {
		return fmt.Errorf("invalid OpenCode settings")
	}
	providers, ok := root["provider"].(map[string]any)
	if !ok {
		return fmt.Errorf("OpenCode providers missing")
	}
	provider, ok := providers["llmrouter"].(map[string]any)
	if !ok || provider["npm"] != "@ai-sdk/openai-compatible" || provider["name"] != "LLMROUTER" {
		return fmt.Errorf("OpenCode provider mismatch")
	}
	options, ok := provider["options"].(map[string]any)
	if !ok || options["baseURL"] != endpointV1(request.ServerURL) || options["apiKey"] != "{env:LLMROUTER_TOKEN}" {
		return fmt.Errorf("OpenCode options mismatch")
	}
	if request.Model != "" {
		models, ok := provider["models"].(map[string]any)
		if !ok {
			return fmt.Errorf("OpenCode models missing")
		}
		model, ok := models[request.Model].(map[string]any)
		if !ok || model["name"] != request.Model {
			return fmt.Errorf("OpenCode model mismatch")
		}
	}
	return nil
}

var _ ClientAdapter = openCodeAdapter{}
