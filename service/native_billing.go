package service

import (
	"context"
	"errors"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/metering"
	perfmetrics "github.com/QuantumNous/new-api/pkg/perf_metrics"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"time"
)

func nativeBillingStore() (*model.NativeBillingStore, error) {
	if model.DB == nil {
		return nil, errors.New("native billing database unavailable")
	}
	if common.RedisEnabled && common.RDB == nil {
		return nil, errors.New("native billing Redis unavailable")
	}
	if common.RedisEnabled {
		return model.NewNativeBillingStore(model.DB, common.RDB), nil
	}
	return model.NewNativeBillingStore(model.DB, nil), nil
}
func BeginNativeBilling(ctx context.Context, a model.NativeAdmission) (model.NativeBilling, error) {
	s, err := nativeBillingStore()
	if err != nil {
		return model.NativeBilling{}, err
	}
	defer s.Close()
	return s.Begin(ctx, a)
}
func RecordNativeUsage(ctx context.Context, id string, u metering.Usage) error {
	s, err := nativeBillingStore()
	if err != nil {
		return err
	}
	defer s.Close()
	return s.RecordUsage(ctx, id, u)
}
func FinishNativeBilling(ctx context.Context, id, outcome string) (model.NativeBilling, error) {
	s, err := nativeBillingStore()
	if err != nil {
		return model.NativeBilling{}, err
	}
	defer s.Close()
	return s.Finish(ctx, id, outcome)
}
func RecordNativeUsageForRelay(ctx context.Context, info *relaycommon.RelayInfo, u metering.Usage) error {
	durable, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer cancel()
	ctx = durable
	if info == nil || info.NativeBilling == nil {
		return errors.New("native billing contract unavailable")
	}
	n := info.NativeBilling
	previouslyConfirmed := n.Usage != nil && n.Usage.State == "confirmed"
	if info.PlatformMode || n.Platform {
		if !info.PlatformMode || !n.Platform || n.Observe == nil {
			return errors.New("platform native evidence observer unavailable")
		}
		if err := n.Record(ctx, u); err != nil {
			return err
		}
		if u.State == "confirmed" {
			recordPlatformRelayTelemetryInfo(ctx, info)
		}
		return nil
	}
	if err := RecordNativeUsage(ctx, n.CallID, u); err != nil {
		return err
	}
	if err := n.Record(ctx, u); err != nil {
		return err
	}
	if u.State == "confirmed" {
		_, err := FinishNativeBilling(ctx, n.CallID, "measured")
		if err == nil && !previouslyConfirmed {
			perfmetrics.RecordRelaySampleAsync(info, true, nativeOutputTokens(&u))
		}
		return err
	}
	return nil
}

// BeginNativeBillingForRelay binds a trusted, bounded request estimate. The
// provider integration must call MarkNativeBillingDispatched immediately before
// sending; uncertain dispatch is thereafter a durable hold, never a refund.
func BeginNativeBillingForRelay(ctx context.Context, info *relaycommon.RelayInfo, a model.NativeAdmission) error {
	if info == nil || info.PlatformMode || a.Platform || info.Billing != nil {
		return errors.New("local native funding ownership conflict")
	}
	if info.NativeBilling != nil {
		return errors.New("native billing already bound")
	}
	n, err := BeginNativeBilling(ctx, a)
	if err != nil {
		return err
	}
	info.NativeBilling = &relaycommon.NativeBillingContext{CallID: n.ID, AttemptID: n.AttemptID, Price: a.Price, Policy: a.Price.Policy}
	info.FinalPreConsumedQuota = n.ReservedQuota
	return nil
}
func MarkNativeBillingDispatched(ctx context.Context, info *relaycommon.RelayInfo) error {
	if info == nil || info.NativeBilling == nil || info.PlatformMode {
		return errors.New("local native billing unavailable")
	}
	s, err := nativeBillingStore()
	if err != nil {
		return err
	}
	defer s.Close()
	return s.MarkDispatched(ctx, info.NativeBilling.CallID, info.NativeBilling.AttemptID)
}
func ResolveNativeBilling(ctx context.Context, id, actionID string, actor int, action string, u *metering.Usage, reason string) (model.NativeBilling, error) {
	s, err := nativeBillingStore()
	if err != nil {
		return model.NativeBilling{}, err
	}
	defer s.Close()
	return s.ResolveMeasurement(ctx, id, actionID, actor, action, u, reason)
}

// StartNativeBillingRecovery drains only durable money decisions and audit
// projections. It never dispatches providers or releases unmeasured holds.
func StartNativeBillingRecovery(ctx context.Context) func() {
	ctx, cancel := context.WithCancel(ctx)
	initial, initialCancel := context.WithTimeout(ctx, 25*time.Second)
	if s, err := nativeBillingStore(); err == nil {
		if recErr := s.Recover(initial, 100); recErr != nil {
			common.SysError("native billing startup recovery: " + recErr.Error())
		}
		if logErr := s.ProjectAuditLogs(initial, model.LOG_DB, 100); logErr != nil {
			common.SysError("native billing startup projection: " + logErr.Error())
		}
		_ = s.Close()
	} else {
		common.SysError("native billing startup recovery: " + err.Error())
	}
	initialCancel()
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				run, cancelRun := context.WithTimeout(ctx, 25*time.Second)
				s, err := nativeBillingStore()
				if err == nil {
					err = s.Recover(run, 100)
					if logErr := s.ProjectAuditLogs(run, model.LOG_DB, 100); logErr != nil {
						common.SysError("native billing log projection: " + logErr.Error())
					}
					s.Close()
				}
				if err != nil {
					common.SysError("native billing recovery: " + err.Error())
				}
				cancelRun()
			}
		}
	}()
	return cancel
}
