package controller

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetCapabilitiesReturnsStablePublicSnapshotWithoutSecrets(t *testing.T) {
	router := gin.New()
	router.GET("/api/capabilities", GetCapabilities)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/capabilities", nil))

	require.Equal(t, http.StatusOK, response.Code)
	var envelope struct {
		Success bool `json:"success"`
		Data    struct {
			Version      string `json:"version"`
			Capabilities []struct {
				ChannelType int    `json:"channel_type"`
				ChannelName string `json:"channel_name"`
				Protocol    string `json:"protocol"`
				Operation   string `json:"operation"`
				Supported   bool   `json:"supported"`
				Reason      string `json:"reason"`
			} `json:"capabilities"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &envelope))
	assert.True(t, envelope.Success)
	assert.Len(t, envelope.Data.Version, 64)
	assert.NotEmpty(t, envelope.Data.Capabilities)

	body := strings.ToLower(response.Body.String())
	for _, forbidden := range []string{"api_key", "base_url", "secret", "credential"} {
		assert.NotContains(t, body, forbidden)
	}
}
