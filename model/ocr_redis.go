package model

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// Receipt TTL is deliberately finite. Expiry means reconciliation, never
// permission to replay. A live receipt is checked against the current hash
// incarnation; it cannot prove safety after an unobserved Redis restore.
const ocrMoneyScript = `
local typ = redis.call('TYPE', KEYS[1]).ok
if typ ~= 'hash' then return 'pending' end
if redis.call('HGET', KEYS[1], 'Id') ~= ARGV[1] then return 'pending' end
local kind,phase,amount = ARGV[2],ARGV[3],tonumber(ARGV[4])
local function integer(v)
 if not v or not string.match(v, '^%-?%d+$') then return nil end
 local n=tonumber(v)
 if not n or n < -2147483648 or n > 2147483647 then return nil end
 return n
end
local remain,used
if kind == 'wallet' then
 if redis.call('HGET',KEYS[1],'CacheSchema') ~= ARGV[10] then return 'pending' end
 remain=integer(redis.call('HGET',KEYS[1],'Quota'))
else
 if redis.call('HGET',KEYS[1],'UserId') ~= ARGV[11] then return 'pending' end
 remain=integer(redis.call('HGET',KEYS[1],'RemainQuota'))
 used=integer(redis.call('HGET',KEYS[1],'UsedQuota'))
 if not used then return 'invalid' end
end
if not remain or not amount or amount < 0 or amount > 2147483647 then return 'invalid' end
if phase == 'reserve' then
 if redis.call('HGET',KEYS[1],'Status') ~= ARGV[12] then return 'pending' end
 if kind == 'token' and redis.call('HGET',KEYS[1],'UnlimitedQuota') ~= ARGV[5] then return 'pending' end
 if (kind == 'wallet' or ARGV[5] ~= 'true') and remain < amount then return 'insufficient' end
end
local delta=amount
if phase == 'reserve' then delta=-amount end
local after=remain+delta
if after < -2147483648 or after > 2147483647 then return 'invalid' end
if used and (used-delta < 0 or used-delta > 2147483647) then return 'invalid' end
local incarnation=redis.call('HGET',KEYS[1],'OCRIncarnation')
if phase == 'refund' and (not incarnation or incarnation ~= ARGV[13]) then return 'pending' end
if not incarnation then incarnation=ARGV[6] end
local receipt=ARGV[7]..'|'..incarnation
-- All identity, type and arithmetic validation precedes any mutation.
if kind == 'wallet' then
 redis.call('HSET',KEYS[1],'Quota',string.format('%.0f',after),'OCRIncarnation',incarnation)
else
 redis.call('HSET',KEYS[1],'RemainQuota',string.format('%.0f',after),'UsedQuota',string.format('%.0f',used-delta),'AccessedTime',ARGV[8],'OCRIncarnation',incarnation)
end
redis.call('SET',KEYS[2],receipt,'EX',ARGV[9])
return receipt
`

func ocrReceiptKey(op *OCROperation) string { return "ocr:receipt:" + op.ID }
func ocrReceiptPrefix(op *OCROperation) string {
	return op.CacheAttempt + "|" + op.Kind + "|" + op.Phase + "|" + strconv.Itoa(op.Amount)
}

