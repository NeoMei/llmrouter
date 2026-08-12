package onboarding

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/pelletier/go-toml/v2"
	"gopkg.in/yaml.v3"
)

type Check struct{ Name, Status, Message string }

type DoctorReport struct {
	Checks                  []Check
	Passed, Failed, Skipped int
}

type DoctorOptions struct {
	Home      string
	Platform  Platform
	ServerURL *url.URL
	Token     Secret
	Clients   []ClientID
	Model     string
	Smoke     bool
}

func RunDoctor(ctx context.Context, options DoctorOptions) DoctorReport {
	report := DoctorReport{}
	add := func(check Check) {
		if secret := options.Token.Reveal(); secret != "" {
			check.Name = strings.ReplaceAll(check.Name, secret, "[REDACTED]")
			check.Message = strings.ReplaceAll(check.Message, secret, "[REDACTED]")
		}
		report.Checks = append(report.Checks, sanitizeCheck(check))
	}

	if options.ServerURL == nil {
		add(Check{Name: "server-url", Status: "FAIL", Message: "server URL is required"})
	} else if _, err := NormalizeServerURL(options.ServerURL.String()); err != nil {
		add(Check{Name: "server-url", Status: "FAIL", Message: "server URL does not satisfy onboarding policy"})
	} else {
		add(Check{Name: "server-url", Status: "PASS", Message: "server URL satisfies onboarding policy"})
	}

	platform := options.Platform
	if platform.OS == "" {
		platform = Platform{OS: runtime.GOOS, Arch: runtime.GOARCH}
	}
	for _, id := range options.Clients {
		adapter, ok := AdapterFor(id)
		if !ok {
			status := "FAIL"
			message := "client adapter is not registered"
			if id == ClientClaudeDesktop {
				status = "SKIP"
				message = "Claude Desktop MCP support is not installed by this onboarding version"
			}
			add(Check{Name: string(id) + "-config", Status: status, Message: message})
			continue
		}
		checks := adapter.Check(ctx, CheckRequest{Home: options.Home, Platform: platform, ServerURL: options.ServerURL, Token: options.Token, Model: options.Model})
		for _, check := range checks {
			add(check)
		}
		discovery := adapter.Discover(platform, options.Home)
		if !discovery.Automatic && len(discovery.Paths) == 0 {
			add(Check{Name: string(id) + "-permissions", Status: "SKIP", Message: discovery.Reason})
		} else {
			add(checkManagedPermissions(string(id)+"-permissions", discovery.Paths))
		}
	}

	var catalog []ModelInfo
	var selectedModel *ModelInfo
	modelsAvailable := false
	if options.ServerURL != nil {
		client := NewAPIClient(options.ServerURL, options.Token, nil)
		if err := client.Health(ctx); err != nil {
			add(Check{Name: "health", Status: "FAIL", Message: safeNetworkMessage(err)})
		} else {
			add(Check{Name: "health", Status: "PASS", Message: "gateway health endpoint is reachable"})
		}
		listed, err := client.ListModelCatalog(ctx)
		if err != nil {
			add(Check{Name: "authentication", Status: "FAIL", Message: safeNetworkMessage(err)})
		} else {
			catalog, modelsAvailable = listed, true
			add(Check{Name: "authentication", Status: "PASS", Message: fmt.Sprintf("authenticated model catalog contains %d models", len(catalog))})
		}
	} else {
		add(Check{Name: "health", Status: "FAIL", Message: "server URL is unavailable"})
		add(Check{Name: "authentication", Status: "FAIL", Message: "server URL is unavailable"})
	}
	if options.Model == "" {
		add(Check{Name: "model", Status: "SKIP", Message: "no model was selected"})
	} else if !modelsAvailable {
		add(Check{Name: "model", Status: "SKIP", Message: "model catalog is unavailable"})
	} else {
		for index := range catalog {
			if catalog[index].ID == options.Model {
				selectedModel = &catalog[index]
				break
			}
		}
		if selectedModel != nil {
			add(Check{Name: "model", Status: "PASS", Message: "selected model is visible"})
		} else {
			add(Check{Name: "model", Status: "FAIL", Message: "selected model is not visible"})
		}
	}

	protocolSet := make(map[probeProtocol]struct{})
	for _, id := range options.Clients {
		if protocol, ok := clientProtocols[id]; ok {
			protocolSet[protocol] = struct{}{}
		}
	}
	protocols := make([]string, 0, len(protocolSet))
	for protocol := range protocolSet {
		protocols = append(protocols, string(protocol))
	}
	sort.Strings(protocols)
	for _, name := range protocols {
		protocol := probeProtocol(name)
		if protocol == probeMCP {
			continue
		}
		check := Check{Name: "model-protocol-" + name}
		if selectedModel == nil {
			check.Status, check.Message = "SKIP", "selected model capability metadata is unavailable"
		} else if len(selectedModel.SupportedEndpointTypes) == 0 {
			check.Status, check.Message = "SKIP", "model catalog does not declare endpoint capabilities"
		} else if modelSupportsProtocol(*selectedModel, protocol) {
			check.Status, check.Message = "PASS", "selected model supports the client protocol"
		} else {
			check.Status, check.Message = "FAIL", "selected model does not support the client protocol"
		}
		add(check)
	}
	for _, name := range protocols {
		protocol := probeProtocol(name)
		if protocol == probeMCP {
			continue
		}
		if !options.Smoke {
			add(Check{Name: "inference-" + name, Status: "SKIP", Message: "inference smoke test is opt-in"})
			continue
		}
		if options.ServerURL == nil || options.Model == "" {
			add(Check{Name: "inference-" + name, Status: "FAIL", Message: "server URL and model are required for smoke testing"})
			continue
		}
		client := NewAPIClient(options.ServerURL, options.Token, nil)
		var err error
		switch protocol {
		case probeResponses:
			err = client.ProbeResponses(ctx, options.Model)
		case probeMessages:
			err = client.ProbeMessages(ctx, options.Model)
		case probeChat:
			err = client.ProbeChatCompletions(ctx, options.Model)
		}
		if err != nil {
			add(Check{Name: "inference-" + name, Status: "FAIL", Message: safeNetworkMessage(err)})
		} else {
			add(Check{Name: "inference-" + name, Status: "PASS", Message: "bounded non-streaming probe succeeded"})
		}
	}
	if _, selected := protocolSet[probeMCP]; selected {
		if platform.OS != "darwin" && platform.OS != "windows" {
			add(Check{Name: "mcp-lifecycle", Status: "SKIP", Message: "official Claude Desktop is unsupported on Linux"})
			add(Check{Name: "inference-mcp", Status: "SKIP", Message: "official Claude Desktop is unsupported on Linux"})
		} else {
			lifecycleErr, smokeErr := probeClaudeDesktopMCP(ctx, CheckRequest{
				Home: options.Home, Platform: platform, ServerURL: options.ServerURL, Token: options.Token, Model: options.Model,
			}, options.Smoke)
			if lifecycleErr != nil {
				add(Check{Name: "mcp-lifecycle", Status: "FAIL", Message: "configured MCP bridge lifecycle check failed"})
			} else {
				add(Check{Name: "mcp-lifecycle", Status: "PASS", Message: "configured MCP bridge completed initialization and exposed the bounded tools"})
			}
			if !options.Smoke {
				add(Check{Name: "inference-mcp", Status: "SKIP", Message: "inference smoke test is opt-in"})
			} else if lifecycleErr != nil || smokeErr != nil {
				add(Check{Name: "inference-mcp", Status: "FAIL", Message: "bounded MCP chat tool call failed"})
			} else {
				add(Check{Name: "inference-mcp", Status: "PASS", Message: "bounded MCP chat tool call succeeded"})
			}
		}
	}

	for _, check := range report.Checks {
		switch check.Status {
		case "PASS":
			report.Passed++
		case "SKIP":
			report.Skipped++
		default:
			report.Failed++
		}
	}
	return report
}

