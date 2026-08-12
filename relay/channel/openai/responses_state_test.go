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
		UserId:  11,
		TokenId: 7,
		ChannelMeta: &relaycommon.ChannelMeta{
			UpstreamModelName: "model-a",
		},
	}

	responsesstate.SaveTurn(
		c.Request.Context(),
		store,
		info.UserId,
		info.TokenId,
		"resp_1",
		info.UpstreamModelName,
		json.RawMessage(`[{"type":"message","role":"user","content":"hello"}]`),
		json.RawMessage(`[{"type":"message","role":"assistant","content":"answer"}]`),
	)

	record, err := store.Load(c.Request.Context(), 11, 7, "resp_1")
	require.NoError(t, err)
	require.Equal(t, "model-a", record.Model)
	require.Contains(t, string(record.Input), "hello")
	require.Contains(t, string(record.Output), "answer")
}
