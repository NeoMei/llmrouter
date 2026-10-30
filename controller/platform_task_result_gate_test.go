package controller

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/internal/platformbilling"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestPlatformVideoResultRequiresSettledReceipt(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open("file:"+t.TempDir()+"/video.db"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Task{}))
	previousDB, previousCheck := model.DB, service.PlatformTaskSettlementCheck
	model.DB = db
	t.Cleanup(func() { model.DB, service.PlatformTaskSettlementCheck = previousDB, previousCheck })
	row := platformbilling.Call{CallID: "call", Operation: "video", ProviderTaskID: "provider"}
	task := model.Task{TaskID: "task", Status: model.TaskStatusSuccess, FundingOwner: "platform", PlatformCallID: row.CallID, PrivateData: model.TaskPrivateData{UpstreamTaskID: "provider"}}
	require.NoError(t, db.Create(&task).Error)
	for _, tc := range []struct {
		name    string
		checker func(context.Context, string) error
	}{
		{"authority unavailable", nil},
		{"not settled", func(context.Context, string) error { return errors.New("pending") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service.PlatformTaskSettlementCheck = tc.checker
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodGet, "/platform/v1/videos/call/result", nil)
			c.Set("platform_task_call", row)
			PlatformTaskResult(c)
			assert.Equal(t, http.StatusConflict, recorder.Code)
			assert.Contains(t, recorder.Body.String(), "native_settlement_pending")
		})
	}
}
