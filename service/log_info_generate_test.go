package service

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/relay/common"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestRouteDecisionTraceInOtherInfo(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	relayInfo := &common.RelayInfo{
		OriginModelName:   "gpt-5.6-sol",
		PublicModelName:   "deepseek/deepseek-v4-flash",
		ResolvedModelName: "deepseek-v4-flash",
		SelectedWire:      "responses",
		ModelRouteId:      7,
		StartTime:         time.Now(),
		FirstResponseTime: time.Now(),
	}
	trace := map[string]interface{}{
		"requested_model": "gpt-5.6-sol",
		"public_model":    "deepseek/deepseek-v4-flash",
		"resolved_model":  "deepseek-v4-flash",
		"selected_wire":   "responses",
		"route_id":        7,
		"attempts": []map[string]interface{}{
			{"channel_id": 7, "wire": "responses", "status": 200, "reason": "ok", "duration_ms": 120},
		},
	}
	data, err := json.Marshal(trace)
	require.NoError(t, err)
	relayInfo.RouteDecisionTrace = data

	other := GenerateTextOtherInfo(c, relayInfo, 1.0, 1.0, 1.0, 0, 0.0, 0.0, 1.0)
	adminInfo, ok := other["admin_info"].(map[string]interface{})
	require.True(t, ok)
	require.NotNil(t, adminInfo["route_decision"])
	require.NotContains(t, other, "sk-")
	require.NotContains(t, other, "Authorization")
}
