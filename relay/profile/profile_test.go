package profile

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestQuirkRulesDefaults(t *testing.T) {
	r := DefaultQuirkRules()
	assert.Equal(t, "effort", r.ReasoningMode)
	assert.True(t, r.PreserveReasoning, "preserve_reasoning should default to true")
}

func TestQuirkRulesJSONRoundtrip(t *testing.T) {
	original := QuirkRules{
		StripParams:       []string{"temperature", "top_p"},
		ReasoningMode:     "budget",
		EffortMap:         map[string]string{"low": "low", "high": "high"},
		PreserveReasoning: true,
	}

	data, err := common.Marshal(original)
	require.NoError(t, err)

	var decoded QuirkRules
	require.NoError(t, common.Unmarshal(data, &decoded))

	assert.Equal(t, "budget", decoded.ReasoningMode)
	assert.Equal(t, []string{"temperature", "top_p"}, decoded.StripParams)
	assert.Equal(t, map[string]string{"low": "low", "high": "high"}, decoded.EffortMap)
	assert.True(t, decoded.PreserveReasoning)
}

func TestModelProfileJSONRoundtrip(t *testing.T) {
	parallel := false
	original := ModelProfile{
		ModelID:       "gpt-4o",
		DisplayName:   "GPT-4o",
		ContextWindow: 131072,
		MaxOutput:     8192,
		InputModes:    []string{"text", "image"},
		QuirkRules: QuirkRules{
			ReasoningMode:     "effort",
			StripParams:       []string{"temperature"},
			PreserveReasoning: true,
			ParallelToolCalls: &parallel,
		},
		ProfileID:   1,
		ProfileName: "openai-direct",
		BaseURL:     "https://api.openai.com/v1",
		AuthMode:    "api_key",
	}

	data, err := common.Marshal(original)
	require.NoError(t, err)

	var decoded ModelProfile
	require.NoError(t, common.Unmarshal(data, &decoded))

	assert.Equal(t, original.ModelID, decoded.ModelID)
	assert.Equal(t, original.ContextWindow, decoded.ContextWindow)
	assert.Equal(t, original.QuirkRules.ReasoningMode, decoded.QuirkRules.ReasoningMode)
	require.NotNil(t, decoded.QuirkRules.ParallelToolCalls)
	assert.False(t, *decoded.QuirkRules.ParallelToolCalls)
	assert.Equal(t, original.BaseURL, decoded.BaseURL)
}

func TestQuirkRulesOmitsEmpty(t *testing.T) {
	data, err := common.Marshal(QuirkRules{})
	require.NoError(t, err)
	// omitempty fields absent; only non-omitempty bools (preserve_reasoning, bracket_strip) remain
	assert.Equal(t, `{"preserve_reasoning":false,"bracket_strip":false}`, string(data))
}
