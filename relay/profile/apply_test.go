package profile

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func ptrFloat(f float64) *float64 { return &f }
func ptrUint(u uint) *uint        { return &u }
func ptrBool(b bool) *bool        { return &b }

func TestApplyRules_StripParams(t *testing.T) {
	req := &dto.GeneralOpenAIRequest{
		Temperature:      ptrFloat(0.7),
		TopP:             ptrFloat(0.9),
		PresencePenalty:  ptrFloat(1.0),
		FrequencyPenalty: ptrFloat(0.5),
		ReasoningEffort:  "high",
	}
	p := &ModelProfile{QuirkRules: QuirkRules{
		StripParams: []string{"temperature", "top_p", "presence_penalty", "frequency_penalty", "reasoning_effort"},
	}}

	ApplyRules(req, p)

	assert.Nil(t, req.Temperature)
	assert.Nil(t, req.TopP)
	assert.Nil(t, req.PresencePenalty)
	assert.Nil(t, req.FrequencyPenalty)
	assert.Empty(t, req.ReasoningEffort)
}

func TestApplyRules_StripParamsNoOp(t *testing.T) {
	temp := 0.7
	req := &dto.GeneralOpenAIRequest{Temperature: &temp}
	p := &ModelProfile{QuirkRules: QuirkRules{StripParams: []string{"top_p"}}}

	ApplyRules(req, p)

	require.NotNil(t, req.Temperature)
	assert.Equal(t, 0.7, *req.Temperature)
}

func TestApplyRules_EffortMapDefault(t *testing.T) {
	req := &dto.GeneralOpenAIRequest{ReasoningEffort: "medium"}
	p := &ModelProfile{QuirkRules: QuirkRules{
		ReasoningMode: "effort",
		EffortMap:     map[string]string{"medium": "high"},
	}}

	ApplyRules(req, p)

	assert.Equal(t, "high", req.ReasoningEffort)
	assert.Nil(t, req.Reasoning)
}

func TestApplyRules_EffortMapUnknownKeyKeepsOriginal(t *testing.T) {
	req := &dto.GeneralOpenAIRequest{ReasoningEffort: "low"}
	p := &ModelProfile{QuirkRules: QuirkRules{
		ReasoningMode: "effort",
		EffortMap:     map[string]string{"medium": "high"},
	}}

	ApplyRules(req, p)

	assert.Equal(t, "low", req.ReasoningEffort)
}

func TestApplyRules_ReasoningSkippedWhenNoEffort(t *testing.T) {
	req := &dto.GeneralOpenAIRequest{ReasoningEffort: ""}
	p := &ModelProfile{QuirkRules: QuirkRules{ReasoningMode: "budget"}}

	ApplyRules(req, p)

	assert.Empty(t, req.ReasoningEffort)
	assert.Nil(t, req.Reasoning)
}

func TestApplyRules_BudgetMode(t *testing.T) {
	req := &dto.GeneralOpenAIRequest{ReasoningEffort: "medium", MaxTokens: ptrUint(10000)}
	p := &ModelProfile{QuirkRules: QuirkRules{ReasoningMode: "budget"}}

	ApplyRules(req, p)

	assert.Empty(t, req.ReasoningEffort, "reasoning_effort consumed into reasoning JSON")
	require.NotNil(t, req.Reasoning)

	var decoded map[string]any
	require.NoError(t, common.Unmarshal(req.Reasoning, &decoded))
	assert.EqualValues(t, 5000, decoded["budget_tokens"]) // 0.50 * 10000
}

func TestApplyRules_BudgetModeDefaultMax(t *testing.T) {
	req := &dto.GeneralOpenAIRequest{ReasoningEffort: "high"}
	p := &ModelProfile{QuirkRules: QuirkRules{ReasoningMode: "budget"}}

	ApplyRules(req, p)

	require.NotNil(t, req.Reasoning)
	var decoded map[string]any
	require.NoError(t, common.Unmarshal(req.Reasoning, &decoded))
	assert.EqualValues(t, 24576, decoded["budget_tokens"]) // 0.75 * 32768
}

func TestApplyRules_ToggleModeEnabled(t *testing.T) {
	req := &dto.GeneralOpenAIRequest{ReasoningEffort: "high"}
	p := &ModelProfile{QuirkRules: QuirkRules{ReasoningMode: "toggle"}}

	ApplyRules(req, p)

	require.NotNil(t, req.Reasoning)
	var decoded map[string]string
	require.NoError(t, common.Unmarshal(req.Reasoning, &decoded))
	assert.Equal(t, "enabled", decoded["type"])
}

