package middleware

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestDistributeRejectsPinnedUnsupportedChannelBeforeNext(t *testing.T) {
	previousDB := model.DB
	previousType := common.MainDatabaseType()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Channel{}))
	model.DB = db
	common.SetMainDatabaseType(common.DatabaseTypeSQLite)
	t.Cleanup(func() {
		model.DB = previousDB
		common.SetMainDatabaseType(previousType)
	})

	channel := &model.Channel{
		Name:   "unsupported-audio",
		Type:   constant.ChannelTypeJina,
		Key:    "test-key",
		Status: common.ChannelStatusEnabled,
	}
	require.NoError(t, db.Create(channel).Error)

	nextCalled := false
	router := gin.New()
	router.POST("/v1/audio/speech", func(c *gin.Context) {
		common.SetContextKey(c, constant.ContextKeyTokenSpecificChannelId, strconv.Itoa(channel.Id))
		common.SetContextKey(c, constant.ContextKeyUsingGroup, "default")
		c.Next()
	}, Distribute(), func(c *gin.Context) {
		nextCalled = true
		c.Status(http.StatusNoContent)
	})

	request := httptest.NewRequest(http.MethodPost, "/v1/audio/speech", bytes.NewBufferString(`{"model":"tts-1","input":"hello","voice":"alloy"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	assert.Equal(t, http.StatusBadRequest, response.Code)
	assert.Contains(t, response.Body.String(), "unsupported_operation")
	assert.False(t, nextCalled)
}
