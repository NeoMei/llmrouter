package relay

import (
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/gin-gonic/gin"
	"golang.org/x/net/http/httpguts"
)

// These identify the gateway's selected route, not the provider's response model.
func setChatRouteHeaders(c *gin.Context, info *relaycommon.RelayInfo) {
	if c.Request.URL.Path != "/v1/chat/completions" || c.Writer.Written() || info.ChannelMeta == nil {
		return
	}
	requested, routed := info.OriginModelName, info.UpstreamModelName
	if requested == "" || !dto.IsPublicConfiguredModelID(routed) || !httpguts.ValidHeaderFieldValue(requested) || !httpguts.ValidHeaderFieldValue(routed) {
		return
	}
	c.Header(common.RequestedModelHeader, requested)
	c.Header(common.RoutedModelHeader, routed)
}
