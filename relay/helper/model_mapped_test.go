package helper

import (
	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"net/http/httptest"
	"testing"
)

func TestModelMappingIdentity(t *testing.T) {
	for _, tc := range []struct {
		name, mapping, want string
		invalid             bool
	}{
		{"absent", "", "alias", false},
		{"self", `{"alias":"alias"}`, "alias", false},
		{"chain", `{"alias":"middle","middle":"canonical"}`, "canonical", false},
		{"terminal self", `{"alias":"middle","middle":"canonical","canonical":"canonical"}`, "canonical", false},
		{"empty target", `{"alias":""}`, "alias", false},
		{"cycle", `{"alias":"middle","middle":"alias"}`, "", true},
		{"malformed", `{"alias":1}`, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Set("model_mapping", tc.mapping)
			info := &relaycommon.RelayInfo{OriginModelName: "alias", ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "alias"}}
			req := &dto.AudioRequest{Model: "alias"}
			err := ModelMappedHelper(c, info, req)
			if tc.invalid {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, info.UpstreamModelName)
			assert.Equal(t, tc.want, req.Model)
			assert.Equal(t, "alias", info.OriginModelName)
		})
	}
	t.Run("catalog authority", func(t *testing.T) {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Set("model_mapping", `{"alias":"middle","middle":"canonical"}`)
		info := &relaycommon.RelayInfo{OriginModelName: "alias", ResolvedModelName: "middle", RouteCandidates: []relaycommon.RouteCandidate{{UpstreamModel: "middle"}}, ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "middle"}}
		req := &dto.AudioRequest{Model: "middle"}
		require.NoError(t, ModelMappedHelper(c, info, req))
		assert.Equal(t, "middle", req.Model)
		assert.Equal(t, "middle", info.UpstreamModelName)
	})
}

func TestModelMappingPreservesCompactLegacyIdentity(t *testing.T) {
	for _, tc := range []struct {
		name, mapping, want string
		mapped              bool
	}{
		{"absent", "", "alias", false},
		{"self", `{"alias":"alias"}`, "alias", true},
		{"chain", `{"alias":"middle","middle":"canonical"}`, "canonical", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Set("model_mapping", tc.mapping)
			info := &relaycommon.RelayInfo{OriginModelName: "alias-openai-compact", RelayMode: relayconstant.RelayModeResponsesCompact, ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "alias-openai-compact"}}
			req := &dto.AudioRequest{Model: "alias-openai-compact"}
			require.NoError(t, ModelMappedHelper(c, info, req))
			assert.Equal(t, tc.want, req.Model)
			assert.Equal(t, tc.want+"-openai-compact", info.OriginModelName)
			assert.Equal(t, tc.mapped, info.IsModelMapped)
		})
	}
}
