package middleware

import (
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNativeRerankCanonicalBodyReplacesCachedEnvelope(t *testing.T) {
	gin.SetMode(gin.TestMode)
	raw := `{"model":"alias","input":{"query":"q","documents":["b","a"]},"parameters":{"top_n":1,"return_documents":false}}`
	old, err := common.CreateBodyStorage([]byte(raw))
	require.NoError(t, err)
	engine := gin.New()
	engine.POST("/native", func(c *gin.Context) {
		c.Set(common.KeyBodyStorage, old)
		c.Set(common.KeyRequestBody, []byte(raw))
		c.Next()
		common.CleanupBodyStorage(c)
	}, AliNativeRerank(), func(c *gin.Context) {
		assert.True(t, common.GetContextKeyBool(c, constant.ContextKeyAliNativeRerank))
		assert.Equal(t, "/native", c.Request.URL.Path)
		_, err := old.Bytes()
		assert.ErrorIs(t, err, common.ErrStorageClosed)
		storage, err := common.GetBodyStorage(c)
		require.NoError(t, err)
		data, err := storage.Bytes()
		require.NoError(t, err)
		assert.NotContains(t, string(data), `"input"`)
		var request dto.RerankRequest
		require.NoError(t, common.UnmarshalBodyReusable(c, &request))
		assert.Equal(t, "q", request.Query)
		assert.Equal(t, []any{"b", "a"}, request.Documents)
		require.NotNil(t, request.TopN)
		assert.Equal(t, 1, *request.TopN)
		require.NotNil(t, request.ReturnDocuments)
		assert.False(t, *request.ReturnDocuments)
		replay, err := c.Request.GetBody()
		require.NoError(t, err)
		defer replay.Close()
		replayed, err := io.ReadAll(replay)
		require.NoError(t, err)
		assert.Equal(t, string(data), string(replayed))
		assert.EqualValues(t, len(data), c.Request.ContentLength)
		c.Status(204)
	})
	req := httptest.NewRequest("POST", "/native", strings.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	engine.ServeHTTP(response, req)
	assert.Equal(t, 204, response.Code)
}
