package onboarding

// OnboardingVersion is included in network probes and matches the minimum
// version published by the versioned onboarding manifest.
const OnboardingVersion = "0.1.0"

type probeProtocol string

const (
	probeResponses probeProtocol = "responses"
	probeMessages  probeProtocol = "messages"
	probeChat      probeProtocol = "chat-completions"
	probeMCP       probeProtocol = "mcp"
)

var clientProtocols = map[ClientID]probeProtocol{
	ClientCodexCLI:      probeResponses,
	ClientCodexDesktop:  probeResponses,
	ClientClaudeCode:    probeMessages,
	ClientClaudeDesktop: probeMCP,
	ClientOpenCode:      probeChat,
	ClientContinue:      probeChat,
	ClientAider:         probeChat,
}
