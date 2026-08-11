package onboarding

import (
	"github.com/stretchr/testify/require"
	"testing"
)

func TestApplyManagedBlockReplacesOnlyClientBlock(t *testing.T) {
	input := "export KEEP=1\n# LLMAPI:BEGIN aider\nold\n# LLMAPI:END aider\n"
	got, err := ApplyManagedBlock(input, "aider", "export LLMAPI=1")
	require.NoError(t, err)
	require.Equal(t, "export KEEP=1\n# LLMAPI:BEGIN aider\nexport LLMAPI=1\n# LLMAPI:END aider\n", got)
	got, err = RemoveManagedBlock(got, "aider")
	require.NoError(t, err)
	require.Equal(t, "export KEEP=1\n", got)
}

func TestManagedBlockRejectsMalformedMarkers(t *testing.T) {
	_, err := ApplyManagedBlock("# LLMAPI:BEGIN aider\n", "aider", "x")
	require.ErrorContains(t, err, "unterminated")
}
