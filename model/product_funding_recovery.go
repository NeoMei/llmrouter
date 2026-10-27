package model

import (
	"context"
	"errors"
	userkit "github.com/NeoMei/UniHub/packages/userkit-go"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/internal/platformaccount"
	"gorm.io/gorm"
	"strconv"
	"time"
)

const ProductFundingDispatchLeaseSeconds int64 = 120

// Keep time for the second independent recovery queue even if the first has
// only slow or permanently broken receipts.
func productRecoveryFirstPhase(ctx context.Context) (context.Context, context.CancelFunc) {
	budget := 5 * time.Second
	if deadline, ok := ctx.Deadline(); ok {
		if half := time.Until(deadline) / 2; half < budget {
			budget = half
		}
	}
	return context.WithTimeout(ctx, budget)
}

func productRuntimeNotFound(err error) bool {
	var remote *userkit.RuntimeError
	return errors.As(err, &remote) && remote.Status == 404
}
func productRuntimeDefiniteReject(err error) bool {
	var remote *userkit.RuntimeError
	return errors.As(err, &remote) && (remote.Status == 400 || remote.Status == 402 || remote.Status == 403)
}
func productPendingTopUp(tx *gorm.DB, id string) error {
	var n int64
	if err := tx.Model(&ProductFundingTopUp{}).Where("call_id = ? AND state = ?", id, "pending").Count(&n).Error; err != nil {
		return err
	}
	if n > 0 {
		return ErrProductFundingPending
	}
	return nil
}

// Serialize each external financial request against recovery with a durable row
// lock. An uncertain response is never sufficient proof to return Key capacity.
func (s *ProductFundingStore) resolveProductReserve(ctx context.Context, id string) error {
	var rejection error
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var call ProductRuntimeCall
		if err := lockForUpdate(tx).First(&call, "id = ?", id).Error; err != nil {
			return err
		}
		if call.ScopeDigest == "" || call.ScopeDigest != s.ScopeDigest() {
			return ErrProductFundingConflict
		}
		if call.State == "reserved" {
			return nil
		}
		if call.State != "reserve_pending" {
			return ErrProductFundingConflict
		}
		var req userkit.RuntimeReserveRequest
		if err := common.UnmarshalJsonStr(call.ReserveJSON, &req); err != nil {
			return err
		}
		remote, remoteErr := s.client.Reserve(ctx, req)
		if remoteErr != nil {
			confirmed, readErr := s.client.Call(ctx, id)
			var insufficient *userkit.RuntimeError
			if call.ParentCallID != "" && call.FinancialCommitment && errors.As(remoteErr, &insufficient) && insufficient.Status == 402 && productRuntimeNotFound(readErr) {
				// This is an already accepted supplier debt, not a declined new
				// request. Retain its immutable intent and Key accounting until
				// real funds arrive; recovery reuses exactly this child call.
				return ErrProductFundingPending
			}
			if productRuntimeDefiniteReject(remoteErr) && productRuntimeNotFound(readErr) {
				if err := productTokenDelta(tx, call.TokenID, -call.TokenReserved); err != nil {
					return err
				}
				rejection = remoteErr
				return tx.Model(&call).Updates(map[string]any{"state": "rejected", "token_reserved": 0, "updated_at": time.Now().Unix()}).Error
			}
			if readErr != nil {
				return remoteErr
			}
			remote = confirmed
			rejection = remoteErr
		}
		if err := s.validateRemote(call, remote); err != nil {
			return err
		}
		if remote.Status == "REJECTED" {
			if call.ParentCallID != "" && call.FinancialCommitment {
				return ErrProductFundingPending
			}
			if err := productTokenDelta(tx, call.TokenID, -call.TokenReserved); err != nil {
				return err
			}
			rejection = ErrProductFundingConflict
			return tx.Model(&call).Updates(map[string]any{"state": "rejected", "token_reserved": 0}).Error
		}
		if remote.Status != "RESERVED" {
			return ErrProductFundingPending
		}
		return tx.Model(&call).Updates(map[string]any{"state": "reserved", "updated_at": time.Now().Unix()}).Error
	})
	if err == nil && rejection != nil {
		return rejection
	}
	return err
}

