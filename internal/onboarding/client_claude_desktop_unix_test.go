//go:build !windows

package onboarding

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTrustedBridgeParentOwnerAllowsCurrentUserOrRootOnly(t *testing.T) {
	require.True(t, trustedBridgeParentUID(501, 501))
	require.True(t, trustedBridgeParentUID(0, 501))
	require.False(t, trustedBridgeParentUID(502, 501))
}
