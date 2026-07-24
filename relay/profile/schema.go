package profile

// QuirkRules 定义在 provider_models.quirk_rules JSONB 中的规则
type QuirkRules struct {
	StripParams       []string          `json:"strip_params,omitempty"`
	ReasoningMode     string            `json:"reasoning_mode,omitempty"` // effort/budget/toggle
	EffortMap         map[string]string `json:"effort_map,omitempty"`
	ToolChoice        string            `json:"tool_choice,omitempty"` // auto_only
	PreserveReasoning bool              `json:"preserve_reasoning"`
	BracketStrip      bool              `json:"bracket_strip"`
	ParallelToolCalls *bool             `json:"parallel_tool_calls,omitempty"` // nil = true
}

// ModelProfile 是 loader 返回的完整 profile（model + provider 级别信息）
type ModelProfile struct {
	ModelID       string     `json:"model_id"`
	DisplayName   string     `json:"display_name"`
	ContextWindow int        `json:"context_window"`
	MaxOutput     int        `json:"max_output"`
	InputModes    []string   `json:"input_modes"`
	QuirkRules    QuirkRules `json:"quirk_rules"`

	// 从 provider_profiles JOIN 获取
	ProfileID   int    `json:"profile_id"`
	ProfileName string `json:"profile_name"`
	BaseURL     string `json:"base_url"`
	AuthMode    string `json:"auth_mode"`
}

// DefaultQuirkRules 返回空的 quirk rules（使用默认行为）
func DefaultQuirkRules() QuirkRules {
	return QuirkRules{
		ReasoningMode:     "effort",
		PreserveReasoning: true,
	}
}
