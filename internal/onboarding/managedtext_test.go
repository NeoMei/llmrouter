package onboarding

import (
	"github.com/stretchr/testify/require"
	"testing"
)

func TestApplyManagedBlockReplacesOnlyClientBlock(t *testing.T) {
	input := "export KEEP=1\n# LLMROUTER:BEGIN aider\nold\n# LLMROUTER:END aider\n"
	got, err := ApplyManagedBlock(input, "aider", "export LLMROUTER=1")
	require.NoError(t, err)
	require.Equal(t, "export KEEP=1\n# LLMROUTER:BEGIN aider\nexport LLMROUTER=1\n# LLMROUTER:END aider\n", got)
	got, err = RemoveManagedBlock(got, "aider")
	require.NoError(t, err)
	require.Equal(t, "export KEEP=1\n", got)
}

func TestManagedBlockRejectsMalformedMarkers(t *testing.T) {
	_, err := ApplyManagedBlock("# LLMROUTER:BEGIN aider\n", "aider", "x")
	require.ErrorContains(t, err, "unterminated")
}
