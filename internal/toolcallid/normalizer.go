package toolcallid

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
)

const (
	maxIDLength = 64
	hashLength  = 24
)

// Normalizer owns tool-call ID mapping and call registration for one request.
type Normalizer struct {
	prefix            string
	forward           map[string]string
	reverse           map[string]string
	registeredSources map[string]struct{}
	registeredIDs     map[string]struct{}
	seq               uint64
}

func New(prefix string) *Normalizer {
	return &Normalizer{
		prefix:            normalizePrefix(prefix),
		forward:           make(map[string]string),
		reverse:           make(map[string]string),
		registeredSources: make(map[string]struct{}),
		registeredIDs:     make(map[string]struct{}),
	}
}

// Normalize preserves valid source IDs and deterministically replaces invalid
// ones. Empty IDs hash the decimal request-local ordinal ("1", "2", ...).
func (n *Normalizer) Normalize(sourceID string) string {
	if sourceID != "" {
		if normalized, ok := n.forward[sourceID]; ok {
			return normalized
		}
	}
	seed := sourceID
	if seed == "" {
		n.seq++
		seed = strconv.FormatUint(n.seq, 10)
	}
	candidate := sourceID
	if !validID(candidate) {
		sum := sha256.Sum256([]byte(seed))
		candidate = n.prefix + "_" + hex.EncodeToString(sum[:])[:hashLength]
	}
	candidate = n.available(candidate, sourceID)
	if sourceID != "" {
		n.forward[sourceID] = candidate
	}
	n.reverse[candidate] = sourceID
	return candidate
}

// RegisterCall registers a distinct call. A repeated non-empty source ID is
// ambiguous and rejected, while empty IDs receive independent ordinals.
func (n *Normalizer) RegisterCall(sourceID string) (string, error) {
	if sourceID != "" {
		if _, exists := n.registeredSources[sourceID]; exists {
			return "", fmt.Errorf("duplicate tool call id %q", sourceID)
		}
	}
	normalized := n.Normalize(sourceID)
	if _, exists := n.registeredIDs[normalized]; exists {
		return "", fmt.Errorf("duplicate normalized tool call id %q", normalized)
	}
	if sourceID != "" {
		n.registeredSources[sourceID] = struct{}{}
	}
	n.registeredIDs[normalized] = struct{}{}
	return normalized, nil
}

func (n *Normalizer) Lookup(sourceID string) (string, bool) {
	if sourceID == "" {
		return "", false
	}
	normalized, ok := n.forward[sourceID]
	return normalized, ok
}

// LookupResult resolves only a non-empty source ID previously registered as a call.
func (n *Normalizer) LookupResult(sourceID string) (string, bool) {
	normalized, ok := n.Lookup(sourceID)
	if !ok {
		return "", false
	}
	_, registered := n.registeredIDs[normalized]
	return normalized, registered
}

func (n *Normalizer) Source(normalizedID string) (string, bool) {
	source, ok := n.reverse[normalizedID]
	return source, ok
}

func (n *Normalizer) available(base, source string) string {
	if owner, exists := n.reverse[base]; !exists || owner == source {
		return base
	}
	for collision := 2; ; collision++ {
		suffix := "_" + strconv.Itoa(collision)
		limit := maxIDLength - len(suffix)
		candidate := base
		if len(candidate) > limit {
			candidate = candidate[:limit]
		}
		candidate += suffix
		if owner, exists := n.reverse[candidate]; !exists || owner == source {
			return candidate
		}
	}
}

func validID(id string) bool {
	if id == "" || len(id) > maxIDLength {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_' || c == '-' {
			continue
		}
		return false
	}
	return true
}

func normalizePrefix(prefix string) string {
	var safe strings.Builder
	for i := 0; i < len(prefix) && safe.Len() < maxIDLength-hashLength-1; i++ {
		c := prefix[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_' || c == '-' {
			safe.WriteByte(c)
		}
	}
	if safe.Len() == 0 {
		return "call"
	}
	return safe.String()
}

// ParseObjectArguments decodes exactly one JSON object, rejects duplicate keys,
// and preserves numeric lexemes as json.Number values.
func ParseObjectArguments(arguments string) (map[string]any, error) {
	if strings.TrimSpace(arguments) == "" {
		return nil, fmt.Errorf("function arguments must be a JSON object")
	}
	var object map[string]any
	if err := common.UnmarshalUnique([]byte(arguments), &object); err != nil {
		return nil, fmt.Errorf("invalid function arguments: %w", err)
	}
	if object == nil || common.GetJsonType([]byte(arguments)) != "object" {
		return nil, fmt.Errorf("function arguments must be a JSON object")
	}
	return object, nil
}
