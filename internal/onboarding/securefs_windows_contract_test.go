//go:build windows

package onboarding

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestWindowsRefusesSensitiveStateWithoutACLSupport(t *testing.T) {
	_, err := BeginTransaction(t.TempDir())
	require.ErrorIs(t, err, ErrUnsafe)
}
