package controller

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/service/modelcatalog"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestGetRouteDecisionEndpoint(t *testing.T) {
	gin.SetMode(gin.TestMode)
	RecordRouteDecision("req-1", modelcatalog.RouteDecisionTrace{
		RequestedModel: "alias",
		PublicModel:    "deepseek/model",
	})

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/log/req-1/route-decision", nil)
	c.Params = gin.Params{{Key: "request_id", Value: "req-1"}}
	GetRouteDecisionEndpoint(c)

	require.Equal(t, http.StatusOK, w.Code)
	require.Contains(t, w.Body.String(), "deepseek/model")
}

func TestGetRouteDecisionEndpointNotFound(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/log/req-missing/route-decision", nil)
	c.Params = gin.Params{{Key: "request_id", Value: "req-missing"}}
	GetRouteDecisionEndpoint(c)

	require.Equal(t, http.StatusNotFound, w.Code)
}
