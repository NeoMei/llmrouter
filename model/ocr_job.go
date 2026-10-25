package model

import (
	"context"
	"errors"
	"math"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/go-redis/redis/v8"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	ErrOCRConflict       = errors.New("OCR immutable contract or state conflict")
	ErrOCRInsufficient   = errors.New("OCR quota insufficient")
	ErrOCRReconciliation = errors.New("OCR billing requires reconciliation")
	ErrOCRPending        = errors.New("OCR billing pending")
)

// OCRAdmission is an internal, already-authorized one-native-request contract.
// Callers must resolve current configured pricing and authorization before create.
// It contains no media, credentials or provider identity.
type OCRAdmission struct {
	IdempotencyKey                  string
	UserID, TokenID, ChannelID      int
	Model, Group, Preference        string
	Price, GroupRatio, QuotaPerUnit float64
}

type OCRJob struct {
	Execution        string `gorm:"type:text" json:"-"`
	Attempts         string `gorm:"type:text" json:"-"`
	SubmitAttempts   int
	MaxAttempts      int
	RetryRefusal     bool
	ProviderState    string `gorm:"size:24"`
	TotalPages       int
	ExtractedPages   int
	ResultURL        string `gorm:"type:text" json:"-"`
	ResultResourceID string `gorm:"size:36"`

	ID              string `gorm:"primaryKey;size:36"`
	UserID          int    `gorm:"uniqueIndex:idx_ocr_owner_key,priority:1"`
	IdempotencyKey  string `gorm:"size:128;uniqueIndex:idx_ocr_owner_key,priority:2"`
	TokenID         int
	ChannelID       int
	Contract        string `gorm:"type:text" json:"-"`
	Model           string `gorm:"size:128"`
	BillingGroup    string `gorm:"size:128"`
	Preference      string `gorm:"size:32"`
	Quota           int
	Redis           bool
	Funding         string `gorm:"size:32"`
	BillingState    string `gorm:"size:32;index"`
	SubmissionState string `gorm:"size:32"`
	MemberID        string `gorm:"size:128"`
	AccountBinding  string `gorm:"type:text" json:"-"`
	ClaimID         string `gorm:"size:36" json:"-"`
	ProviderJobID   string `gorm:"size:256" json:"-"`
	Decision        string `gorm:"size:32"`
	Reason          string `gorm:"size:64"`
	Version         int64
	CreatedAt       int64
	UpdatedAt       int64
}

// An applied operation is the receipt for its SQL delta. CacheAttempt is never
// reused for a money command, including when an attempted operation is uncertain.
type OCROperation struct {
	ID                     string `gorm:"primaryKey;size:36"`
	JobID                  string `gorm:"size:36;uniqueIndex:idx_ocr_component,priority:1"`
	Kind                   string `gorm:"size:24;uniqueIndex:idx_ocr_component,priority:2"`
	Phase                  string `gorm:"size:16;uniqueIndex:idx_ocr_component,priority:3"`
	Amount                 int
	OwnerID                int
	State                  string `gorm:"size:32"`
	ReserveID              string `gorm:"size:36"`
	SubscriptionGeneration int64
	CacheAttempt           string `gorm:"size:36"`
	CacheIncarnation       string `gorm:"size:36"`
	SQLAppliedAt           int64
	Reason                 string `gorm:"size:64"`
}

// Main DB is the authoritative audit sink. External projections are at-least-once
// unless the sink implements unique Event.ID receipts. Never replay money for logs.
type OCRAuditEvent struct {
	ID          string `gorm:"primaryKey;size:36"`
	JobID       string `gorm:"size:36;uniqueIndex"`
	Kind        string `gorm:"size:32"`
	Payload     string `gorm:"type:text"`
	ProjectedAt int64
	CreatedAt   int64
}

type OCRClaim struct{ JobID, ClaimID, MemberID string }
type OCRJournal struct {
	Job        OCRJob
	Operations []OCROperation
}

// OCRBillingStore owns its separate Redis pool. Close when the owning service
// shuts down; the shared application client and its options are never modified.
type OCRBillingStore struct {
	db    *gorm.DB
	cache *redis.Client
}

func NewOCRBillingStore(db *gorm.DB, shared *redis.Client) *OCRBillingStore {
	s := &OCRBillingStore{db: db}
	if shared != nil {
		options := *shared.Options()
		options.MaxRetries = -1
		s.cache = redis.NewClient(&options)
	}
	return s
}
func (s *OCRBillingStore) Close() error {
	if s.cache != nil {
		return s.cache.Close()
	}
	return nil
}

