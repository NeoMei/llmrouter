package platformbilling

const deepSeekProductChatEndpoint = "https://api.deepseek.com/v1/chat/completions"

// These are the current direct DeepSeek Chat models with a documented tariff.
// Its remaining historical names are not assigned an unverified price.
func deepSeekProductChatUpstream(model string) bool {
	switch model {
	case "deepseek-flash", "deepseek-v4-flash", "deepseek-v4-flash-vision-exp", "deepseek-v4-pro":
		return true
	}
	return false
}

func normalizeDeepSeekProductChat(request map[string]any) error {
	if cap, exists := request["max_completion_tokens"]; exists {
		// DeepSeek uses max_tokens for the same bounded completion counter.
		// Reject competing limits so the price and transmitted cap agree.
		if _, duplicate := request["max_tokens"]; duplicate {
			return ErrInvalid
		}
		request["max_tokens"] = cap
		delete(request, "max_completion_tokens")
	}
	for _, item := range request["messages"].([]any) {
		message := item.(map[string]any)
		if message["role"] == "developer" {
			message["role"] = "system"
		}
	}
	return nil
}
