package openai

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/service/responsesstate"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestPersistResponsesTurn(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)

	store := responsesstate.NewMemory(time.Hour, 1<<20)
	info := &relaycommon.RelayInfo{
		UserId:            11,
		TokenId:           7,
		ModelRouteId:      3,
		ModelTargetId:     4,
		ResolvedChannelId: 5,
		ResolvedModelName: "model-a",
		ChannelMeta: &relaycommon.ChannelMeta{
			UpstreamModelName: "model-a",
		},
	}

	require.NoError(t, responsesstate.SaveTurn(
		c.Request.Context(),
		store,
		responsesstate.Record{
			Version: responsesstate.CurrentVersion,
			Scope: responsesstate.Scope{
				TenantID: info.UserId, UserID: info.UserId, TokenID: info.TokenId, RouteID: info.ModelRouteId,
				TargetID: info.ModelTargetId, ChannelID: info.ResolvedChannelId, Model: info.ResolvedModelNameOrOrigin(),
			},
			ResponseID: "resp_1",
			Input:      json.RawMessage(`[{"type":"message","role":"user","content":"hello"}]`),
			Output:     json.RawMessage(`[{"type":"message","role":"assistant","content":"answer"}]`),
		},
	))

	expected := responsesstate.Scope{
		TenantID: 11, UserID: 11, TokenID: 7, RouteID: 3,
		TargetID: 4, ChannelID: 5, Model: "model-a",
	}
	record, err := store.Load(c.Request.Context(), expected, "resp_1")
	require.NoError(t, err)
	require.Equal(t, "model-a", record.Scope.Model)
	require.Contains(t, string(record.Input), "hello")
	require.Contains(t, string(record.Output), "answer")
}
