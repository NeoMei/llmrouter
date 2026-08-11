package onboarding

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

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
	if len(existing) != 0 {
		var root map[string]any
		if err := toml.Unmarshal(existing, &root); err != nil {
			return ConfigPlan{}, fmt.Errorf("%w: invalid Codex TOML", ErrUsage)
		}
	}
	configured := mergeCodexText(string(existing), request)
	if err := validateConfigSyntax(path, []byte(configured)); err != nil {
		return ConfigPlan{}, fmt.Errorf("%w: Codex configuration conflicts with the managed provider", ErrConflict)
	}
	instructions := []string{"Set LLMAPI_TOKEN in the environment before launching Codex."}
	if adapter.id == ClientCodexDesktop {
		instructions = []string{"Set LLMAPI_TOKEN in the desktop launch environment, then restart Codex Desktop."}
	}
	return ConfigPlan{Client: adapter.id, Writes: []PlannedWrite{{Path: path, Data: []byte(configured), Mode: privateFileMode}}, Instructions: instructions, RestartRequired: adapter.id == ClientCodexDesktop}, nil
}

func mergeCodexText(input string, request ConfigureRequest) string {
	lines := strings.SplitAfter(input, "\n")
	rootLines := make([]string, 0, len(lines))
	tableLines := make([]string, 0, len(lines))
	inTables := false
	inManagedProvider := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(strings.TrimSuffix(line, "\n"))
		if trimmed == "# LLMAPI:BEGIN codex" || trimmed == "# LLMAPI:END codex" || trimmed == "# Managed keys: model, model_provider, model_providers.llmapi" {
			continue
		}
		table := codexTableName(trimmed)
		if inManagedProvider {
			if table == "" {
				continue
			}
			inManagedProvider = false
		}
		if table == "model_providers.llmapi" {
			inManagedProvider = true
			inTables = true
			continue
		}
		if table != "" {
			inTables = true
		}
		if !inTables && isCodexSelectionLine(trimmed) {
			continue
		}
		if inTables {
			tableLines = append(tableLines, line)
		} else {
			rootLines = append(rootLines, line)
		}
	}
	var output strings.Builder
	output.WriteString(strings.Join(rootLines, ""))
	if output.Len() != 0 && !strings.HasSuffix(output.String(), "\n") {
		output.WriteByte('\n')
	}
	output.WriteString("# LLMAPI:BEGIN codex\n")
	output.WriteString("model = " + strconv.Quote(request.Model) + "\n")
	output.WriteString("model_provider = \"llmapi\"\n")
	output.WriteString("# LLMAPI:END codex\n")
	output.WriteString(strings.Join(tableLines, ""))
	if output.Len() != 0 && !strings.HasSuffix(output.String(), "\n") {
		output.WriteByte('\n')
	}
	if !strings.HasSuffix(output.String(), "\n\n") {
		output.WriteByte('\n')
	}
	output.WriteString("[model_providers.llmapi]\n")
	output.WriteString("name = \"LLMAPI\"\n")
	output.WriteString("base_url = " + strconv.Quote(endpointV1(request.ServerURL)) + "\n")
	output.WriteString("wire_api = \"responses\"\n")
	output.WriteString("env_key = \"LLMAPI_TOKEN\"\n")
	return output.String()
}

func codexTableName(line string) string {
	code, _, _ := strings.Cut(line, "#")
	code = strings.TrimSpace(code)
	if len(code) < 3 || !strings.HasPrefix(code, "[") || !strings.HasSuffix(code, "]") || strings.HasPrefix(code, "[[") {
		return ""
	}
	return strings.ReplaceAll(strings.TrimSpace(code[1:len(code)-1]), " ", "")
}

func isCodexSelectionLine(line string) bool {
	code, _, _ := strings.Cut(line, "#")
	key, _, ok := strings.Cut(code, "=")
	if !ok {
		return false
	}
	key = strings.TrimSpace(key)
	return key == "model" || key == "model_provider"
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
