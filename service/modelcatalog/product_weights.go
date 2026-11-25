package modelcatalog

import (
	"crypto/sha256"
	"encoding/binary"
	"math/rand/v2"
	"strconv"
)

// WeightedTargetIndex chooses from eligible suppliers in one priority band.
// The logical call's stable ID makes the choice independent of Key rotation.
func WeightedTargetIndex(weights []int, callID string) int {
	var total uint64
	for _, weight := range weights {
		if weight > 0 {
			total += uint64(weight)
		}
	}
	if total == 0 {
		return 0
	}
	sum := sha256.Sum256([]byte(callID))
	draw := binary.BigEndian.Uint64(sum[:8]) % total
	for i, weight := range weights {
		if weight <= 0 {
			continue
		}
		w := uint64(weight)
		if draw < w {
			return i
		}
		draw -= w
	}
	return 0
}

// OrderCandidatesByWeight reorders each priority band in items using weighted
// selection without replacement. Items must already be grouped/sorted by
// descending priority. When seed is empty, a per-call random seed is used.
func OrderCandidatesByWeight[T any](items []T, priority func(T) int, weight func(T) int, seed string) {
	if len(items) <= 1 {
		return
	}
	if seed == "" {
		seed = strconv.FormatUint(rand.Uint64(), 36)
	}
	for start := 0; start < len(items); {
		bandPriority := priority(items[start])
		end := start + 1
		for end < len(items) && priority(items[end]) == bandPriority {
			end++
		}
		if end-start > 1 {
			for pos := start; pos < end-1; pos++ {
				weights := make([]int, end-pos)
				for i := pos; i < end; i++ {
					weights[i-pos] = weight(items[i])
				}
				stepSeed := seed
				if pos > 0 {
					stepSeed = seed + ":" + strconv.Itoa(pos)
				}
				pick := pos + WeightedTargetIndex(weights, stepSeed)
				if pick != pos {
					selected := items[pick]
					copy(items[pos+1:pick+1], items[pos:pick])
					items[pos] = selected
				}
			}
		}
		start = end
	}
}