func (s *OCRBillingStore) CreateIntent(ctx context.Context, a OCRAdmission) (OCRJob, error) {
	if a.UserID <= 0 || a.TokenID <= 0 || a.ChannelID <= 0 || len(a.IdempotencyKey) == 0 || len(a.IdempotencyKey) > 128 || len(a.Model) == 0 || len(a.Model) > 128 || len(a.Group) > 128 {
		return OCRJob{}, ErrOCRConflict
	}
	for _, v := range []float64{a.Price, a.GroupRatio, a.QuotaPerUnit} {
		if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
			return OCRJob{}, ErrOCRConflict
		}
	}
	if a.QuotaPerUnit == 0 {
		return OCRJob{}, ErrOCRConflict
	}
	quota, clamp := common.QuotaFromFloatChecked(a.Price * a.GroupRatio * a.QuotaPerUnit)
	if clamp != nil {
		return OCRJob{}, clamp
	}
	if quota == 0 && a.Price > 0 && a.GroupRatio > 0 {
		return OCRJob{}, errors.New("OCR price below quota unit")
	}
	a.Preference = common.NormalizeBillingPreference(a.Preference)
	raw, err := common.Marshal(a)
	if err != nil {
		return OCRJob{}, err
	}
	job := OCRJob{ID: uuid.NewString(), UserID: a.UserID, TokenID: a.TokenID, ChannelID: a.ChannelID, IdempotencyKey: a.IdempotencyKey, Contract: string(raw), Model: a.Model, BillingGroup: a.Group, Preference: a.Preference, Quota: quota, Redis: s.cache != nil, BillingState: "pending", SubmissionState: "unclaimed", CreatedAt: time.Now().Unix(), UpdatedAt: time.Now().Unix()}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&job)
		if result.Error != nil {
			return result.Error
		}
		var existing OCRJob
		if err := lockForUpdate(tx).Where("user_id = ? AND idempotency_key = ?", a.UserID, a.IdempotencyKey).First(&existing).Error; err != nil {
			return err
		}
		if existing.Contract != job.Contract {
			return ErrOCRConflict
		}
		job = existing
		for _, kind := range []string{"wallet", "subscription", "token"} {
			owner := a.UserID
			if kind == "token" {
				owner = a.TokenID
			}
			op := OCROperation{ID: uuid.NewString(), JobID: job.ID, Kind: kind, Phase: "reserve", Amount: quota, OwnerID: owner, State: "prepared"}
			if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&op).Error; err != nil {
				return err
			}
		}
		return nil
	})
	return job, err
}

func (s *OCRBillingStore) Inspect(ctx context.Context, id string) (OCRJournal, error) {
	var j OCRJournal
	err := s.db.WithContext(ctx).Where("id = ?", id).First(&j.Job).Error
	if err != nil {
		return j, err
	}
	err = s.db.WithContext(ctx).Where("job_id = ?", id).Order("kind, phase").Find(&j.Operations).Error
	return j, err
}

// User-only phases must not subsequently acquire a subscription row. Funding
// phases use ocrLockJobForFunding, matching legacy subscription -> User writers.
func ocrLockJobAndFundingOwner(tx *gorm.DB, id string) (*OCRJob, error) {
	j, err := ocrLockJob(tx, id)
	if err != nil {
		return j, err
	}
	return j, lockOCRFundingOwner(tx, j.UserID)
}
func lockOCRFundingOwner(tx *gorm.DB, userID int) error {
	var owner User
	return lockForUpdate(tx).Unscoped().Select("id").First(&owner, userID).Error
}

