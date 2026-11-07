package modelcatalog

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOperationOnlyCatalogAllowsEmptyPreferredWires(t *testing.T) {
	for _, operations := range [][]string{
		{"images.generations.create", "dashscope.images.generate"},
		{"images.edits.create"}, {"embeddings.create"}, {"multimodal.embeddings.create"},
		{"rerank.create"}, {"audio.speech.create"}, {"audio.transcriptions.create"}, {"ocr.jobs.create"},
		{"bailian.audio.speech.create"},
	} {
		t.Run(operations[0], func(t *testing.T) {
			routes := []ClientModelRoute{{Id: 1, PublicId: "operation-only", Client: "all", Enabled: true, Metadata: `{"operations":` + encodeStrings(operations) + `}`}}
			targets := []ClientModelTarget{{Id: 1, RouteId: 1, ChannelId: 1, UpstreamModel: "operation-only", BillingModel: "operation-only", PreferredWires: `[]`, Enabled: true, Weight: 1, Groups: []string{"default"}}}
			routes = append(routes, ClientModelRoute{Id: 2, PublicId: "unrelated-chat", Client: "all", Enabled: true, Metadata: `{"operations":["chat.completions.create"]}`})
			targets = append(targets, ClientModelTarget{Id: 2, RouteId: 2, ChannelId: 2, UpstreamModel: "chat", BillingModel: "chat", PreferredWires: `["openai_chat"]`, Enabled: true, Weight: 1, Groups: []string{"default"}})
			catalog, err := NewCatalog(routes, targets)
			require.NoError(t, err)
			catalog = catalog.WithAuthorizer(testAuthorizer{})
			listed, err := catalog.List(context.Background(), 11, []string{"default"}, "superwagie")
			require.NoError(t, err)
			require.Len(t, listed, 2)
			require.Equal(t, "unrelated-chat", listed[1].PublicID)
			require.Equal(t, []WireProtocol{WireOpenAIChat}, listed[1].PreferredWires)
			require.Empty(t, listed[0].PreferredWires)
			require.Equal(t, operations, listed[0].Operations)
			resolved, err := catalog.ResolveCandidates(context.Background(), 11, []string{"default"}, "operation-only", "superwagie")
			require.NoError(t, err)
			require.Len(t, resolved, 1)
			require.Empty(t, resolved[0].PreferredWires)
			require.Equal(t, operations, resolved[0].Capabilities.Operations)
		})
	}
}

func TestEmptyPreferredWiresRequiresExplicitSupportedOperationOnlyRoute(t *testing.T) {
	for _, metadata := range []string{`{}`, `{"operations":[]}`, `{"operations":["unknown.operation"]}`, `{"operations":["chat.completions.create"]}`, `{"operations":["images.generations.create","chat.completions.create"]}`, `{"operations":["responses.create"]}`, `{"operations":["claude.messages.create"]}`, `{"operations":["gemini.generate"]}`, `{"operations":["realtime.connect"]}`} {
		t.Run(metadata, func(t *testing.T) {
			_, err := NewCatalog([]ClientModelRoute{{Id: 1, PublicId: "invalid", Enabled: true, Metadata: metadata}}, []ClientModelTarget{{Id: 1, RouteId: 1, ChannelId: 1, UpstreamModel: "invalid", BillingModel: "invalid", PreferredWires: `[]`, Enabled: true, Weight: 1}})
			require.Error(t, err)
		})
	}
	_, err := NewCatalog([]ClientModelRoute{{Id: 1, PublicId: "image", Enabled: true, Metadata: `{"operations":["images.generations.create"]}`}}, []ClientModelTarget{{Id: 1, RouteId: 1, ChannelId: 1, UpstreamModel: "image", BillingModel: "image", PreferredWires: `["invented_image_wire"]`, Enabled: true, Weight: 1}})
	require.ErrorContains(t, err, "unknown wire protocol")
}
