package service

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/gin-gonic/gin"
)

// productFundingSession implements the original billing interface. Identity
// source never chooses a different relay or key model.
type productFundingSession struct {
	store *model.ProductFundingStore
	info  *relaycommon.RelayInfo
	call  model.ProductRuntimeCall
	mu    sync.Mutex
}

func newProductFundingSession(ctx context.Context, info *relaycommon.RelayInfo, quota int) (*productFundingSession, bool, error) {
	store, managed, err := model.ProductFundingStoreForUser(ctx, info.UserId)
	if err != nil || !managed {
		return nil, managed, err
	}
	snapshot, err := common.Marshal(map[string]any{"price": info.PriceData, "tiered": info.TieredBillingSnapshot, "group": info.UsingGroup, "model": info.BillingModelNameOrOrigin()})
	if err != nil {
		return nil, true, err
	}
	call, err := store.Reserve(ctx, model.ProductFundingAdmission{CallID: info.RequestId, UserID: info.UserId, TokenID: info.TokenId, Model: info.BillingModelNameOrOrigin(), Operation: strconv.Itoa(info.RelayMode), PriceSnapshot: string(snapshot), Quota: quota})
	if err != nil {
		return nil, true, err
	}
	info.BillingSource = "unihub"
	info.FinalPreConsumedQuota = call.ReservedQuota
	return &productFundingSession{store: store, info: info, call: call}, true, nil
}

func (s *productFundingSession) Reserve(target int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if target <= s.call.ReservedQuota {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := s.store.TopUp(ctx, s.call.ID, target); err != nil {
		return err
	}
	s.call.ReservedQuota = target
	s.info.FinalPreConsumedQuota = target
	return nil
}

func (s *productFundingSession) Settle(actual int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	usage, err := common.Marshal(map[string]any{"price": s.info.PriceData, "tiered": s.info.TieredBillingSnapshot, "model": s.info.BillingModelNameOrOrigin(), "group": s.info.UsingGroup, "quota": actual})
	if err != nil {
		return err
	}
	if err = s.store.Settle(ctx, s.call.ID, actual, string(usage)); err != nil {
		return err
	}
	s.call.State = "settled"
	s.call.FinalQuota = actual
	return nil
}

func (s *productFundingSession) Refund(c *gin.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.call.State == "settled" || s.call.State == "released" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := s.store.Release(ctx, s.call.ID, "product_request_failed"); err != nil {
		common.SysError("central product release pending")
		return
	}
	s.call.State = "released"
}

// MarkProductFundingDispatched fences recovery before the original relay sends.
func MarkProductFundingDispatched(ctx context.Context, info *relaycommon.RelayInfo) error {
	if info == nil || info.PlatformMode {
		return nil
	}
	if session, ok := info.Billing.(*productFundingSession); ok {
		return session.store.MarkDispatched(ctx, session.call.ID)
	}
	if info.NativeBilling != nil {
		return model.MarkProductFundingDispatched(ctx, info.NativeBilling.CallID)
	}
	return nil
}

func (s *productFundingSession) NeedsRefund() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.call.State != "settled" && s.call.State != "released"
}
func (s *productFundingSession) GetPreConsumedQuota() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.call.ReservedQuota
}

// Reconciliation is financial only. It never redispatches a provider request.
func runProductRecoveryStages(ctx context.Context, budget time.Duration, stages []func(context.Context) error) []error {
	results := make([]error, 0, len(stages))
	for _, stage := range stages {
		if ctx.Err() != nil {
			break
		}
		run, stop := context.WithTimeout(ctx, budget)
		results = append(results, stage(run))
		stop()
	}
	return results
}

func StartProductFundingRecovery(ctx context.Context) func() {
	ctx, cancel := context.WithCancel(ctx)
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				results := runProductRecoveryStages(ctx, 5*time.Second, []func(context.Context) error{
					model.RecoverProductRefunds,
					model.RecoverProductSubscriptionPurchases,
					func(run context.Context) error { return model.FlushProductCreditAwards(run, 0) },
					model.RecoverProductFunding,
				})
				messages := []string{"central product refund pending", "central product purchase pending", "central product credit pending", "central product reconciliation pending"}
				for i, err := range results {
					if err != nil && !(i == 3 && errors.Is(err, model.ErrProductFundingPending)) {
						common.SysError(messages[i])
					}
				}
			}
		}
	}()
	return cancel
}
