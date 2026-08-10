package protocol

import (
	"context"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/service/modelcatalog"
	"github.com/stretchr/testify/require"
)

func TestProbeStoreTTLExpires(t *testing.T) {
	store := NewInMemoryProbeStore()
	store.Set(context.Background(), "x", Observation{Healthy: true}, 1*time.Millisecond)
	time.Sleep(2 * time.Millisecond)
	_, ok := store.Get(context.Background(), "x")
	require.False(t, ok)
}

func TestProbeStoreCachesHealthyObservation(t *testing.T) {
	store := NewInMemoryProbeStore()
	RecordProbe(store, 7, modelcatalog.WireResponses, Observation{Healthy: true, ObservedAt: time.Now()})
	obs, ok := store.Get(context.Background(), ProbeKey(7, modelcatalog.WireResponses))
	require.True(t, ok)
	require.True(t, obs.Healthy)
}
