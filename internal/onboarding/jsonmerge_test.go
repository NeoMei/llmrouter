package onboarding

import (
	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestMergeJSONObjectPreservesSiblings(t *testing.T) {
	got, err := MergeJSONObject([]byte(`{"existing":true,"nested":{"keep":"yes"}}`), []string{"nested", "llmapi"}, map[string]any{"url": "https://stage.example"})
	require.NoError(t, err)
	var decoded map[string]any
	require.NoError(t, common.Unmarshal(got, &decoded))
	require.Equal(t, true, decoded["existing"])
	require.Equal(t, "yes", decoded["nested"].(map[string]any)["keep"])
	require.Equal(t, "https://stage.example", decoded["nested"].(map[string]any)["llmapi"].(map[string]any)["url"])
}

func TestMergeJSONObjectRejectsNonObjectBoundary(t *testing.T) {
	_, err := MergeJSONObject([]byte(`{"nested":false}`), []string{"nested", "llmapi"}, true)
	require.ErrorContains(t, err, "not an object")
}

func TestMergeJSONObjectRejectsDuplicateKeysRecursivelyAndTrailingJSON(t *testing.T) {
	for _, input := range []string{
		`{"mcpServers":{},"mcpServers":{}}`,
		`{"mcpServers":{"existing":{"command":"a","command":"b"}}}`,
		`{"mcpServers":{}} {}`,
	} {
		_, err := MergeJSONObject([]byte(input), []string{"mcpServers", "llmapi"}, map[string]any{"command": "bridge"})
		require.Error(t, err, input)
	}
}
