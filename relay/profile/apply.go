package profile

import (
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
)

// ApplyRules applies the profile's quirk_rules to a Chat Completions request in-place.
func ApplyRules(req *dto.GeneralOpenAIRequest, p *ModelProfile) {
	rules := p.QuirkRules

	for _, param := range rules.StripParams {
		switch param {
		case "temperature":
			req.Temperature = nil
		case "top_p":
			req.TopP = nil
		case "presence_penalty":
			req.PresencePenalty = nil
		case "frequency_penalty":
			req.FrequencyPenalty = nil
		case "reasoning_effort":
			req.ReasoningEffort = ""
		}
	}

	applyReasoningMode(req, rules)

	if rules.ToolChoice == "auto_only" && req.ToolChoice != nil {
		if normalizeToolChoice(req.ToolChoice) != "none" {
			req.ToolChoice = "auto"
		}
	}

	if rules.ParallelToolCalls != nil && !*rules.ParallelToolCalls && req.ParallelTooCalls != nil {
		// ponytail: field name ParallelTooCalls is an upstream typo; json tag is correct
		disabled := false
		req.ParallelTooCalls = &disabled
	}
}

// applyReasoningMode maps the client's reasoning_effort into the shape the provider expects.
func applyReasoningMode(req *dto.GeneralOpenAIRequest, rules QuirkRules) {
	if req.ReasoningEffort == "" {
		return
	}

	mapped := req.ReasoningEffort
	if v, ok := rules.EffortMap[req.ReasoningEffort]; ok {
		mapped = v
	}

	switch rules.ReasoningMode {
	case "budget":
		// ponytail: thinking_budget mode (DeepSeek-R1 etc.), proportional to max_tokens; 32768 default
		maxOut := 32768
		if req.MaxTokens != nil && *req.MaxTokens > 0 {
			maxOut = int(*req.MaxTokens)
		}
		budget := thinkingBudgetForEffort(mapped, maxOut)
		reasoningJSON, _ := common.Marshal(map[string]any{"budget_tokens": budget})
		req.Reasoning = reasoningJSON
		req.ReasoningEffort = ""
	case "toggle":
		// ponytail: GLM 5/5.1, MiMo — thinking:{type} on/off switch
		toggleType := "enabled"
		if mapped == "none" || mapped == "minimal" {
			toggleType = "disabled"
		}
		thinkingJSON, _ := common.Marshal(map[string]string{"type": toggleType})
		req.Reasoning = thinkingJSON
		req.ReasoningEffort = ""
	default:
		req.ReasoningEffort = mapped
	}
}

func thinkingBudgetForEffort(effort string, maxOutputTokens int) int {
	fractions := map[string]float64{
		"low": 0.20, "medium": 0.50, "high": 0.75,
		"xhigh": 0.90, "max": 1.0,
	}
	f, ok := fractions[effort]
	if !ok {
		return 0
	}
	result := int(float64(maxOutputTokens) * f)
	if result < 1 {
		return 1
	}
	return result
}

func normalizeToolChoice(tc any) string {
	if s, ok := tc.(string); ok {
		return s
	}
	return "auto"
}
