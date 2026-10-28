package service

import (
	"context"
	"errors"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/metering"
	"github.com/QuantumNous/new-api/relay/channel/task/taskcommon"
)

// Installed by the platform router; background workers retain no credentials.
var PlatformTaskObserver func(context.Context, string, string, metering.Usage) error

// PlatformTaskRelease is installed by platformbilling for trusted no-output terminals.
var PlatformTaskRelease func(context.Context, string, string) error

var PlatformTaskBind func(context.Context, string, string) error

// prepareNativeTaskIntent runs before the terminal CAS, so success and evidence
// are one row mutation. A crash after this point is replayable without a provider
// request or current pricing lookup.
func prepareNativeTaskIntent(task *model.Task, body []byte) error {
	if task.FundingOwner != "native" && task.FundingOwner != "platform" {
		return nil
	}
	bc := task.PrivateData.BillingContext
	if bc == nil || bc.NativePrice == nil {
		return errors.New("native task contract missing")
	}
	u := taskcommon.VideoNativeUsage(*bc.NativePrice, body, task.GetUpstreamTaskID(), bc.VideoResolution)
	if task.Status != model.TaskStatusSuccess {
		u.State = "incomplete"
		u.Measurements = nil
	}
	raw, err := common.Marshal(u)
	if err != nil {
		return err
	}
	if task.NativeEvidence != "" && task.NativeEvidence != string(raw) {
		return errors.New("native task evidence conflict")
	}
	task.NativeEvidence = string(raw)
	task.NativePending = true
	return nil
}

func PrepareNativeTaskEvidence(task *model.Task, body []byte) error {
	return prepareNativeTaskIntent(task, body)
}

func SettleNativeTask(ctx context.Context, task *model.Task) error {
	return settleNativeTask(ctx, task)
}
func settleNativeTask(ctx context.Context, task *model.Task) error {
	if err := model.DB.WithContext(ctx).First(task, task.ID).Error; err != nil {
		return err
	}
	if !task.NativePending {
		return nil
	}
	if task.NativeConflictEvidence != "" {
		return errors.New("native task evidence conflict pending reconciliation")
	}
	bc := task.PrivateData.BillingContext
	if bc == nil || bc.NativePrice == nil {
		return errors.New("native task contract missing")
	}
	var u metering.Usage
	if err := common.UnmarshalJsonStr(task.NativeEvidence, &u); err != nil {
		return err
	}
	if task.FundingOwner == "platform" {
		if task.Status == model.TaskStatusFailure && u.Source == "provider_confirmed_no_output" {
			if PlatformTaskRelease == nil {
				return errors.New("platform task release unavailable")
			}
			if err := PlatformTaskRelease(ctx, bc.PlatformCallID, task.GetUpstreamTaskID()); err != nil {
				return err
			}
			goto clear
		}
		if u.State == "confirmed" && len(u.Measurements) == 0 && PlatformTaskRelease != nil {
			return PlatformTaskRelease(ctx, bc.PlatformCallID, task.GetUpstreamTaskID())
		}
		if PlatformTaskObserver == nil {
			return errors.New("platform task observer unavailable")
		}
		if err := PlatformTaskObserver(ctx, bc.PlatformCallID, task.GetUpstreamTaskID(), u); err != nil {
			return err
		}
	} else if task.FundingOwner == "native" {
		if task.Status == model.TaskStatusFailure && u.Source == "provider_confirmed_no_output" {
			if _, err := FinishNativeBilling(ctx, bc.NativeCallID, "no_output"); err != nil {
				return err
			}
			goto clear
		}
		if err := RecordNativeUsage(ctx, bc.NativeCallID, u); err != nil {
			return err
		}
		if u.State == "confirmed" {
			if _, err := FinishNativeBilling(ctx, bc.NativeCallID, "measured"); err != nil {
				return err
			}
		}
	} else {
		return errors.New("native task funding owner missing")
	}
	if u.State != "confirmed" {
		return nil
	}

clear:
	// Unknown evidence is durably held in the owner journal; it is not a final
	// debit or refund. Avoid indefinitely polling the supplier for the same data.
	result := model.DB.WithContext(ctx).Model(&model.Task{}).Where("id = ? AND native_evidence = ?", task.ID, task.NativeEvidence).Update("native_pending", false)
	if result.Error == nil {
		task.NativePending = false
	}
	return result.Error
}

// Each run advances through the complete ordered snapshot, rather than letting
// permanent measurement holds occupy the first page forever.
func recoverNativeTasks(ctx context.Context) {
	var after int64
	for ctx.Err() == nil {
		var tasks []model.Task
		if model.DB.WithContext(ctx).Where("native_pending = ? AND id > ?", true, after).Order("id").Limit(100).Find(&tasks).Error != nil {
			return
		}
		for i := range tasks {
			if err := settleNativeTask(ctx, &tasks[i]); err != nil {
				common.SysError("native task settlement pending: " + err.Error())
			}
			after = tasks[i].ID
		}
		if len(tasks) < 100 {
			return
		}
	}
}
