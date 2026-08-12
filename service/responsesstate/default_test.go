package responsesstate

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDefaultFallsBackToMemory(t *testing.T) {
	ResetForTest()
	t.Setenv(stateEnabledEnv, "1")
	store, err := Default()
	require.NoError(t, err)
	require.NotNil(t, store)

	// Second call returns the cached instance.
	again, err := Default()
	require.NoError(t, err)
	require.Same(t, store, again)

	ResetForTest()
}

func TestEnabledReflectsEnv(t *testing.T) {
	t.Setenv(stateEnabledEnv, "")
	require.False(t, Enabled())
	t.Setenv(stateEnabledEnv, "1")
	require.True(t, Enabled())
}

func TestReadinessCheckPassesWhenDisabled(t *testing.T) {
	t.Setenv(stateEnabledEnv, "")
	require.NoError(t, ReadinessCheck())
}
