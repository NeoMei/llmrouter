package capability

import (
	"net/http"
	"testing"

	"github.com/QuantumNous/new-api/constant"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolveRequest(t *testing.T) {
	cases := []struct {
		method    string
		path      string
		protocol  Protocol
		operation Operation
	}{
		{http.MethodPost, "/v1/chat/completions", ProtocolOpenAI, OperationChatCreate},
		{http.MethodDelete, "/v1/models/model-1", ProtocolOpenAI, OperationModelDelete},
		{http.MethodPost, "/v1beta/models/gemini-2.5-pro:generateContent", ProtocolGemini, OperationGeminiGenerate},
		{http.MethodPost, "/v1/videos/video-1/remix", ProtocolOpenAI, OperationVideoRemix},
	}

	for _, tc := range cases {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			protocol, operation, err := ResolveRequest(tc.method, tc.path)
			require.NoError(t, err)
			assert.Equal(t, tc.protocol, protocol)
			assert.Equal(t, tc.operation, operation)
		})
	}
}

func TestResolveRequestRejectsUnknownRoute(t *testing.T) {
	_, _, err := ResolveRequest(http.MethodPost, "/v1/unknown")
	require.ErrorContains(t, err, "unknown capability route")
}

func TestNewRegistryRejectsDuplicateDeclaration(t *testing.T) {
	key := Key{ChannelType: constant.ChannelTypeOpenAI, Protocol: ProtocolOpenAI, Operation: OperationChatCreate}
	_, err := NewRegistry(
		[]int{constant.ChannelTypeOpenAI},
		[]Operation{OperationChatCreate},
		[]Declaration{
			{Key: key, Decision: Decision{Supported: true}},
			{Key: key, Decision: Decision{Supported: false, Reason: "duplicate"}},
		},
	)
	require.ErrorContains(t, err, "duplicate")
}

func TestDefaultRegistryCoversEveryChannelAndOperation(t *testing.T) {
	registry := DefaultRegistry()
	require.NoError(t, registry.Ready())

	snapshot := registry.Snapshot()
	for channelType := range constant.ChannelTypeNames {
		if channelType == constant.ChannelTypeUnknown {
			continue
		}
		for _, operation := range AllOperations() {
			protocol := ProtocolForOperation(operation)
			decision, ok := snapshot[Key{ChannelType: channelType, Protocol: protocol, Operation: operation}]
			require.Truef(t, ok, "missing channel=%d operation=%s", channelType, operation)
			if !decision.Supported {
				require.NotEmptyf(t, decision.Reason, "unsupported channel=%d operation=%s needs a reason", channelType, operation)
			}
		}
	}
}

func TestDefaultRegistrySupportsOpenAIChatThroughAnthropicAdaptor(t *testing.T) {
	decision := DefaultRegistry().Resolve(
		constant.ChannelTypeAnthropic,
		ProtocolOpenAI,
		OperationChatCreate,
	)

	require.True(t, decision.Supported, decision.Reason)
}

func TestSnapshotIsDefensiveCopy(t *testing.T) {
	registry := DefaultRegistry()
	key := Key{ChannelType: constant.ChannelTypeOpenAI, Protocol: ProtocolOpenAI, Operation: OperationChatCreate}
	snapshot := registry.Snapshot()
	before := registry.Resolve(key.ChannelType, key.Protocol, key.Operation)
	snapshot[key] = Decision{Supported: !before.Supported}
	assert.Equal(t, before, registry.Resolve(key.ChannelType, key.Protocol, key.Operation))
}