// Freeze and lock the subscription candidates before User. Exact primary-key
// reads revalidate rows after concurrent invalidation/deletion; selection uses
// this snapshot exclusively and never locks a new candidate after User.
func ocrLockJobForFunding(tx *gorm.DB, id string) (*OCRJob, []UserSubscription, error) {
	j, err := ocrLockJob(tx, id)
	if err != nil {
		return j, nil, err
	}
	var subs []UserSubscription
	if j.Decision != "" {
		var reserve OCROperation
		result := tx.Where("job_id = ? AND kind = ? AND phase = ? AND state = ?", id, "subscription", "reserve", "applied").Limit(1).Find(&reserve)
		if result.Error != nil {
			return j, nil, result.Error
		}
		if result.RowsAffected > 0 {
			var original UserSubscription
			if err := lockForUpdate(tx).First(&original, reserve.OwnerID).Error; err != nil {
				return j, nil, err
			}
		}
	} else if j.Quota > 0 && j.Preference != "wallet_only" {
		var candidates []UserSubscription
		now := time.Now().Unix()
		if err := tx.Where("user_id = ? AND status = ? AND end_time > ?", j.UserID, "active", now).Order("end_time asc, id asc").Find(&candidates).Error; err != nil {
			return j, nil, err
		}
		for _, candidate := range candidates {
			var current UserSubscription
			err := lockForUpdate(tx).First(&current, candidate.Id).Error
			if errors.Is(err, gorm.ErrRecordNotFound) {
				continue
			}
			if err != nil {
				return j, nil, err
			}
			if current.UserId == j.UserID && current.Status == "active" && current.EndTime > now {
				subs = append(subs, current)
			}
		}
	}
	if err := lockOCRFundingOwner(tx, j.UserID); err != nil {
		return j, nil, err
	}
	return j, subs, nil
}
func ocrLockJob(tx *gorm.DB, id string) (*OCRJob, error) {
	var j OCRJob
	err := lockForUpdate(tx).Where("id = ?", id).First(&j).Error
	return &j, err
}
func ocrSaveJob(tx *gorm.DB, j *OCRJob) error {
	old := j.Version
	j.Version++
	j.UpdatedAt = time.Now().Unix()
	r := tx.Model(&OCRJob{}).Where("id = ? AND version = ?", j.ID, old).Select("*").Updates(j)
	if r.Error != nil {
		return r.Error
	}
	if r.RowsAffected != 1 {
		return ErrOCRConflict
	}
	return nil
}
func ocrOperation(tx *gorm.DB, id, kind, phase string) (*OCROperation, error) {
	var op OCROperation
	err := lockForUpdate(tx).Where("job_id = ? AND kind = ? AND phase = ?", id, kind, phase).First(&op).Error
	return &op, err
}
func ocrApplied(tx *gorm.DB, op *OCROperation) error {
	op.State = "applied"
	op.SQLAppliedAt = time.Now().Unix()
	return tx.Save(op).Error
}

// Subscription selection and journal are in the caller's transaction. This does
// not call the legacy preconsume helper, whose transaction is independently owned.
func reserveOCRSubscriptionTx(tx *gorm.DB, j *OCRJob, subs []UserSubscription) (bool, error) {
	op, err := ocrOperation(tx, j.ID, "subscription", "reserve")
	if err != nil {
		return false, err
	}
	if op.State == "applied" {
		j.Funding = "subscription"
		return true, nil
	}
	overflow := true
	for _, sub := range subs {
		if !sub.AllowWalletOverflow {
			overflow = false
		}
	}
	for _, sub := range subs {
		var plan SubscriptionPlan
		if err := tx.First(&plan, sub.PlanId).Error; err != nil {
			return false, err
		}
		if err := maybeResetUserSubscriptionWithPlanTx(tx, &sub, &plan, time.Now().Unix()); err != nil {
			return false, err
		}
		if sub.AmountTotal > 0 && sub.AmountTotal-sub.AmountUsed < int64(j.Quota) {
			continue
		}
		if sub.AmountUsed > math.MaxInt64-int64(j.Quota) {
			return false, ErrOCRReconciliation
		}
		sub.AmountUsed += int64(j.Quota)
		if err := tx.Save(&sub).Error; err != nil {
			return false, err
		}
		op.OwnerID = sub.Id
		op.SubscriptionGeneration = sub.ResetGeneration
		if err := ocrApplied(tx, op); err != nil {
			return false, err
		}
		j.Funding = "subscription"
		return true, nil
	}
	op.State = "rejected"
	op.Reason = "subscription_insufficient"
	if err := tx.Save(op).Error; err != nil {
		return false, err
	}
	if j.Preference == "subscription_only" || (!overflow && j.Preference == "subscription_first") {
		return false, ErrOCRInsufficient
	}
	return false, nil
}

