package modelcatalog

import (
	"crypto/sha256"
	"encoding/binary"
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
