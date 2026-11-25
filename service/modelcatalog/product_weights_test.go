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

func TestOrderCandidatesByWeightPreservesPriorityBandsAndDistributesWeights(t *testing.T) {
	type target struct {
		id       int
		priority int
		weight   int
	}
	base := []target{
		{id: 1, priority: 10, weight: 1},
		{id: 2, priority: 10, weight: 3},
		{id: 3, priority: 5, weight: 2},
		{id: 4, priority: 5, weight: 8},
	}

	firstCounts := map[int]int{}
	for _, seed := range []string{"req-1", "req-2", "req-3", "req-4", "req-5", "req-6", "req-7", "req-8", "req-9", "req-10", "req-11", "req-12"} {
		items := append([]target(nil), base...)
		OrderCandidatesByWeight(items, func(v target) int { return v.priority }, func(v target) int { return v.weight }, seed)
		assert.Equal(t, 10, items[0].priority)
		assert.Equal(t, 10, items[1].priority)
		assert.Equal(t, 5, items[2].priority)
		assert.Equal(t, 5, items[3].priority)
		firstCounts[items[0].id]++
	}
	assert.Positive(t, firstCounts[1])
	assert.Positive(t, firstCounts[2])

	// First choice with an explicit seed matches WeightedTargetIndex.
	items := append([]target(nil), base...)
	OrderCandidatesByWeight(items, func(v target) int { return v.priority }, func(v target) int { return v.weight }, "call-7")
	assert.Equal(t, 1, items[0].id)
}
