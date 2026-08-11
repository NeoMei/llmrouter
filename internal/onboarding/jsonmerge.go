package onboarding

import (
	"fmt"
	"strings"

	"github.com/QuantumNous/new-api/common"
)

// MergeJSONObject replaces value at keys while retaining every unrelated object field.
func MergeJSONObject(input []byte, keys []string, value any) ([]byte, error) {
	if len(keys) == 0 {
		return nil, fmt.Errorf("%w: JSON key path is required", ErrUsage)
	}
	root := make(map[string]any)
	if len(strings.TrimSpace(string(input))) != 0 {
		if err := common.Unmarshal(input, &root); err != nil {
			return nil, fmt.Errorf("%w: invalid JSON object", ErrUsage)
		}
		if root == nil {
			return nil, fmt.Errorf("%w: JSON root is not an object", ErrUsage)
		}
	}
	current := root
	for _, key := range keys[:len(keys)-1] {
		if key == "" || hasControl(key) {
			return nil, fmt.Errorf("%w: invalid JSON key", ErrUsage)
		}
		next, exists := current[key]
		if !exists {
			child := make(map[string]any)
			current[key] = child
			current = child
			continue
		}
		child, ok := next.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("%w: JSON path %q is not an object", ErrConflict, key)
		}
		current = child
	}
	last := keys[len(keys)-1]
	if last == "" || hasControl(last) {
		return nil, fmt.Errorf("%w: invalid JSON key", ErrUsage)
	}
	current[last] = value
	output, err := common.Marshal(root)
	if err != nil {
		return nil, fmt.Errorf("%w: cannot encode JSON object", ErrUsage)
	}
	return output, nil
}
