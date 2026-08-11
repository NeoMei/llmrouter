package onboarding

import (
	"testing"

	"github.com/stretchr/testify/require"
)

type promptStub struct {
	token   string
	clients []ClientID
	err     error
}

func (p promptStub) ReadHidden(string) (string, error)                 { return p.token, p.err }
func (p promptStub) SelectMany(string, []ClientID) ([]ClientID, error) { return p.clients, p.err }

func TestResolveOptionsPrecedence(t *testing.T) {
	options, err := ResolveOptions(Inputs{
		Args:       []string{"--server-url", "https://flag.example/v1", "--clients", "aider,codex-cli", "--model", "flag-model", "--smoke"},
		Env:        map[string]string{"LLMAPI_SERVER_URL": "https://env.example", "LLMAPI_TOKEN": "env-token", "LLMAPI_CLIENTS": "continue", "LLMAPI_MODEL": "env-model"},
		PageOrigin: "https://page.example",
		Prompt:     promptStub{token: "prompt-token", clients: []ClientID{ClientOpenCode}},
	})
	require.NoError(t, err)
	require.Equal(t, "https://flag.example", options.ServerURL.String())
	require.Equal(t, "env-token", options.Token.Reveal())
	require.Equal(t, "[REDACTED]", options.Token.String())
	require.Equal(t, []ClientID{ClientAider, ClientCodexCLI}, options.Clients)
	require.Equal(t, "flag-model", options.Model)
	require.True(t, options.Smoke)
}

func TestResolveOptionsDoesNotAcceptTokenFlag(t *testing.T) {
	_, err := ResolveOptions(Inputs{Args: []string{"--token", "leak"}})
	require.ErrorContains(t, err, "LLMAPI_TOKEN or hidden prompt")
}

func TestResolveOptionsRequiresInteractiveInputsWithoutPrompt(t *testing.T) {
	_, err := ResolveOptions(Inputs{Env: map[string]string{"LLMAPI_SERVER_URL": "https://stage.example"}})
	require.ErrorContains(t, err, "LLMAPI_TOKEN")
}

func TestResolveOptionsRejectsUnknownClientsAndControls(t *testing.T) {
	_, err := ResolveOptions(Inputs{Args: []string{"--server-url", "https://stage.example", "--clients", "unknown"}, Env: map[string]string{"LLMAPI_TOKEN": "token"}})
	require.ErrorContains(t, err, "unknown client")
	_, err = ResolveOptions(Inputs{Args: []string{"--server-url", "https://stage.example\n.evil", "--clients", "aider"}, Env: map[string]string{"LLMAPI_TOKEN": "token"}})
	require.Error(t, err)
}
