package protocol

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/service/modelcatalog"
)

// ProbeStore caches observed capability probe results. It is bounded by TTL:
// success is remembered for 24 hours, failure for 10 minutes. Explicit
// management configuration outranks observed state.
type ProbeStore interface {
	Get(ctx context.Context, key string) (Observation, bool)
	Set(ctx context.Context, key string, observation Observation, ttl time.Duration)
}

// InMemoryProbeStore is a development/single-instance implementation.
// Production deployments should back this with Redis for consistency across
// instances.
type InMemoryProbeStore struct {
	mu    sync.RWMutex
	items map[string]observationEntry
}

type observationEntry struct {
	observation Observation
	expiresAt   time.Time
}

// NewInMemoryProbeStore creates an empty in-memory probe cache.
func NewInMemoryProbeStore() *InMemoryProbeStore {
	return &InMemoryProbeStore{items: make(map[string]observationEntry)}
}

func (s *InMemoryProbeStore) Get(_ context.Context, key string) (Observation, bool) {
	if s == nil {
		return Observation{}, false
	}
	s.mu.RLock()
	entry, ok := s.items[key]
	s.mu.RUnlock()
	if !ok || time.Now().After(entry.expiresAt) {
		return Observation{}, false
	}
	return entry.observation, true
}

func (s *InMemoryProbeStore) Set(_ context.Context, key string, observation Observation, ttl time.Duration) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.items == nil {
		s.items = make(map[string]observationEntry)
	}
	s.items[key] = observationEntry{observation: observation, expiresAt: time.Now().Add(ttl)}
}

// ProbeSuccessTTL is how long a successful capability observation is cached.
const ProbeSuccessTTL = 24 * time.Hour

// ProbeFailureTTL is how long a failed capability observation is cached.
const ProbeFailureTTL = 10 * time.Minute

// ProbeKey builds a stable cache key for a channel/protocol combination.
func ProbeKey(channelID int, wire modelcatalog.WireProtocol) string {
	return fmt.Sprintf("%s/channel/%d", wire, channelID)
}

// RecordProbe stores a probe observation with the appropriate TTL.
func RecordProbe(store ProbeStore, channelID int, wire modelcatalog.WireProtocol, observation Observation) {
	if store == nil {
		return
	}
	ttl := ProbeFailureTTL
	if observation.Healthy {
		ttl = ProbeSuccessTTL
	}
	store.Set(context.Background(), ProbeKey(channelID, wire), observation, ttl)
}
