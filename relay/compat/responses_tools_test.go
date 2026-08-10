package compat

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNormalizeResponsesTools(t *testing.T) {
	input := json.RawMessage(`[{"type":"additional_tools","tools":[{"type":"namespace","name":"functions","tools":[{"type":"custom","name":"exec","format":{"type":"grammar"}}]}]}]`)
	got, err := NormalizeResponsesTools(input, nil)
	require.NoError(t, err)
	require.JSONEq(t, `[]`, string(got.Input))
	require.Contains(t, string(got.Tools), `"name":"functions.exec"`)
	require.Contains(t, got.CustomWireNames, "functions.exec")
}

func TestNormalizeResponsesToolsPreservesToolSearch(t *testing.T) {
	tools := json.RawMessage(`[{"type":"tool_search","name":"load_tools"}]`)
	got, err := NormalizeResponsesTools(nil, tools)
	require.NoError(t, err)
	require.True(t, got.RequiresToolSearch)
}

func TestNormalizeResponsesToolsMergesExistingTools(t *testing.T) {
	tools := json.RawMessage(`[{"type":"function","name":"weather"}]`)
	input := json.RawMessage(`[{"type":"additional_tools","tools":[{"type":"namespace","name":"functions","tools":[{"type":"custom","name":"exec"}]}]}]`)
	got, err := NormalizeResponsesTools(input, tools)
	require.NoError(t, err)
	require.Contains(t, string(got.Tools), `"name":"weather"`)
	require.Contains(t, string(got.Tools), `"name":"functions.exec"`)
}

func TestNormalizeResponsesToolsRejectsDuplicateNames(t *testing.T) {
	tools := json.RawMessage(`[{"type":"custom","name":"functions.exec"}]`)
	input := json.RawMessage(`[{"type":"additional_tools","tools":[{"type":"namespace","name":"functions","tools":[{"type":"custom","name":"exec"}]}]}]`)
	_, err := NormalizeResponsesTools(input, tools)
	require.Error(t, err)
}