func (s *ProductFundingStore) resolveProductTopUp(ctx context.Context, id string) error {
	var rejection error
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var op ProductFundingTopUp
		if err := tx.First(&op, "id = ?", id).Error; err != nil {
			return err
		}
		var call ProductRuntimeCall
		if err := lockForUpdate(tx).First(&call, "id = ?", op.CallID).Error; err != nil {
			return err
		}
		if call.ScopeDigest == "" || call.ScopeDigest != s.ScopeDigest() {
			return ErrProductFundingConflict
		}
		if err := lockForUpdate(tx).First(&op, "id = ?", id).Error; err != nil {
			return err
		}
		if op.State == "applied" {
			return nil
		}
		if op.State != "pending" {
			return ErrProductFundingConflict
		}
		remote, remoteErr := s.client.TopUp(ctx, call.ID, userkit.RuntimeTopUpRequest{RequestKey: op.ID, Amount: strconv.Itoa(op.Delta), AcceptedCall: op.AcceptedCall})
		if remoteErr != nil {
			confirmed, readErr := s.client.Call(ctx, call.ID)
			if readErr != nil {
				return remoteErr
			}
			if err := s.validateRemote(call, confirmed); err != nil {
				return err
			}
			held, err := productQuota(confirmed.Held)
			if err != nil {
				return err
			}
			// A terminal rejection plus unchanged hold proves this extension did
			// not execute. A timeout with unchanged hold is still uncertain.
			if productRuntimeDefiniteReject(remoteErr) && held == call.ReservedQuota && confirmed.Status == "RESERVED" {
				// Final observed usage already consumed this key's capacity.
				// Keep this immutable intent and debt until funding completes it.
				if op.AcceptedCall {
					return remoteErr
				}
				if err = productTokenDelta(tx, call.TokenID, -op.Delta); err != nil {
					return err
				}
				if err = tx.Model(&call).Update("token_reserved", call.TokenReserved-op.Delta).Error; err != nil {
					return err
				}
				rejection = remoteErr
				return tx.Model(&op).Updates(map[string]any{"state": "rejected", "updated_at": time.Now().Unix()}).Error
			}
			return remoteErr
		}
		if err := s.validateRemote(call, remote); err != nil {
			return err
		}
		held, err := productQuota(remote.Held)
		if err != nil {
			return err
		}
		if remote.Status != "RESERVED" || held < op.TargetQuota {
			return ErrProductFundingPending
		}
		if err = tx.Model(&call).Updates(map[string]any{"reserved_quota": op.TargetQuota, "updated_at": time.Now().Unix()}).Error; err != nil {
			return err
		}
		return tx.Model(&op).Updates(map[string]any{"state": "applied", "updated_at": time.Now().Unix()}).Error
	})
	if err == nil && rejection != nil {
		return rejection
	}
	return err
}

// MarkDispatched must complete before the supplier request can leave the product.
// Recovery never releases a hold after this marker, even after a process crash.
func (s *ProductFundingStore) MarkDispatched(ctx context.Context, id string) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var call ProductRuntimeCall
		if err := lockForUpdate(tx).First(&call, "id = ?", id).Error; err != nil {
			return err
		}
		if call.ScopeDigest == "" || call.ScopeDigest != s.ScopeDigest() {
			return ErrProductFundingConflict
		}
		if call.DispatchedAt != 0 || call.FinancialCommitment {
			return nil
		}
		if call.State != "reserved" || call.DispatchLeaseUntil <= time.Now().Unix() {
			return ErrProductFundingPending
		}
		if err := productPendingTopUp(tx, id); err != nil {
			return err
		}
		return tx.Model(&call).Update("dispatched_at", time.Now().Unix()).Error
	})
}
func MarkProductFundingDispatched(ctx context.Context, id string) error {
	if DB == nil || !DB.Migrator().HasTable(&ProductRuntimeCall{}) {
		return nil
	}
	var call ProductRuntimeCall
	err := DB.WithContext(ctx).First(&call, "id = ?", id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	client, e := platformaccount.RuntimeClient()
	if e != nil {
		return e
	}
	store := NewProductFundingStore(DB, client)
	// A wallet-first native admission can retain a rejected wallet candidate
	// after the original funding journal selected a purchased subscription.
	// Its subscription/native fences own dispatch; no central wallet hold exists
	// to mark. Never apply this exception to an ordinary rejected wallet call.
	if call.State == "rejected" && call.TokenReserved == 0 && call.ScopeDigest == store.ScopeDigest() && DB.Migrator().HasTable(&OCRJob{}) {
		var selected OCRJob
		err = DB.WithContext(ctx).Where("id = ? AND user_id = ? AND token_id = ?", id, call.UserID, call.TokenID).First(&selected).Error
		if err == nil && selected.Funding == "subscription" && selected.BillingState == "reserved" && selected.CentralAccountID == call.AccountID {
			return nil
		}
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
	}
	return store.MarkDispatched(ctx, id)
}

func (s *ProductFundingStore) recoverProductReserve(ctx context.Context, id string) error {
	shouldRelease := false
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var call ProductRuntimeCall
		if err := lockForUpdate(tx).First(&call, "id = ?", id).Error; err != nil {
			return err
		}
		if call.ScopeDigest == "" || call.ScopeDigest != s.ScopeDigest() {
			return ErrProductFundingConflict
		}
		if call.FinancialCommitment || call.DispatchedAt != 0 || call.DispatchLeaseUntil > time.Now().Unix() || call.State == "rejected" {
			return nil
		}
		if call.State != "reserve_pending" && call.State != "reserved" {
			return nil
		}
		if err := productPendingTopUp(tx, id); err != nil {
			return err
		}
		remote, err := s.client.Call(ctx, id)
		if productRuntimeNotFound(err) {
			if err = productTokenDelta(tx, call.TokenID, -call.TokenReserved); err != nil {
				return err
			}
			return tx.Model(&call).Updates(map[string]any{"state": "rejected", "token_reserved": 0, "updated_at": time.Now().Unix()}).Error
		}
		if err != nil {
			return err
		}
		if err = s.validateRemote(call, remote); err != nil {
			return err
		}
		if remote.Status == "REJECTED" {
			if err = productTokenDelta(tx, call.TokenID, -call.TokenReserved); err != nil {
				return err
			}
			return tx.Model(&call).Updates(map[string]any{"state": "rejected", "token_reserved": 0}).Error
		}
		if remote.Status != "RESERVED" {
			return ErrProductFundingPending
		}
		shouldRelease = true
		return tx.Model(&call).Updates(map[string]any{"state": "release_pending", "final_quota": 0, "final_usage": "undispatched_reservation_expired", "final_digest": productDigest("undispatched_reservation_expired")}).Error
	})
	if err != nil {
		return err
	}
	if shouldRelease {
		return s.reconcile(ctx, id)
	}
	return nil
}
