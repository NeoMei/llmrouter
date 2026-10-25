package capability

import (
	"github.com/QuantumNous/new-api/constant"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"net/http"
	"testing"
)

func TestOCRPublicManifest(t *testing.T) {
	for _, tc := range []struct {
		method, path string
		op           Operation
	}{{http.MethodPost, "/api/v2/ocr/jobs", OperationOCRCreate}, {http.MethodGet, "/api/v2/ocr/jobs/x", OperationOCRRetrieve}, {http.MethodPost, "/v1/ocr", OperationOCRCreate}, {http.MethodGet, "/v1/ocr/x", OperationOCRRetrieve}, {http.MethodGet, "/api/ocr/jobs/x/resources/y", OperationOCRResource}} {
		p, op, err := ResolveRequest(tc.method, tc.path)
		require.NoError(t, err)
		assert.Equal(t, tc.op, op)
		assert.Equal(t, ProtocolPaddleOCR, p)
	}
	assert.True(t, DefaultRegistry().Supports(constant.ChannelTypePaddleOCR, ProtocolPaddleOCR, OperationOCRCreate))
	assert.False(t, DefaultRegistry().Supports(constant.ChannelTypePaddleOCR, ProtocolOpenAI, OperationChatCreate))
}
