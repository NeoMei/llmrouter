package service

import (
	"math"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/require"
)

func TestViolationFeeQuotaSaturatesInsteadOfWrapping(t *testing.T) {
	require.Equal(t, common.MaxQuota, calcViolationFeeQuota(1e30, 1))
	require.NotPanics(t, func() { require.Zero(t, calcViolationFeeQuota(math.NaN(), 1)) })
	require.NotPanics(t, func() { require.Zero(t, calcViolationFeeQuota(math.Inf(1), 1)) })
}