// Hydration is before the durable money attempt. It never overwrites a live
// hash, respects legacy auth/token fences, and is forbidden during refund or
// recovery. It is not a fix for loss of ordinary process-memory batch deltas.
func (s *OCRBillingStore) ocrCacheAccount(ctx context.Context, j *OCRJob, op *OCROperation, hydrate bool) (string, bool, error) {
	if s.cache == nil {
		return "", false, ErrOCRPending
	}
	var key, fence, version string
	var fields map[string]any
	unlimited := false
	if op.Kind == "wallet" {
		var u User
		if err := s.db.WithContext(ctx).Unscoped().First(&u, op.OwnerID).Error; err != nil {
			return "", false, err
		}
		if op.Phase == "reserve" && (u.DeletedAt.Valid || u.Status != common.UserStatusEnabled) {
			return "", false, ErrOCRConflict
		}
		key = getUserCacheKey(u.Id)
		fence = getUserAuthFenceKey(u.Id)
		version = getUserAuthVersionKey(u.Id)
		fields = map[string]any{"Id": u.Id, "Group": u.Group, "Email": u.Email, "Quota": u.Quota, "Status": u.Status, "Role": u.Role, "Username": u.Username, "Setting": u.Setting, "AuthVersion": u.AuthVersion, "CacheSchema": userCacheSchemaVersion}
	} else {
		var tok Token
		if err := s.db.WithContext(ctx).Unscoped().First(&tok, op.OwnerID).Error; err != nil {
			return "", false, err
		}
		if tok.UserId != j.UserID || tok.Key == "" {
			return "", false, ErrOCRConflict
		}
		if op.Phase == "reserve" && (tok.DeletedAt.Valid || tok.Status != common.TokenStatusEnabled || (tok.ExpiredTime != -1 && tok.ExpiredTime <= time.Now().Unix())) {
			return "", false, ErrOCRConflict
		}
		unlimited = tok.UnlimitedQuota
		key = getTokenCacheKey(tok.Key)
		fence = getTokenCacheFenceKey(tok.Key)
		version = fence
		ips := ""
		if tok.AllowIps != nil {
			ips = *tok.AllowIps
		}
		fields = map[string]any{"Id": tok.Id, "UserId": tok.UserId, "Status": tok.Status, "Name": tok.Name, "CreatedTime": tok.CreatedTime, "AccessedTime": tok.AccessedTime, "ExpiredTime": tok.ExpiredTime, "UnlimitedQuota": strconv.FormatBool(tok.UnlimitedQuota), "ModelLimitsEnabled": strconv.FormatBool(tok.ModelLimitsEnabled), "ModelLimits": tok.ModelLimits, "AllowIps": ips, "Group": tok.Group, "CrossGroupRetry": strconv.FormatBool(tok.CrossGroupRetry), "RemainQuota": tok.RemainQuota, "UsedQuota": tok.UsedQuota}
	}
	if hydrate {
		raw, err := common.Marshal(fields)
		if err != nil {
			return "", false, err
		}
		const script = `
local f=cjson.decode(ARGV[1])
if ARGV[2]=='token' then
 if redis.call('EXISTS',KEYS[2])==1 then return 0 end
else
 local pending=tonumber(redis.call('GET',KEYS[2]) or '0')
 local committed=tonumber(redis.call('GET',KEYS[3]) or '0')
 if not pending or not committed or pending>tonumber(f.AuthVersion) or committed>tonumber(f.AuthVersion) then return 0 end
end
if redis.call('EXISTS',KEYS[1])==1 then return 1 end
local args={}
for k,v in pairs(f) do table.insert(args,k);table.insert(args,tostring(v)) end
redis.call('HSET',KEYS[1],unpack(args))
redis.call('EXPIRE',KEYS[1],ARGV[3])
return 1`
		ok, err := s.cache.Eval(ctx, script, []string{key, fence, version}, string(raw), op.Kind, userCacheTTLSeconds()).Int()
		if err != nil {
			return "", false, err
		}
		if ok != 1 {
			return "", false, ErrOCRPending
		}
	}
	return key, unlimited, nil
}

func (s *OCRBillingStore) holdOCR(ctx context.Context, id, reason string) error {
	return s.db.WithContext(ctx).Model(&OCRJob{}).Where("id = ? AND billing_state NOT IN ?", id, []string{"settled", "refunded"}).Updates(map[string]any{"billing_state": "reconciliation", "reason": reason}).Error
}
func (s *OCRBillingStore) receiptEvidence(ctx context.Context, j *OCRJob, op *OCROperation) (string, error) {
	key, _, err := s.ocrCacheAccount(ctx, j, op, false)
	if err != nil {
		return "", ErrOCRReconciliation
	}
	// One read-only Lua invocation keeps receipt/incarnation comparison consistent.
	const script = `local r=redis.call('GET',KEYS[2]);if not r then return '' end;local i=redis.call('HGET',KEYS[1],'OCRIncarnation');if not i then return '' end;if r~=ARGV[1]..'|'..i then return '' end;return i`
	inc, err := s.cache.Eval(ctx, script, []string{key, ocrReceiptKey(op)}, ocrReceiptPrefix(op)).Text()
	if err != nil || inc == "" || (op.CacheIncarnation != "" && inc != op.CacheIncarnation) {
		return "", ErrOCRReconciliation
	}
	return inc, nil
}
func (s *OCRBillingStore) validateCacheReceipts(ctx context.Context, id string) error {
	snap, err := s.Inspect(ctx, id)
	if err != nil {
		return err
	}
	if snap.Job.Redis != (s.cache != nil) {
		return ErrOCRReconciliation
	}
	if !snap.Job.Redis {
		return nil
	}
	for i := range snap.Operations {
		op := &snap.Operations[i]
		if op.Phase != "reserve" || op.State != "applied" || op.Kind == "subscription" || op.Amount == 0 {
			continue
		}
		if _, err := s.receiptEvidence(ctx, &snap.Job, op); err != nil {
			_ = s.holdOCR(ctx, id, "cache_evidence_lost")
			return err
		}
	}
	return nil
}

