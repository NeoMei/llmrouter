package sora

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildRequestBodyBindsReplayMetadataForPassThrough(t *testing.T) {
	gin.SetMode(gin.TestMode)
	payload := []byte("opaque sora request body")
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/videos", bytes.NewReader(payload))
	ctx.Request.Header.Set("Content-Type", "application/octet-stream")
	defer common.CleanupBodyStorage(ctx)

	info := &relaycommon.RelayInfo{}
	body, err := (&TaskAdaptor{}).BuildRequestBody(ctx, info)
	require.NoError(t, err)
	assert.Equal(t, int64(len(payload)), info.UpstreamRequestBodySize)
	require.NotNil(t, info.UpstreamRequestGetBody)

	primary, err := io.ReadAll(body)
	require.NoError(t, err)
	assert.Equal(t, payload, primary)
	for range 2 {
		replay, err := info.UpstreamRequestGetBody()
		require.NoError(t, err)
		got, err := io.ReadAll(replay)
		require.NoError(t, err)
		require.NoError(t, replay.Close())
		assert.Equal(t, payload, got)
	}
}