func applyOCRQuotaTx(tx *gorm.DB, j *OCRJob, op *OCROperation, cacheAdmitted bool) error {
	if op.State == "applied" {
		return nil
	}
	delta := -op.Amount
	if op.Phase == "refund" {
		delta = op.Amount
	}
	if op.Kind == "wallet" {
		var owner User
		if err := lockForUpdate(tx).Unscoped().First(&owner, op.OwnerID).Error; err != nil {
			return err
		}
		if !cacheAdmitted && op.Phase == "reserve" && (owner.Status != common.UserStatusEnabled || owner.DeletedAt.Valid) {
			return ErrOCRConflict
		}
		if !cacheAdmitted && op.Phase == "reserve" && owner.Quota < op.Amount {
			return ErrOCRInsufficient
		}
		after := int64(owner.Quota) + int64(delta)
		if after < common.MinQuota || after > common.MaxQuota {
			return ErrOCRReconciliation
		}
		if delta != 0 {
			r := tx.Unscoped().Model(&User{}).Where("id = ?", owner.Id).Update("quota", gorm.Expr("quota + ?", delta))
			if r.Error != nil {
				return r.Error
			}
			if r.RowsAffected != 1 {
				return ErrOCRReconciliation
			}
		}
	} else if op.Kind == "token" {
		var token Token
		if err := lockForUpdate(tx).Unscoped().First(&token, op.OwnerID).Error; err != nil {
			return err
		}
		if token.UserId != j.UserID {
			return ErrOCRConflict
		}
		if !cacheAdmitted && op.Phase == "reserve" && (token.Status != common.TokenStatusEnabled || token.DeletedAt.Valid || (token.ExpiredTime != -1 && token.ExpiredTime <= time.Now().Unix())) {
			return ErrOCRConflict
		}
		if !cacheAdmitted && op.Phase == "reserve" && !token.UnlimitedQuota && token.RemainQuota < op.Amount {
			return ErrOCRInsufficient
		}
		remain, used := int64(token.RemainQuota)+int64(delta), int64(token.UsedQuota)-int64(delta)
		if remain < common.MinQuota || remain > common.MaxQuota || used < 0 || used > common.MaxQuota {
			return ErrOCRReconciliation
		}
		if delta != 0 {
			r := tx.Unscoped().Model(&Token{}).Where("id = ?", token.Id).Updates(map[string]any{"remain_quota": gorm.Expr("remain_quota + ?", delta), "used_quota": gorm.Expr("used_quota - ?", delta), "accessed_time": time.Now().Unix()})
			if r.Error != nil {
				return r.Error
			}
			if r.RowsAffected != 1 {
				return ErrOCRReconciliation
			}
		}
	} else {
		return ErrOCRConflict
	}
	return ocrApplied(tx, op)
}

func (s *OCRBillingStore) Reserve(ctx context.Context, id string) (OCRJob, error) {
	snapshot, err := s.Inspect(ctx, id)
	if err != nil {
		return OCRJob{}, err
	}
	if snapshot.Job.Redis != (s.cache != nil) {
		return snapshot.Job, ErrOCRReconciliation
	}
	if snapshot.Job.Redis {
		return s.reserveRedis(ctx, id)
	}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		j, subs, err := ocrLockJobForFunding(tx, id)
		if err != nil {
			return err
		}
		if j.BillingState == "reserved" {
			return nil
		}
		if j.BillingState != "pending" || j.Decision != "" {
			return ErrOCRConflict
		}
		if j.Quota == 0 {
			j.Funding = "wallet"
		} else if j.Preference == "subscription_only" || j.Preference == "subscription_first" {
			selected, err := reserveOCRSubscriptionTx(tx, j, subs)
			if err != nil {
				return err
			}
			if !selected {
				j.Funding = "wallet"
			}
		} else {
			j.Funding = "wallet"
		}
		if j.Funding == "wallet" {
			op, err := ocrOperation(tx, id, "wallet", "reserve")
			if err != nil {
				return err
			}
			err = applyOCRQuotaTx(tx, j, op, false)
			if errors.Is(err, ErrOCRInsufficient) && j.Preference == "wallet_first" {
				op.State = "rejected"
				op.Reason = "wallet_insufficient"
				if e := tx.Save(op).Error; e != nil {
					return e
				}
				selected, e := reserveOCRSubscriptionTx(tx, j, subs)
				if e != nil {
					return e
				}
				if !selected {
					return ErrOCRInsufficient
				}
			} else if err != nil {
				return err
			}
		}
		op, err := ocrOperation(tx, id, "token", "reserve")
		if err != nil {
			return err
		}
		if err = applyOCRQuotaTx(tx, j, op, false); err != nil {
			return err
		}
		j.BillingState = "reserved"
		return ocrSaveJob(tx, j)
	})
	if errors.Is(err, ErrOCRInsufficient) {
		_ = s.db.WithContext(ctx).Model(&OCRJob{}).Where("id = ? AND billing_state = ?", id, "pending").Updates(map[string]any{"billing_state": "rejected", "reason": "insufficient"})
	}
	out, readErr := s.Inspect(ctx, id)
	if err != nil {
		return out.Job, err
	}
	return out.Job, readErr
}