// applyOCRCacheOperation is the only money EVAL caller. A committed attempt
// claim can only be inspected afterwards, never automatically issued again.
func (s *OCRBillingStore) applyOCRCacheOperation(ctx context.Context, id, kind, phase string) error {
	snap, err := s.Inspect(ctx, id)
	if err != nil {
		return err
	}
	j := snap.Job
	op, err := ocrOperation(s.db.WithContext(ctx), id, kind, phase)
	if err != nil {
		return err
	}
	if op.State == "applied" {
		return nil
	}
	if op.State == "rejected" {
		return ErrOCRInsufficient
	}
	if op.Amount == 0 {
		return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			locked, err := ocrLockJob(tx, id)
			if err != nil {
				return err
			}
			current, err := ocrOperation(tx, id, kind, phase)
			if err != nil {
				return err
			}
			return applyOCRQuotaTx(tx, locked, current, false)
		})
	}
	var incarnation string
	if op.CacheAttempt != "" {
		incarnation, err = s.receiptEvidence(ctx, &j, op)
		if err != nil {
			_ = s.holdOCR(ctx, id, "cache_attempt_unknown")
			return err
		}
	} else {
		key, unlimited, err := s.ocrCacheAccount(ctx, &j, op, phase == "reserve")
		if err != nil {
			return err
		}
		if phase == "refund" {
			reserve, err := ocrOperation(s.db.WithContext(ctx), id, kind, "reserve")
			if err != nil {
				return err
			}
			if _, err = s.receiptEvidence(ctx, &j, reserve); err != nil {
				_ = s.holdOCR(ctx, id, "refund_cache_evidence_lost")
				return err
			}
			op.CacheIncarnation = reserve.CacheIncarnation
		}
		attempt := uuid.NewString()
		err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			current, err := ocrLockJob(tx, id)
			if err != nil {
				return err
			}
			if phase == "reserve" && (current.Decision != "" || current.BillingState == "refunding" || current.BillingState == "refunded") {
				return ErrOCRConflict
			}
			if phase == "refund" && (current.Decision == "" || current.Decision == "success") {
				return ErrOCRConflict
			}
			r := tx.Model(&OCROperation{}).Where("id = ? AND state = ? AND cache_attempt = ?", op.ID, "prepared", "").Updates(map[string]any{"state": "cache_attempting", "cache_attempt": attempt, "cache_incarnation": op.CacheIncarnation})
			if r.Error != nil {
				return r.Error
			}
			if r.RowsAffected != 1 {
				return ErrOCRPending
			}
			return nil
		})
		if err != nil {
			return err
		}
		op.CacheAttempt = attempt
		status := common.UserStatusEnabled
		if kind == "token" {
			status = common.TokenStatusEnabled
		}
		reply, evalErr := s.cache.Eval(ctx, ocrMoneyScript, []string{key, ocrReceiptKey(op)}, op.OwnerID, kind, phase, op.Amount, strconv.FormatBool(unlimited), uuid.NewString(), ocrReceiptPrefix(op), time.Now().Unix(), 7*24*60*60, userCacheSchemaVersion, j.UserID, status, op.CacheIncarnation).Text()
		if evalErr != nil {
			_ = s.holdOCR(ctx, id, "cache_attempt_unknown")
			return ErrOCRReconciliation
		}
		switch reply {
		case "insufficient":
			if err := s.db.WithContext(ctx).Model(&OCROperation{}).Where("id = ? AND cache_attempt = ?", op.ID, attempt).Updates(map[string]any{"state": "rejected", "reason": "insufficient"}).Error; err != nil {
				return err
			}
			return ErrOCRInsufficient
		case "pending", "invalid":
			// A definite no-effect script result is retained. A fresh invocation requires
			// operator resolution, avoiding unsafe automatic resets of attempt claims.
			_ = s.db.WithContext(ctx).Model(&OCROperation{}).Where("id = ?", op.ID).Updates(map[string]any{"state": "not_applied", "reason": "cache_not_ready"}).Error
			_ = s.holdOCR(ctx, id, "cache_not_ready")
			return ErrOCRReconciliation
		}
		prefix := ocrReceiptPrefix(op) + "|"
		if !strings.HasPrefix(reply, prefix) {
			_ = s.holdOCR(ctx, id, "cache_response_unknown")
			return ErrOCRReconciliation
		}
		incarnation = strings.TrimPrefix(reply, prefix)
		if len(incarnation) != 36 {
			_ = s.holdOCR(ctx, id, "cache_response_unknown")
			return ErrOCRReconciliation
		}
	}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		current, err := ocrLockJob(tx, id)
		if err != nil {
			return err
		}
		locked, err := ocrOperation(tx, id, kind, phase)
		if err != nil {
			return err
		}
		if locked.State == "applied" {
			return nil
		}
		locked.CacheIncarnation = incarnation
		return applyOCRQuotaTx(tx, current, locked, true)
	})
	if err != nil {
		_ = s.holdOCR(ctx, id, "cache_sql_gap")
		return err
	}
	return nil
}