func checkManagedPermissions(name string, paths []string) Check {
	if len(paths) == 0 {
		return Check{Name: name, Status: "FAIL", Message: "no managed configuration path was discovered"}
	}
	for _, path := range paths {
		info, err := os.Lstat(path)
		if err != nil || info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return Check{Name: name, Status: "FAIL", Message: "managed configuration path is unavailable or unsafe"}
		}
		if err := validateSecureOwner(info); err != nil || info.Mode().Perm()&0o077 != 0 {
			return Check{Name: name, Status: "FAIL", Message: "managed configuration permissions are not private"}
		}
	}
	return Check{Name: name, Status: "PASS", Message: "managed configuration permissions are private"}
}

func semanticConfigCheck(name string, err error) Check {
	if err != nil {
		return Check{Name: name, Status: "FAIL", Message: "managed configuration does not match the selected LLMROUTER settings"}
	}
	return Check{Name: name, Status: "PASS", Message: "managed configuration matches the selected LLMROUTER settings"}
}

func environmentCredentialCheck(name string, token Secret) Check {
	value, visible := os.LookupEnv("LLMROUTER_TOKEN")
	if !visible || value == "" || value != token.Reveal() {
		return Check{Name: name, Status: "FAIL", Message: "LLMROUTER_TOKEN is not visible with the expected value"}
	}
	return Check{Name: name, Status: "PASS", Message: "LLMROUTER_TOKEN is visible with the expected value"}
}

func loadManagedConfig(home, path string) ([]byte, error) {
	data, exists, err := readPlannedFile(home, path)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, fmt.Errorf("managed configuration is unavailable")
	}
	return data, nil
}

func validateConfigSyntax(path string, data []byte) error {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".json":
		var value map[string]any
		return common.Unmarshal(data, &value)
	case ".toml":
		var value map[string]any
		return toml.Unmarshal(data, &value)
	case ".yaml", ".yml":
		var value map[string]any
		return yaml.Unmarshal(data, &value)
	default:
		return nil
	}
}

func safeNetworkMessage(err error) string {
	message := err.Error()
	if index := strings.Index(message, "API status"); index >= 0 {
		return message[index:]
	}
	return "network request failed"
}

func sanitizeCheck(check Check) Check {
	if check.Status != "PASS" && check.Status != "FAIL" && check.Status != "SKIP" {
		check.Status = "FAIL"
	}
	if hasControl(check.Name) || len(check.Name) > 128 {
		check.Name = "invalid-check"
	}
	if hasControl(check.Message) || len(check.Message) > 512 {
		check.Message = "diagnostic message omitted"
	}
	return check
}

func modelSupportsProtocol(model ModelInfo, protocol probeProtocol) bool {
	required := map[probeProtocol]string{
		probeResponses: "openai-response",
		probeMessages:  "anthropic",
		probeChat:      "openai",
	}[protocol]
	for _, endpoint := range model.SupportedEndpointTypes {
		if endpoint == required {
			return true
		}
	}
	return false
}
