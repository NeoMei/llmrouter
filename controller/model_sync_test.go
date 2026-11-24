package controller

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSyncModelsKeepsVendorWritesInTheSelectedModelTransaction(t *testing.T) {
	db := setupModelListControllerTestDB(t)
	metadata := &model.Model{ModelName: "existing-model", Description: "old", Status: 1, SyncOfficial: 1}
	require.NoError(t, metadata.Insert())
	require.NoError(t, db.Create(&model.Ability{Group: "default", Model: "blocked-model", ChannelId: 1, Enabled: true}).Error)
	require.NoError(t, db.Exec(`CREATE TRIGGER test_reject_metadata_sync BEFORE INSERT ON models
		WHEN NEW.model_name = 'blocked-model' BEGIN SELECT RAISE(ABORT, 'metadata write failed'); END`).Error)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/vendors.json") {
			_, _ = w.Write([]byte(`[{"name":"unselected vendor"},{"name":"rolled back vendor"}]`))
			return
		}
		_, _ = w.Write([]byte(`[
			{"model_name":"existing-model","description":"selected description","vendor_name":"unselected vendor"},
			{"model_name":"blocked-model","vendor_name":"rolled back vendor"}
		]`))
	}))
	defer upstream.Close()
	t.Setenv("SYNC_UPSTREAM_BASE", upstream.URL)

	router := gin.New()
	router.POST("/sync", SyncUpstreamModels)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/sync", strings.NewReader(
		`{"overwrite":[{"model_name":"existing-model","fields":["description"]}]}`,
	))
	request.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(recorder, request)

	var response struct {
		Success bool `json:"success"`
		Data    struct {
			CreatedModels  int      `json:"created_models"`
			CreatedVendors int      `json:"created_vendors"`
			UpdatedModels  int      `json:"updated_models"`
			SkippedModels  []string `json:"skipped_models"`
		} `json:"data"`
	}
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
	require.True(t, response.Success, recorder.Body.String())
	assert.Zero(t, response.Data.CreatedModels)
	assert.Zero(t, response.Data.CreatedVendors)
	assert.Equal(t, 1, response.Data.UpdatedModels)
	assert.Equal(t, []string{"blocked-model"}, response.Data.SkippedModels)

	require.NoError(t, db.First(metadata, metadata.Id).Error)
	assert.Equal(t, "selected description", metadata.Description)
	assert.Zero(t, metadata.VendorID)
	var count int64
	require.NoError(t, db.Model(&model.Vendor{}).Count(&count).Error)
	assert.Zero(t, count)
}