func (s *OCRBillingStore) reserveRedis(ctx context.Context, id string) (OCRJob, error) {
	snap, err := s.Inspect(ctx, id)
	if err != nil {
		return OCRJob{}, err
	}
	if snap.Job.Decision != "" || snap.Job.BillingState == "rejected" {
		return snap.Job, ErrOCRConflict
	}
	if snap.Job.BillingState == "reserved" {
		err = s.validateCacheReceipts(ctx, id)
		return snap.Job, err
	}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		j, err := ocrLockJob(tx, id)
		if err != nil {
			return err
		}
		if j.Decision != "" {
			return ErrOCRConflict
		}
		if j.Funding != "" {
			return nil
		}
		j.Funding = "wallet"
		if j.Quota > 0 && (j.Preference == "subscription_first" || j.Preference == "subscription_only") {
			selected, err := reserveOCRSubscriptionTx(tx, j)
			if err != nil {
				return err
			}
			if !selected {
				j.Funding = "wallet"
			}
		}
		return ocrSaveJob(tx, j)
	})
	if err == nil {
		snap, err = s.Inspect(ctx, id)
		if err == nil && snap.Job.Funding == "wallet" {
			err = s.applyOCRCacheOperation(ctx, id, "wallet", "reserve")
			if errors.Is(err, ErrOCRInsufficient) && snap.Job.Preference == "wallet_first" {
				err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
					j, err := ocrLockJob(tx, id)
					if err != nil {
						return err
					}
					if j.Decision != "" {
						return ErrOCRConflict
					}
					selected, err := reserveOCRSubscriptionTx(tx, j)
					if err != nil {
						return err
					}
					if !selected {
						return ErrOCRInsufficient
					}
					return ocrSaveJob(tx, j)
				})
			}
		}
	}
	if err == nil {
		err = s.applyOCRCacheOperation(ctx, id, "token", "reserve")
	}
	if err == nil {
		err = s.validateCacheReceipts(ctx, id)
	}
	if err == nil {
		err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			j, err := ocrLockJob(tx, id)
			if err != nil {
				return err
			}
			if j.Decision != "" {
				return ErrOCRConflict
			}
			for _, kind := range []string{j.Funding, "token"} {
				op, err := ocrOperation(tx, id, kind, "reserve")
				if err != nil {
					return err
				}
				if op.State != "applied" {
					return ErrOCRReconciliation
				}
			}
			j.BillingState = "reserved"
			j.Reason = ""
			return ocrSaveJob(tx, j)
		})
	}
	if errors.Is(err, ErrOCRInsufficient) {
		_ = s.holdOCR(ctx, id, "insufficient_partial_hold")
	}
	out, readErr := s.Inspect(ctx, id)
	if err != nil {
		return out.Job, err
	}
	return out.Job, readErr
}

