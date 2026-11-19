package platformbilling

import "github.com/QuantumNous/new-api/pkg/metering"

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

// DeepSeek charges the complete output counter at one rate. Reasoning is an
// optional breakdown, and automatic caching reports reads and ordinary input.
func DeepSeekProductChatTariff(price metering.Price) bool {
	if price.Operation != "chat" || len(price.InputTokenBands) != 1 || len(price.Terms) != 4 {
		return false
	}
	rates := price.InputTokenBands[0].Prices
	for _, key := range []string{"input", "cache_read", "output", "reasoning"} {
		if _, ok := rates[key]; !ok {
			return false
		}
	}
	if rates["output"] != rates["reasoning"] {
		return false
	}
	for _, term := range price.Terms {
		if term.Price != rates[term.Key] {
			return false
		}
	}
	return true
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
	messages, ok := request["messages"].([]any)
	if !ok {
		return ErrInvalid
	}
	for _, item := range messages {
		message, ok := item.(map[string]any)
		if !ok {
			return ErrInvalid
		}
		if message["role"] == "developer" {
			message["role"] = "system"
		}
	}
	return nil
}
