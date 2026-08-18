package controller

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

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

func TestRouteDecisionSnapshotsDoNotShareAttemptStorage(t *testing.T) {
	trace := modelcatalog.RouteDecisionTrace{
		Attempts: []modelcatalog.Attempt{{ChannelID: 7, Reason: "original"}},
	}
	RecordRouteDecision("req-snapshot-isolation", trace)
	trace.Attempts[0].Reason = "mutated input"

	first, ok := GetRouteDecision("req-snapshot-isolation")
	require.True(t, ok)
	require.Equal(t, "original", first.Attempts[0].Reason)
	first.Attempts[0].Reason = "mutated output"

	second, ok := GetRouteDecision("req-snapshot-isolation")
	require.True(t, ok)
	require.Equal(t, "original", second.Attempts[0].Reason)
}

func TestAppendRouteAttemptRefreshesTraceLifetime(t *testing.T) {
	const requestID = "req-refresh-lifetime"
	traceStoreMu.Lock()
	traceStore[requestID] = traceEntry{RecordedAt: time.Now().Add(-routeDecisionTraceTTL - time.Second)}
	traceStoreMu.Unlock()

	AppendRouteAttempt(requestID, modelcatalog.Attempt{ChannelID: 9})
	trace, ok := GetRouteDecision(requestID)
	require.True(t, ok)
	require.Len(t, trace.Attempts, 1)
}