// OCRAccountBinding retains safe stable ownership separately from the selected
// health generation. These are trusted service inputs, never client identities.
type OCRAccountBinding struct {
	Ref        UpstreamAccountRef  `json:"ref"`
	Owner      *UpstreamAccountRef `json:"owner,omitempty"`
	Sequence   int64               `json:"sequence"`
	SelectedAt time.Time           `json:"selected_at"`
}

func (s *OCRBillingStore) ClaimSubmission(ctx context.Context, id string, selection UpstreamAccountSelection, owner ...UpstreamAccountRef) (OCRClaim, error) {
	ref := selection.Ref
	if _, err := uuid.Parse(ref.MemberID); err != nil {
		return OCRClaim{}, ErrOCRConflict
	}
	if ref.PoolID == "" || len(ref.PoolID) > 112 || ref.ChannelID <= 0 || ref.KeyIndex < 0 || selection.KeyIndex != ref.KeyIndex || selection.Sequence <= 0 || selection.SelectedAt.IsZero() || len(owner) > 1 {
		return OCRClaim{}, ErrOCRConflict
	}
	binding := OCRAccountBinding{Ref: ref, Sequence: selection.Sequence, SelectedAt: selection.SelectedAt}
	member := ref.MemberID
	if len(owner) == 1 {
		stable := owner[0]
		if _, err := uuid.Parse(stable.MemberID); err != nil {
			return OCRClaim{}, ErrOCRConflict
		}
		if stable.PoolID != ref.PoolID || stable.ChannelID != ref.ChannelID || stable.KeyIndex != ref.KeyIndex {
			return OCRClaim{}, ErrOCRConflict
		}
		binding.Owner = &stable
		member = stable.MemberID
	}
	raw, err := common.Marshal(binding)
	if err != nil {
		return OCRClaim{}, err
	}

	if err := s.validateCacheReceipts(ctx, id); err != nil {
		return OCRClaim{}, err
	}
	claim := OCRClaim{JobID: id, ClaimID: uuid.NewString(), MemberID: member}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		j, err := ocrLockJobAndFundingOwner(tx, id)
		if err != nil {
			return err
		}
		if j.ChannelID != ref.ChannelID {
			return ErrOCRConflict
		}
		if j.BillingState != "reserved" || (j.SubmissionState != "unclaimed" && !(j.SubmissionState == "rejected" && j.RetryRefusal)) || j.Decision != "" {
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
		var owner User
		if err := lockForUpdate(tx).First(&owner, j.UserID).Error; err != nil {
			return err
		}
		var token Token
		if err := lockForUpdate(tx).First(&token, j.TokenID).Error; err != nil {
			return err
		}
		if owner.Status != common.UserStatusEnabled || token.UserId != j.UserID || token.Status != common.TokenStatusEnabled || (token.ExpiredTime != -1 && token.ExpiredTime <= time.Now().Unix()) {
			return ErrOCRConflict
		}
		var attempts []OCRSubmissionAttempt
		if j.Attempts != "" {
			if err := common.UnmarshalJsonStr(j.Attempts, &attempts); err != nil {
				return err
			}
		}
		budget := j.MaxAttempts
		if budget == 0 {
			budget = 1
		}
		if len(attempts) >= budget {
			return ErrOCRConflict
		}
		for _, prior := range attempts {
			if prior.MemberID == member || prior.HealthMemberID == ref.MemberID {
				return ErrOCRConflict
			}
		}
		attempts = append(attempts, OCRSubmissionAttempt{ClaimID: claim.ClaimID, MemberID: member, HealthMemberID: ref.MemberID, Binding: binding, State: "submitting", CreatedAt: time.Now().Unix()})
		encoded, err := common.Marshal(attempts)
		if err != nil {
			return err
		}
		j.Attempts, j.SubmitAttempts, j.RetryRefusal = string(encoded), len(attempts), false
		j.SubmissionState = "submitting"
		j.MemberID = member
		j.AccountBinding = string(raw)
		j.ClaimID = claim.ClaimID
		return ocrSaveJob(tx, j)
	})
	return claim, err
}
func (s *OCRBillingStore) RecordSubmission(ctx context.Context, c OCRClaim, providerID string, uncertain bool) error {
	if len(providerID) > 256 || (!uncertain && providerID == "") {
		return ErrOCRConflict
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		j, err := ocrLockJob(tx, c.JobID)
		if err != nil {
			return err
		}
		if j.ClaimID != c.ClaimID || j.MemberID != c.MemberID || j.Decision != "" {
			return ErrOCRConflict
		}
		if j.SubmissionState != "submitting" && j.SubmissionState != "uncertain" && j.SubmissionState != "submitted" {
			return ErrOCRConflict
		}
		if j.ProviderJobID != "" && j.ProviderJobID != providerID {
			return ErrOCRConflict
		}
		if uncertain {
			if j.SubmissionState == "submitted" {
				return ErrOCRConflict
			}
			j.SubmissionState = "uncertain"
		} else {
			j.SubmissionState = "submitted"
			j.ProviderJobID = providerID
		}
		if err := ocrUpdateAttempt(j, j.SubmissionState, 0); err != nil {
			return err
		}
		return ocrSaveJob(tx, j)
	})
}

