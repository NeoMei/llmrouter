package service

import (
	"context"
	"testing"

	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTaskTokenSettlementUsesAdmissionSnapshot(t *testing.T) {
	oldModels, oldGroups := ratio_setting.ModelRatio2JSONString(), ratio_setting.GroupRatio2JSONString()
	t.Cleanup(func() {
		require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(oldModels))
		require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(oldGroups))
	})
	require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(`{"snapshot-alias":11,"snapshot-billing":5}`))
	require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(`{"default":3}`))

	for _, tt := range []struct {
		name     string
		snapshot *model.TaskBillingContext
		want     int
	}{
		{
			name: "frozen model group and request factors",
			snapshot: &model.TaskBillingContext{ModelRatio: 2, GroupRatio: 2, OriginModelName: "snapshot-alias",
				OtherRatios: map[string]float64{"video_input": 0.5}},
			want: 200,
		},
		{
			name:     "zero group ratio cannot become payable",
			snapshot: &model.TaskBillingContext{ModelRatio: 2, GroupRatio: 0, OriginModelName: "snapshot-alias"},
			want:     0,
		},
		{
			name: "fixed-price tasks keep the submission charge",
			snapshot: &model.TaskBillingContext{ModelPrice: 0.01, GroupRatio: 2, OriginModelName: "snapshot-alias",
				PerCallBilling: true},
			want: 100,
		},
		{name: "legacy task without a snapshot", want: 3300},
	} {
		t.Run(tt.name, func(t *testing.T) {
			truncate(t)
			precharge := 100
			if tt.snapshot != nil && tt.snapshot.GroupRatio == 0 {
				precharge = 0
			}
			seedUser(t, 91, 10000-precharge)
			seedChannel(t, 92)
			seedChargedAccounting(t, 91, 92, 0, precharge, 1)
			task := makeTask(91, 92, precharge, 0, "wallet", 0)
			task.Properties.OriginModelName = "snapshot-alias"
			task.PrivateData.BillingContext = tt.snapshot
			require.NoError(t, model.DB.Create(task).Error)

			RecalculateTaskQuotaByTokens(context.Background(), task, 100)

			var persisted model.Task
			var user model.User
			require.NoError(t, model.DB.First(&persisted, task.ID).Error)
			require.NoError(t, model.DB.First(&user, 91).Error)
			assert.Equal(t, tt.want, persisted.Quota)
			assert.Equal(t, 10000-tt.want, user.Quota)
			assert.Equal(t, tt.want, user.UsedQuota)
			assert.Equal(t, 1, user.RequestCount)
		})
	}
}
