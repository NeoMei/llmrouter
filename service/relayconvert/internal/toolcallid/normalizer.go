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

// Normalizer owns the tool-call ID mapping for one conversion request.
// It is deliberately not safe for concurrent use: a request conversion is
// sequential, and keeping the state request-local prevents cross-request races.
type Normalizer struct {
	prefix  string
	forward map[string]string
	reverse map[string]string
	seq     uint64
}

func New(prefix string) *Normalizer {
	return &Normalizer{
		prefix:  normalizePrefix(prefix),
		forward: make(map[string]string),
		reverse: make(map[string]string),
	}
}

// Normalize preserves valid source IDs and deterministically replaces invalid
// ones. Empty IDs represent distinct calls, so each empty occurrence receives
// a deterministic request-local ordinal.
func (n *Normalizer) Normalize(sourceID string) string {
	if sourceID != "" {
		if normalized, ok := n.forward[sourceID]; ok {
			return normalized
		}
	}

	seed := sourceID
	if seed == "" {
		n.seq++
		seed = "empty:" + strconv.FormatUint(n.seq, 10)
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

// Lookup resolves a non-empty source ID without creating a new mapping.
func (n *Normalizer) Lookup(sourceID string) (string, bool) {
	if sourceID == "" {
		return "", false
	}
	normalized, ok := n.forward[sourceID]
	return normalized, ok
}

// Source performs the reverse lookup for diagnostics and protocol bridges.
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
