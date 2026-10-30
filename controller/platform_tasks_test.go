package controller

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/internal/platformbilling"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/metering"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type wanCancelAdaptor struct {
	cancellations int
	running       bool
	responseBody  string
}

func (a *wanCancelAdaptor) Init(*relaycommon.RelayInfo) {}
func (a *wanCancelAdaptor) FetchTask(string, string, map[string]any, string) (*http.Response, error) {
	return nil, http.ErrNotSupported
}
func (a *wanCancelAdaptor) ParseTaskResult([]byte) (*relaycommon.TaskInfo, error) {
	return nil, http.ErrNotSupported
}
func (a *wanCancelAdaptor) AdjustBillingOnComplete(*model.Task, *relaycommon.TaskInfo) int { return 0 }
func (a *wanCancelAdaptor) CancelTask(_, _, _, _ string) (*http.Response, error) {
	a.cancellations++
	body := `{"output":{"task_id":"provider-wan","task_status":"CANCELED","results":[]},"usage":{"video_count":0}}`
	status := http.StatusOK
	if a.responseBody != "" {
		body = a.responseBody
	}
	if a.running {
		body = `{"code":"UnsupportedOperation","message":"task is running","request_id":"req-running"}`
		status = http.StatusBadRequest
	}
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body))}, nil
}

func newWanCancelFixture(t *testing.T, adaptor *wanCancelAdaptor) (*gin.Context, *httptest.ResponseRecorder, *model.Task) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open("file:"+t.TempDir()+"/cancel.db"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Channel{}, &model.Task{}, &platformbilling.Call{}, &platformbilling.Attempt{}))
	oldDB := model.DB
	model.DB = db
	t.Cleanup(func() { model.DB = oldDB })
	require.NoError(t, db.Create(&model.Channel{Id: 16, Type: constant.ChannelTypeAli, Key: "channel-key", Status: common.ChannelStatusEnabled}).Error)
	call := platformbilling.Call{CallID: "wan-cancel-call", Operation: "video", WireOperation: "videos.create", ProviderTaskID: "provider-wan", Status: "UNKNOWN"}
	require.NoError(t, db.Create(&call).Error)
	require.NoError(t, db.Create(&platformbilling.Attempt{CallID: call.CallID, ChannelID: 16, Status: "DISPATCHED"}).Error)
	task := &model.Task{
		TaskID: "task_wan_cancel", Platform: constant.TaskPlatform("17"), UserId: 1, ChannelId: 16, Status: model.TaskStatusQueued,
		SubmitTime: time.Now().Unix(), FundingOwner: "platform", PlatformCallID: call.CallID,
		PrivateData: model.TaskPrivateData{UpstreamTaskID: "provider-wan", BillingContext: &model.TaskBillingContext{
			NativePrice: &metering.Price{Version: "metered_v2", Operation: "video", Policy: "ali_wan26_t2v_billable_duration_v1", Terms: []metering.Term{
				{Key: "video_720p", Unit: "video_second", Price: metering.Rational{Numerator: "3", Denominator: "5"}},
				{Key: "video_1080p", Unit: "video_second", Price: metering.Rational{Numerator: "1", Denominator: "1"}},
			}}, PlatformCallID: call.CallID, VideoResolution: "video_720p"},
		},
	}
	require.NoError(t, db.Create(task).Error)
	oldAdaptor := service.GetTaskAdaptorFunc
	service.GetTaskAdaptorFunc = func(constant.TaskPlatform) service.TaskPollingAdaptor { return adaptor }
	t.Cleanup(func() { service.GetTaskAdaptorFunc = oldAdaptor })
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/platform/v1/videos/wan-cancel-call/cancel", nil)
	ctx.Set("platform_task_call", call)
	return ctx, recorder, task
}

func TestPlatformWanPendingCancelConfirmsAndReleasesOnce(t *testing.T) {
	adaptor := &wanCancelAdaptor{}
	ctx, recorder, task := newWanCancelFixture(t, adaptor)
	releases := 0
	oldRelease := service.PlatformTaskRelease
	service.PlatformTaskRelease = func(context.Context, string, string) error { releases++; return nil }
	t.Cleanup(func() { service.PlatformTaskRelease = oldRelease })

	PlatformTaskCancel(ctx)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	require.Equal(t, 1, adaptor.cancellations)
	assert.Equal(t, 1, releases)
	var done model.Task
	require.NoError(t, model.DB.First(&done, task.ID).Error)
	assert.Equal(t, model.TaskStatus(model.TaskStatusFailure), done.Status)
	assert.False(t, done.NativePending)
	var evidence metering.Usage
	require.NoError(t, common.UnmarshalJsonStr(done.NativeEvidence, &evidence))
	assert.Equal(t, "provider_confirmed_no_output", evidence.Source)
}

func TestPlatformWanRunningCancelRefusalKeepsHoldAndTaskPending(t *testing.T) {
	adaptor := &wanCancelAdaptor{running: true}
	ctx, recorder, task := newWanCancelFixture(t, adaptor)
	releases := 0
	oldRelease := service.PlatformTaskRelease
	service.PlatformTaskRelease = func(context.Context, string, string) error { releases++; return nil }
	t.Cleanup(func() { service.PlatformTaskRelease = oldRelease })

	PlatformTaskCancel(ctx)
	require.Equal(t, http.StatusConflict, recorder.Code, recorder.Body.String())
	require.Contains(t, recorder.Body.String(), "provider_cancellation_unsupported")
	require.Equal(t, 1, adaptor.cancellations)
	assert.Zero(t, releases)
	var unchanged model.Task
	require.NoError(t, model.DB.First(&unchanged, task.ID).Error)
	assert.Equal(t, model.TaskStatus(model.TaskStatusQueued), unchanged.Status)
	assert.False(t, unchanged.NativePending)
}

func TestPlatformWanCancelRejectsUnboundOrContradictoryReceipt(t *testing.T) {
	for name, body := range map[string]string{
		"wrong_task":   `{"output":{"task_id":"another-task","task_status":"CANCELED","results":[]},"usage":{"video_count":0}}`,
		"missing_task": `{"output":{"task_status":"CANCELED","results":[]},"usage":{"video_count":0}}`,
		"output_count": `{"output":{"task_id":"provider-wan","task_status":"CANCELED","results":[]},"usage":{"video_count":1}}`,
	} {
		t.Run(name, func(t *testing.T) {
			ctx, recorder, task := newWanCancelFixture(t, &wanCancelAdaptor{responseBody: body})
			releases := 0
			oldRelease := service.PlatformTaskRelease
			service.PlatformTaskRelease = func(context.Context, string, string) error { releases++; return nil }
			t.Cleanup(func() { service.PlatformTaskRelease = oldRelease })
			PlatformTaskCancel(ctx)
			assert.NotEqual(t, http.StatusOK, recorder.Code)
			assert.Zero(t, releases)
			var unchanged model.Task
			require.NoError(t, model.DB.First(&unchanged, task.ID).Error)
			assert.Equal(t, model.TaskStatus(model.TaskStatusQueued), unchanged.Status)
			assert.False(t, unchanged.NativePending)
		})
	}
}