func refundOCRSubscriptionTx(tx *gorm.DB, op *OCROperation) error {
	if op.State == "applied" {
		return nil
	}
	var sub UserSubscription
	if err := lockForUpdate(tx).First(&sub, op.OwnerID).Error; err != nil {
		return err
	}
	if sub.ResetGeneration < op.SubscriptionGeneration {
		return ErrOCRReconciliation
	}
	if sub.ResetGeneration == op.SubscriptionGeneration {
		if sub.AmountUsed < int64(op.Amount) {
			return ErrOCRReconciliation
		}
		sub.AmountUsed -= int64(op.Amount)
		if err := tx.Save(&sub).Error; err != nil {
			return err
		}
	} else {
		op.Reason = "released_prior_period"
	}
	return ocrApplied(tx, op)
}
func ocrFinalizeTx(tx *gorm.DB, j *OCRJob) error {
	if j.Decision == "success" {
		var user User
		if err := lockForUpdate(tx).Unscoped().First(&user, j.UserID).Error; err != nil {
			return err
		}
		var ch Channel
		if err := lockForUpdate(tx).Select("id", "used_quota").First(&ch, j.ChannelID).Error; err != nil {
			return err
		}
		if int64(user.UsedQuota)+int64(j.Quota) > common.MaxQuota || user.RequestCount >= common.MaxQuota || int64(ch.UsedQuota)+int64(j.Quota) > common.MaxQuota {
			return ErrOCRReconciliation
		}
		if err := tx.Unscoped().Model(&User{}).Where("id = ?", j.UserID).Updates(map[string]any{"used_quota": gorm.Expr("used_quota + ?", j.Quota), "request_count": gorm.Expr("request_count + 1")}).Error; err != nil {
			return err
		}
		if j.Quota != 0 {
			r := tx.Model(&Channel{}).Where("id = ?", j.ChannelID).Update("used_quota", gorm.Expr("used_quota + ?", j.Quota))
			if r.Error != nil {
				return r.Error
			}
			if r.RowsAffected != 1 {
				return ErrOCRReconciliation
			}
		}
		j.BillingState = "settled"
	} else {
		j.BillingState = "refunded"
	}
	payload, err := common.Marshal(map[string]any{"job_id": j.ID, "user_id": j.UserID, "token_id": j.TokenID, "channel_id": j.ChannelID, "model": j.Model, "quota": j.Quota, "funding": j.Funding, "outcome": j.Decision})
	if err != nil {
		return err
	}
	if err := tx.Create(&OCRAuditEvent{ID: uuid.NewString(), JobID: j.ID, Kind: j.Decision, Payload: string(payload), CreatedAt: time.Now().Unix()}).Error; err != nil {
		return err
	}
	return ocrSaveJob(tx, j)
}
func (s *OCRBillingStore) Finish(ctx context.Context, id, outcome string) (OCRJob, error) {
	if outcome != "success" && outcome != "failure" && outcome != "cancel" {
		return OCRJob{}, ErrOCRConflict
	}
	mode, err := s.Inspect(ctx, id)
	if err != nil {
		return OCRJob{}, err
	}
	if mode.Job.Redis != (s.cache != nil) {
		return mode.Job, ErrOCRReconciliation
	}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		j, err := ocrLockJobAndFundingOwner(tx, id)
		if err != nil {
			return err
		}
		if j.Decision != "" && j.Decision != outcome {
			return ErrOCRConflict
		}
		if j.BillingState == "settled" || j.BillingState == "refunded" {
			return nil
		}
		if outcome == "cancel" && j.SubmissionState != "unclaimed" {
			return ErrOCRConflict
		}
		if outcome == "success" && j.SubmissionState != "submitted" {
			return ErrOCRConflict
		}
		if outcome == "failure" && j.SubmissionState != "submitted" && j.SubmissionState != "rejected" {
			return ErrOCRConflict
		}
		if outcome == "success" && j.BillingState != "reserved" {
			return ErrOCRReconciliation
		}
		j.Decision = outcome
		if outcome == "success" {
			return ocrFinalizeTx(tx, j)
		}
		var reserves []OCROperation
		if err := lockForUpdate(tx).Where("job_id = ? AND phase = ?", id, "reserve").Order("kind").Find(&reserves).Error; err != nil {
			return err
		}
		for _, reserve := range reserves {
			if reserve.State != "applied" {
				continue
			}
			op := OCROperation{ID: uuid.NewString(), JobID: id, Kind: reserve.Kind, Phase: "refund", Amount: reserve.Amount, OwnerID: reserve.OwnerID, State: "prepared", ReserveID: reserve.ID, SubscriptionGeneration: reserve.SubscriptionGeneration}
			if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&op).Error; err != nil {
				return err
			}
		}
		j.BillingState = "refunding"
		return ocrSaveJob(tx, j)
	})
	if err != nil {
		return OCRJob{}, err
	}
	current, err := s.Inspect(ctx, id)
	if err != nil {
		return OCRJob{}, err
	}
	if current.Job.BillingState == "settled" || current.Job.BillingState == "refunded" {
		return current.Job, nil
	}
	if current.Job.Redis {
		return s.refundRedis(ctx, id)
	}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		j, _, err := ocrLockJobForFunding(tx, id)
		if err != nil {
			return err
		}
		if j.BillingState == "refunded" {
			return nil
		}
		var ops []OCROperation
		if err := lockForUpdate(tx).Where("job_id = ? AND phase = ?", id, "refund").Order("CASE kind WHEN 'wallet' THEN 0 WHEN 'subscription' THEN 1 ELSE 2 END").Find(&ops).Error; err != nil {
			return err
		}
		for i := range ops {
			if ops[i].Kind == "subscription" {
				err = refundOCRSubscriptionTx(tx, &ops[i])
			} else {
				err = applyOCRQuotaTx(tx, j, &ops[i], false)
			}
			if err != nil {
				return err
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

// RecordAdmissionRejection keeps sanitized admission/saturation evidence in the
// authoritative main DB. It has no financial operation and cannot retry money.
func (s *OCRBillingStore) RecordAdmissionRejection(ctx context.Context, userID, tokenID int, modelName string, reason map[string]any) error {
	if userID <= 0 || tokenID <= 0 || len(modelName) > 128 {
		return ErrOCRConflict
	}
	payload, err := common.Marshal(map[string]any{"user_id": userID, "token_id": tokenID, "model": modelName, "rejection": reason})
	if err != nil {
		return err
	}
	id := uuid.NewString()
	return s.db.WithContext(ctx).Create(&OCRAuditEvent{ID: id, JobID: id, Kind: "admission_rejected", Payload: string(payload), CreatedAt: time.Now().Unix()}).Error
}

// RecordSubmissionFailure accepts only a known definitive supplier rejection,
// never a transport timeout. It preserves the original durable claim capability.
func (s *OCRBillingStore) RecordSubmissionFailure(ctx context.Context, c OCRClaim) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		j, err := ocrLockJob(tx, c.JobID)
		if err != nil {
			return err
		}
		if j.ClaimID != c.ClaimID || j.MemberID != c.MemberID || j.Decision != "" || (j.SubmissionState != "submitting" && j.SubmissionState != "rejected") {
			return ErrOCRConflict
		}
		j.SubmissionState = "rejected"
		j.RetryRefusal = false
		if err := ocrUpdateAttempt(j, "rejected", 0); err != nil {
			return err
		}
		return ocrSaveJob(tx, j)
	})
}