func (s *OCRBillingStore) refundRedis(ctx context.Context, id string) (OCRJob, error) {
	snap, err := s.Inspect(ctx, id)
	if err != nil {
		return OCRJob{}, err
	}
	for _, op := range snap.Operations {
		if op.Phase != "refund" {
			continue
		}
		if op.Kind == "subscription" {
			err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
				_, err := ocrLockJob(tx, id)
				if err != nil {
					return err
				}
				locked, err := ocrOperation(tx, id, "subscription", "refund")
				if err != nil {
					return err
				}
				return refundOCRSubscriptionTx(tx, locked)
			})
		} else {
			err = s.applyOCRCacheOperation(ctx, id, op.Kind, "refund")
		}
		if err != nil {
			_ = s.holdOCR(ctx, id, "refund_unresolved")
			out, _ := s.Inspect(ctx, id)
			return out.Job, err
		}
	}
	// Check the completed credit receipts before declaring the refund terminal.
	// SQL application alone does not certify the current cache incarnation.
	refreshed, err := s.Inspect(ctx, id)
	if err != nil {
		return OCRJob{}, err
	}
	for i := range refreshed.Operations {
		op := &refreshed.Operations[i]
		if op.Phase == "refund" && op.State == "applied" && op.Kind != "subscription" && op.Amount > 0 {
			if _, err := s.receiptEvidence(ctx, &refreshed.Job, op); err != nil {
				_ = s.holdOCR(ctx, id, "refund_cache_evidence_lost")
				out, _ := s.Inspect(ctx, id)
				return out.Job, err
			}
		}
	}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		j, err := ocrLockJob(tx, id)
		if err != nil {
			return err
		}
		if j.BillingState == "refunded" {
			return nil
		}
		var ops []OCROperation
		if err := tx.Where("job_id = ?", id).Find(&ops).Error; err != nil {
			return err
		}
		releases := make(map[string]bool)
		for _, op := range ops {
			if op.Phase == "refund" && op.State == "applied" {
				releases[op.ReserveID] = true
			}
		}
		for _, op := range ops {
			if op.Phase == "reserve" && op.State == "applied" && !releases[op.ID] {
				return ErrOCRReconciliation
			}
			if op.Phase == "reserve" && (op.State == "cache_attempting" || op.State == "reconciliation") {
				return ErrOCRReconciliation
			}
			if op.Phase == "refund" && op.State != "applied" {
				return ErrOCRReconciliation
			}
		}
		return ocrFinalizeTx(tx, j)
	})
	if err != nil {
		_ = s.holdOCR(ctx, id, "refund_unresolved")
	}
	out, _ := s.Inspect(ctx, id)
	return out.Job, err
}

// ProjectAudit presents durable events to an external sink. The sink MUST use
// event.ID for deduplication if exactly-once projection is required. A crash
// after emit and before acknowledgement can deliver that event again.
func (s *OCRBillingStore) ProjectAudit(ctx context.Context, limit int, emit func(context.Context, OCRAuditEvent) error) error {
	if limit < 1 || limit > 100 || emit == nil {
		return fmt.Errorf("invalid OCR audit projection")
	}
	var events []OCRAuditEvent
	if err := s.db.WithContext(ctx).Where("projected_at = ?", 0).Order("created_at, id").Limit(limit).Find(&events).Error; err != nil {
		return err
	}
	for _, event := range events {
		if err := emit(ctx, event); err != nil {
			return err
		}
		if err := s.db.WithContext(ctx).Model(&OCRAuditEvent{}).Where("id = ? AND projected_at = ?", event.ID, 0).Update("projected_at", time.Now().Unix()).Error; err != nil {
			return err
		}
	}
	return nil
}
