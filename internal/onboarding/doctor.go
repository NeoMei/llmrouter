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

	platform := Platform{OS: runtime.GOOS, Arch: runtime.GOARCH}
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
		add(checkManagedPermissions(string(id)+"-permissions", adapter.Discover(platform, options.Home).Paths))
	}

	var models []string
	modelsAvailable := false
	if options.ServerURL != nil {
		client := NewAPIClient(options.ServerURL, options.Token, nil)
		if err := client.Health(ctx); err != nil {
			add(Check{Name: "health", Status: "FAIL", Message: safeNetworkMessage(err)})
		} else {
			add(Check{Name: "health", Status: "PASS", Message: "gateway health endpoint is reachable"})
		}
		listed, err := client.ListModels(ctx)
		if err != nil {
			add(Check{Name: "authentication", Status: "FAIL", Message: safeNetworkMessage(err)})
		} else {
			models, modelsAvailable = listed, true
			add(Check{Name: "authentication", Status: "PASS", Message: fmt.Sprintf("authenticated model catalog contains %d models", len(models))})
		}
	} else {
		add(Check{Name: "health", Status: "FAIL", Message: "server URL is unavailable"})
		add(Check{Name: "authentication", Status: "FAIL", Message: "server URL is unavailable"})
	}
	if options.Model == "" {
		add(Check{Name: "model", Status: "SKIP", Message: "no model was selected"})
	} else if !modelsAvailable {
		add(Check{Name: "model", Status: "SKIP", Message: "model catalog is unavailable"})
	} else if containsModel(models, options.Model) {
		add(Check{Name: "model", Status: "PASS", Message: "selected model is visible"})
	} else {
		add(Check{Name: "model", Status: "FAIL", Message: "selected model is not visible"})
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
		if !options.Smoke {
			add(Check{Name: "inference-" + name, Status: "SKIP", Message: "inference smoke test is opt-in"})
			continue
		}
		if protocol == probeMCP {
			add(Check{Name: "inference-" + name, Status: "SKIP", Message: "Claude Desktop MCP probe is not available in this task"})
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

func checkManagedConfig(name, path string, required []string) Check {
	info, err := os.Lstat(path)
	if err != nil || info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return Check{Name: name, Status: "FAIL", Message: "managed configuration file is unavailable or unsafe"}
	}
	if err := validateSecureOwner(info); err != nil || info.Mode().Perm()&0o077 != 0 {
		return Check{Name: name, Status: "FAIL", Message: "managed configuration permissions are not private"}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Check{Name: name, Status: "FAIL", Message: "managed configuration cannot be read"}
	}
	for _, expected := range required {
		if !strings.Contains(string(data), expected) {
			return Check{Name: name, Status: "FAIL", Message: "managed LLMAPI settings are missing"}
		}
	}
	if err := validateConfigSyntax(path, data); err != nil {
		return Check{Name: name, Status: "FAIL", Message: "managed configuration syntax is invalid"}
	}
	return Check{Name: name, Status: "PASS", Message: "managed configuration and permissions are valid"}
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

func containsModel(models []string, selected string) bool {
	for _, model := range models {
		if model == selected {
			return true
		}
	}
	return false
}
