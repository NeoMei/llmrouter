package protocol

import (
	"testing"

	"github.com/QuantumNous/new-api/service/modelcatalog"
	"github.com/stretchr/testify/require"
)

func TestResolvePrefersProvenNativeResponses(t *testing.T) {
	decision, err := Resolve(ResolveInput{
		Requested: []modelcatalog.WireProtocol{modelcatalog.WireResponses, modelcatalog.WireOpenAIChat},
		Declared:  map[modelcatalog.WireProtocol]bool{modelcatalog.WireResponses: true, modelcatalog.WireOpenAIChat: true},
		Observed:  map[modelcatalog.WireProtocol]Observation{modelcatalog.WireResponses: {Healthy: true}},
	})
	require.NoError(t, err)
	require.Equal(t, modelcatalog.WireResponses, decision.Wire)
}

func TestResolveSkipsUnhealthyObservedWire(t *testing.T) {
	decision, err := Resolve(ResolveInput{
		Requested: []modelcatalog.WireProtocol{modelcatalog.WireResponses, modelcatalog.WireOpenAIChat},
		Declared:  map[modelcatalog.WireProtocol]bool{modelcatalog.WireResponses: true, modelcatalog.WireOpenAIChat: true},
		Observed:  map[modelcatalog.WireProtocol]Observation{modelcatalog.WireResponses: {Healthy: false}},
	})
	require.NoError(t, err)
	require.Equal(t, modelcatalog.WireOpenAIChat, decision.Wire)
}

func TestResolveRejectsUndeclaredPreferredWire(t *testing.T) {
	_, err := Resolve(ResolveInput{
		Requested: []modelcatalog.WireProtocol{modelcatalog.WireResponses},
		Declared:  map[modelcatalog.WireProtocol]bool{modelcatalog.WireOpenAIChat: true},
		Observed:  map[modelcatalog.WireProtocol]Observation{},
	})
	require.Error(t, err)
}
