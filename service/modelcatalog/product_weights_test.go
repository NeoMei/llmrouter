package modelcatalog

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestProductCreditTargetWeightsPreserveHighestEligiblePriority(t *testing.T) {
	weights := []int{1, 0, 3}
	assert.Equal(t, 0, WeightedTargetIndex(weights, "call-7"))
	assert.Equal(t, 2, WeightedTargetIndex(weights, "call-1"))
	assert.Equal(t, 2, WeightedTargetIndex(weights, "call-4"))
	assert.Equal(t, 2, WeightedTargetIndex(weights, "call-5"))
	assert.Equal(t, 1, WeightedTargetIndex([]int{0, 2, 0}, "call-7"))
}
