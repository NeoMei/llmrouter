package relay

import (
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

func TestChatRouteHeadersDoNotDiscloseUnsafeConfiguredTarget(t *testing.T) {
	for _, target := range []string{"https://provider.invalid/secret", "sk-private", "provider//private"} {
		t.Run(target, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest("POST", "/v1/chat/completions", nil)
			setChatRouteHeaders(c, &relaycommon.RelayInfo{OriginModelName: "public", ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: target}})
			assert.Empty(t, c.Writer.Header().Get(common.RoutedModelHeader))
		})
	}
}
