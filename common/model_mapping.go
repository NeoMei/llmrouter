package common

import "errors"

// ResolveModelMapping follows the legacy relay mapping chain. Self-maps are
// terminal, empty targets are absent, and cycles never establish an identity.
func ResolveModelMapping(model string, mapping map[string]string) (string, error) {
	current := model
	visited := map[string]bool{current: true}
	for {
		next := mapping[current]
		if next == "" || next == current {
			return current, nil
		}
		if visited[next] {
			return "", errors.New("model_mapping_contains_cycle")
		}
		visited[next] = true
		current = next
	}
}