func TestApplyRules_ToggleModeDisabled(t *testing.T) {
	for _, effort := range []string{"none", "minimal"} {
		req := &dto.GeneralOpenAIRequest{ReasoningEffort: effort}
		p := &ModelProfile{QuirkRules: QuirkRules{ReasoningMode: "toggle"}}

		ApplyRules(req, p)

		require.NotNil(t, req.Reasoning, "effort=%s", effort)
		var decoded map[string]string
		require.NoError(t, common.Unmarshal(req.Reasoning, &decoded))
		assert.Equal(t, "disabled", decoded["type"])
	}
}

func TestApplyRules_ToolChoiceAutoOnlyString(t *testing.T) {
	req := &dto.GeneralOpenAIRequest{ToolChoice: "required"}
	p := &ModelProfile{QuirkRules: QuirkRules{ToolChoice: "auto_only"}}

	ApplyRules(req, p)

	assert.Equal(t, "auto", req.ToolChoice)
}

func TestApplyRules_ToolChoiceAutoOnlyKeepsNone(t *testing.T) {
	req := &dto.GeneralOpenAIRequest{ToolChoice: "none"}
	p := &ModelProfile{QuirkRules: QuirkRules{ToolChoice: "auto_only"}}

	ApplyRules(req, p)

	assert.Equal(t, "none", req.ToolChoice)
}

func TestApplyRules_ToolChoiceAutoOnlyFlattensObject(t *testing.T) {
	req := &dto.GeneralOpenAIRequest{ToolChoice: map[string]any{"type": "function", "function": map[string]any{"name": "get_weather"}}}
	p := &ModelProfile{QuirkRules: QuirkRules{ToolChoice: "auto_only"}}

	ApplyRules(req, p)

	assert.Equal(t, "auto", req.ToolChoice)
}

func TestApplyRules_ToolChoiceAutoOnlyNilUnchanged(t *testing.T) {
	req := &dto.GeneralOpenAIRequest{}
	p := &ModelProfile{QuirkRules: QuirkRules{ToolChoice: "auto_only"}}

	ApplyRules(req, p)

	assert.Nil(t, req.ToolChoice)
}

func TestApplyRules_ParallelToolCallsDisabled(t *testing.T) {
	req := &dto.GeneralOpenAIRequest{ParallelTooCalls: ptrBool(true)}
	p := &ModelProfile{QuirkRules: QuirkRules{ParallelToolCalls: ptrBool(false)}}

	ApplyRules(req, p)

	require.NotNil(t, req.ParallelTooCalls)
	assert.False(t, *req.ParallelTooCalls)
}

func TestApplyRules_ParallelToolCallsNotSetWhenAbsent(t *testing.T) {
	req := &dto.GeneralOpenAIRequest{}
	p := &ModelProfile{QuirkRules: QuirkRules{ParallelToolCalls: ptrBool(false)}}

	ApplyRules(req, p)

	assert.Nil(t, req.ParallelTooCalls, "must not inject parallel_tool_calls the client never sent")
}

func TestApplyRules_ParallelToolCallsAllowedLeftAlone(t *testing.T) {
	req := &dto.GeneralOpenAIRequest{ParallelTooCalls: ptrBool(true)}
	p := &ModelProfile{QuirkRules: QuirkRules{ParallelToolCalls: ptrBool(true)}}

	ApplyRules(req, p)

	require.NotNil(t, req.ParallelTooCalls)
	assert.True(t, *req.ParallelTooCalls)
}

func TestThinkingBudgetForEffort(t *testing.T) {
	tests := []struct {
		effort    string
		maxTokens int
		expected  int
	}{
		{"low", 32768, 6553},
		{"medium", 32768, 16384},
		{"high", 32768, 24576},
		{"xhigh", 32768, 29491},
		{"max", 32768, 32768},
		{"unknown", 32768, 0},
		{"low", 4, 1}, // clamp to minimum 1
	}
	for _, tt := range tests {
		got := thinkingBudgetForEffort(tt.effort, tt.maxTokens)
		assert.Equal(t, tt.expected, got, "effort=%s max=%d", tt.effort, tt.maxTokens)
	}
}

func TestNormalizeToolChoice(t *testing.T) {
	assert.Equal(t, "auto", normalizeToolChoice("auto"))
	assert.Equal(t, "none", normalizeToolChoice("none"))
	assert.Equal(t, "auto", normalizeToolChoice(map[string]any{"type": "function"}))
	assert.Equal(t, "auto", normalizeToolChoice(42))
}
