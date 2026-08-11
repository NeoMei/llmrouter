package onboarding

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
)

var (
	ErrUsage       = errors.New("onboarding usage error")
	ErrUnsafe      = errors.New("unsafe filesystem or configuration")
	ErrConflict    = errors.New("configuration conflict")
	ErrNetwork     = errors.New("network or authentication failure")
	ErrUnavailable = errors.New("onboarding capability unavailable")
)

type Inputs struct {
	Args       []string
	Env        map[string]string
	PageOrigin string
	Prompt     Prompter
}

type Options struct {
	ServerURL *url.URL
	Token     Secret
	Clients   []ClientID
	Model     string
	Smoke     bool
}

type ClientID string

const (
	ClientCodexCLI      ClientID = "codex-cli"
	ClientCodexDesktop  ClientID = "codex-desktop"
	ClientClaudeCode    ClientID = "claude-code"
	ClientClaudeDesktop ClientID = "claude-desktop"
	ClientOpenCode      ClientID = "opencode"
	ClientContinue      ClientID = "continue"
	ClientAider         ClientID = "aider"
)

var supportedClients = []ClientID{
	ClientCodexCLI, ClientCodexDesktop, ClientClaudeCode, ClientClaudeDesktop,
	ClientOpenCode, ClientContinue, ClientAider,
}

type Secret struct{ value string }

func NewSecret(value string) Secret { return Secret{value: value} }
func (Secret) String() string       { return "[REDACTED]" }
func (s Secret) Reveal() string     { return s.value }

type Prompter interface {
	ReadHidden(label string) (string, error)
	SelectMany(label string, allowed []ClientID) ([]ClientID, error)
}

// ResolveOptions applies command-line, environment, page-origin, then prompt precedence.
func ResolveOptions(input Inputs) (Options, error) {
	flags, err := parseOptionFlags(input.Args)
	if err != nil {
		return Options{}, err
	}

	server := firstNonEmpty(flags.serverURL, input.Env["LLMAPI_SERVER_URL"], input.PageOrigin)
	if server == "" {
		return Options{}, fmt.Errorf("%w: --server-url, LLMAPI_SERVER_URL, or page origin is required", ErrUsage)
	}
	serverURL, err := NormalizeServerURL(server)
	if err != nil {
		return Options{}, err
	}

	token := input.Env["LLMAPI_TOKEN"]
	if token == "" {
		if input.Prompt == nil {
			return Options{}, fmt.Errorf("%w: LLMAPI_TOKEN or hidden prompt is required", ErrUsage)
		}
		token, err = input.Prompt.ReadHidden("LLMAPI token")
		if err != nil || token == "" {
			return Options{}, fmt.Errorf("%w: LLMAPI_TOKEN or hidden prompt is required", ErrUsage)
		}
	}
	if hasControl(token) {
		return Options{}, fmt.Errorf("%w: token contains control characters", ErrUsage)
	}

	clients, err := resolveClients(flags.clients, input.Env["LLMAPI_CLIENTS"], input.Prompt)
	if err != nil {
		return Options{}, err
	}
	model := firstNonEmpty(flags.model, input.Env["LLMAPI_MODEL"])
	if hasControl(model) {
		return Options{}, fmt.Errorf("%w: model contains control characters", ErrUsage)
	}

	return Options{ServerURL: serverURL, Token: NewSecret(token), Clients: clients, Model: model, Smoke: flags.smoke}, nil
}

type optionFlags struct {
	serverURL string
	clients   string
	model     string
	smoke     bool
}

func parseOptionFlags(args []string) (optionFlags, error) {
	var result optionFlags
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--token" || strings.HasPrefix(arg, "--token=") || arg == "--api-token" || strings.HasPrefix(arg, "--api-token=") {
			return result, fmt.Errorf("%w: pass the token through LLMAPI_TOKEN or hidden prompt", ErrUsage)
		}
		name, value, hasValue := strings.Cut(arg, "=")
		if name == "--smoke" {
			if hasValue {
				return result, fmt.Errorf("%w: --smoke does not take a value", ErrUsage)
			}
			result.smoke = true
			continue
		}
		if name != "--server-url" && name != "--clients" && name != "--model" {
			return result, fmt.Errorf("%w: unknown option %q", ErrUsage, name)
		}
		if !hasValue {
			i++
			if i >= len(args) {
				return result, fmt.Errorf("%w: %s requires a value", ErrUsage, name)
			}
			value = args[i]
		}
		if value == "" || hasControl(value) {
			return result, fmt.Errorf("%w: %s has an invalid value", ErrUsage, name)
		}
		switch name {
		case "--server-url":
			result.serverURL = value
		case "--clients":
			result.clients = value
		case "--model":
			result.model = value
		}
	}
	return result, nil
}

func resolveClients(flagValue, envValue string, prompt Prompter) ([]ClientID, error) {
	value := firstNonEmpty(flagValue, envValue)
	if value != "" {
		parts := strings.Split(value, ",")
		clients := make([]ClientID, 0, len(parts))
		seen := make(map[ClientID]bool, len(parts))
		for _, part := range parts {
			client := ClientID(strings.TrimSpace(part))
			if hasControl(string(client)) || !isSupportedClient(client) {
				return nil, fmt.Errorf("%w: unknown client %q", ErrUsage, client)
			}
			if !seen[client] {
				seen[client] = true
				clients = append(clients, client)
			}
		}
		if len(clients) == 0 {
			return nil, fmt.Errorf("%w: at least one client is required", ErrUsage)
		}
		return clients, nil
	}
	if prompt == nil {
		return nil, fmt.Errorf("%w: LLMAPI_CLIENTS or interactive client selection is required", ErrUsage)
	}
	clients, err := prompt.SelectMany("Clients", append([]ClientID(nil), supportedClients...))
	if err != nil || len(clients) == 0 {
		return nil, fmt.Errorf("%w: LLMAPI_CLIENTS or interactive client selection is required", ErrUsage)
	}
	for _, client := range clients {
		if !isSupportedClient(client) {
			return nil, fmt.Errorf("%w: unknown client %q", ErrUsage, client)
		}
	}
	return clients, nil
}

func isSupportedClient(client ClientID) bool {
	for _, allowed := range supportedClients {
		if client == allowed {
			return true
		}
	}
	return false
}
func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
func hasControl(value string) bool {
	return strings.IndexFunc(value, func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0
}
