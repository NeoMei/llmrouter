package controller

import (
	"net/http/httptest"
	"testing"

	userkit "github.com/NeoMei/UniHub/packages/userkit-go"
	"github.com/QuantumNous/new-api/service/authz"
	"github.com/QuantumNous/new-api/service/modelcatalog"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

func TestProductRouteDecisionRedactsProviderFreeText(t *testing.T) {
	RecordRouteDecision("product-view-redaction", modelcatalog.RouteDecisionTrace{PublicModel: "public-model", Attempts: []modelcatalog.Attempt{{ChannelID: 7, Wire: "openai_chat", Status: 401, Reason: "Bearer provider-secret", RejectedReason: "https://user:secret@provider.test"}}})
	for _, product := range []bool{true, false} {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Params = gin.Params{{Key: "request_id", Value: "product-view-redaction"}}
		if product {
			authz.SetProductPrincipal(c, userkit.Principal{})
		}
		GetRouteDecisionEndpoint(c)
		assert.Equal(t, 200, w.Code)
		assert.Contains(t, w.Body.String(), "public-model")
		if product {
			assert.NotContains(t, w.Body.String(), "provider-secret")
			assert.NotContains(t, w.Body.String(), "user:secret")
		} else {
			assert.Contains(t, w.Body.String(), "provider-secret")
		}
	}
}
