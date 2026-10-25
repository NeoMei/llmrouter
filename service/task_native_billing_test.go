package service

import (
	"context"
	"fmt"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/metering"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"path/filepath"
	"testing"
)

func TestNativeTaskRecoveryPagesPastMeasurementHolds(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "tasks.db")), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	old, observer, release := model.DB, PlatformTaskObserver, PlatformTaskRelease
	model.DB = db
	t.Cleanup(func() {
		model.DB = old
		PlatformTaskObserver = observer
		PlatformTaskRelease = release
		require.NoError(t, sqlDB.Close())
	})
	require.NoError(t, db.AutoMigrate(&model.Task{}))
	p := metering.Price{Version: "metered_v2", Operation: "video", Policy: "sora_completed_seconds_v1", Terms: []metering.Term{{Key: "video", Unit: "video_second", Price: metering.Rational{Numerator: "1", Denominator: "1"}}}}
	observed := map[string]bool{}
	PlatformTaskObserver = func(_ context.Context, call, _ string, _ metering.Usage) error { observed[call] = true; return nil }
	for i := 0; i < 101; i++ {
		u := metering.Usage{Version: "metered_v2", Operation: "video", State: "missing", Source: p.Policy}
		if i == 100 {
			u.State = "confirmed"
			u.Measurements = []metering.Measurement{{Key: "video", Unit: "video_second", Quantity: metering.Quantity{Value: "5"}}}
		}
		raw, err := common.Marshal(u)
		require.NoError(t, err)
		task := model.Task{TaskID: fmt.Sprint(i), FundingOwner: "platform", NativePending: true, NativeEvidence: string(raw), Status: model.TaskStatusSuccess, PrivateData: model.TaskPrivateData{BillingContext: &model.TaskBillingContext{NativePrice: &p, PlatformCallID: fmt.Sprint(i)}}}
		require.NoError(t, db.Create(&task).Error)
	}
	recoverNativeTasks(context.Background())
	assert.True(t, observed["100"])
	var last model.Task
	require.NoError(t, db.Where("task_id = ?", "100").First(&last).Error)
	assert.False(t, last.NativePending)
	// A losing terminal observation is retained, keeps recovery active, and blocks
	// another observer settlement even when the caller has a stale task snapshot.
	stale := last
	require.NoError(t, model.RecordNativeTaskConflict(last.ID, `{"state":"invalid"}`))
	require.Error(t, settleNativeTask(context.Background(), &stale))
	require.NoError(t, db.First(&last, last.ID).Error)
	assert.NotEmpty(t, last.NativeConflictEvidence)
	assert.True(t, last.NativePending)
}
