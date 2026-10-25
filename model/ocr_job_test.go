package model

import (
	"context"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/glebarez/sqlite"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func ocrFixture(t *testing.T) (*OCRBillingStore, OCRAdmission) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "ocr.db")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	require.NoError(t, db.AutoMigrate(&User{}, &Token{}, &Channel{}, &SubscriptionPlan{}, &UserSubscription{}, &OCRJob{}, &OCROperation{}, &OCRAuditEvent{}))
	user := User{Username: uuid.NewString(), Quota: 1000, Status: common.UserStatusEnabled, AuthVersion: 1, AffCode: uuid.NewString()}
	require.NoError(t, db.Create(&user).Error)
	token := Token{UserId: user.Id, Key: uuid.NewString(), RemainQuota: 500, Status: common.TokenStatusEnabled, ExpiredTime: -1}
	require.NoError(t, db.Create(&token).Error)
	ch := Channel{Name: "ocr fixture"}
	require.NoError(t, db.Create(&ch).Error)
	store := NewOCRBillingStore(db, nil)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	return store, OCRAdmission{IdempotencyKey: uuid.NewString(), UserID: user.Id, TokenID: token.Id, ChannelID: ch.Id, Model: "ocr-native", Group: "default", Preference: "wallet_only", Price: 0.0002, GroupRatio: 1, QuotaPerUnit: 500000}
}
func ocrBalances(t *testing.T, s *OCRBillingStore, a OCRAdmission, wallet, remain, used int) {
	t.Helper()
	var u User
	var tok Token
	require.NoError(t, s.db.Unscoped().First(&u, a.UserID).Error)
	require.NoError(t, s.db.Unscoped().First(&tok, a.TokenID).Error)
	assert.Equal(t, wallet, u.Quota)
	assert.Equal(t, remain, tok.RemainQuota)
	assert.Equal(t, used, tok.UsedQuota)
}

func TestOCRWalletJournalAndTerminalIdempotency(t *testing.T) {
	s, a := ocrFixture(t)
	ctx := context.Background()
	job, err := s.CreateIntent(ctx, a)
	require.NoError(t, err)
	same, err := s.CreateIntent(ctx, a)
	require.NoError(t, err)
	assert.Equal(t, job.ID, same.ID)
	a.Price = 0.0004
	_, err = s.CreateIntent(ctx, a)
	require.ErrorIs(t, err, ErrOCRConflict)
	for i := 0; i < 2; i++ {
		state, err := s.Reserve(ctx, job.ID)
		require.NoError(t, err)
		assert.Equal(t, "reserved", state.BillingState)
	}
	ocrBalances(t, s, a, 900, 400, 100)
	claim, err := s.ClaimSubmission(ctx, job.ID, ocrTestSelection(a.ChannelID))
	require.NoError(t, err)
	_, err = s.ClaimSubmission(ctx, job.ID, ocrTestSelection(a.ChannelID))
	require.ErrorIs(t, err, ErrOCRConflict)
	require.NoError(t, s.RecordSubmission(ctx, claim, "provider-fixture", false))
	for i := 0; i < 2; i++ {
		_, err = s.Finish(ctx, job.ID, "success")
		require.NoError(t, err)
	}
	_, err = s.Finish(ctx, job.ID, "failure")
	require.ErrorIs(t, err, ErrOCRConflict)
	ocrBalances(t, s, a, 900, 400, 100)
	var u User
	require.NoError(t, s.db.First(&u, a.UserID).Error)
	assert.Equal(t, 100, u.UsedQuota)
	assert.Equal(t, 1, u.RequestCount)
	var n int64
	require.NoError(t, s.db.Model(&OCRAuditEvent{}).Where("job_id = ?", job.ID).Count(&n).Error)
	assert.EqualValues(t, 1, n)
}

func TestOCRRefundAndResetGeneration(t *testing.T) {
	for _, reset := range []bool{false, true} {
		t.Run(map[bool]string{false: "same period", true: "new period"}[reset], func(t *testing.T) {
			s, a := ocrFixture(t)
			a.Preference = "subscription_only"
			ctx := context.Background()
			plan := SubscriptionPlan{Title: "fixture", QuotaResetPeriod: SubscriptionResetNever}
			require.NoError(t, s.db.Create(&plan).Error)
			sub := UserSubscription{UserId: a.UserID, PlanId: plan.Id, AmountTotal: 1000, Status: "active", EndTime: time.Now().Add(time.Hour).Unix()}
			require.NoError(t, s.db.Create(&sub).Error)
			j, err := s.CreateIntent(ctx, a)
			require.NoError(t, err)
			_, err = s.Reserve(ctx, j.ID)
			require.NoError(t, err)
			if reset {
				require.NoError(t, s.db.Transaction(func(tx *gorm.DB) error {
					require.NoError(t, tx.First(&sub, sub.Id).Error)
					return resetUserSubscriptionTx(tx, &sub, &plan, time.Now().Unix(), false)
				}))
				require.NoError(t, s.db.Model(&sub).Update("amount_used", 37).Error)
			}
			for i := 0; i < 2; i++ {
				result, err := s.Finish(ctx, j.ID, "cancel")
				require.NoError(t, err)
				assert.Equal(t, "refunded", result.BillingState)
			}
			require.NoError(t, s.db.First(&sub, sub.Id).Error)
			want := int64(0)
			if reset {
				want = 37
			}
			assert.Equal(t, want, sub.AmountUsed)
			ocrBalances(t, s, a, 1000, 500, 0)
		})
	}
}

func TestOCRFundingPreferencesAndTokenAccounting(t *testing.T) {
	cases := []struct {
		name, pref          string
		wallet, token       int
		unlimited, overflow bool
		subTotal            int64
		wantFunding         string
		wantErr             bool
	}{
		{"wallet", "wallet_only", 1000, 500, false, true, 1000, "wallet", false},
		{"wallet fallback", "wallet_first", 20, 500, false, true, 1000, "subscription", false},
		{"subscription", "subscription_first", 1000, 500, false, true, 1000, "subscription", false},
		{"overflow", "subscription_first", 1000, 500, false, true, 20, "wallet", false},
		{"no overflow", "subscription_first", 1000, 500, false, false, 20, "", true},
		{"subscription only", "subscription_only", 1000, 500, false, true, 20, "", true},
		{"token denies all", "wallet_first", 1000, 20, false, true, 1000, "", true},
		{"unlimited", "wallet_only", 1000, 20, true, true, 1000, "wallet", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, a := ocrFixture(t)
			a.Preference = tc.pref
			ctx := context.Background()
			require.NoError(t, s.db.Model(&User{}).Where("id = ?", a.UserID).Update("quota", tc.wallet).Error)
			require.NoError(t, s.db.Model(&Token{}).Where("id = ?", a.TokenID).Updates(map[string]any{"remain_quota": tc.token, "unlimited_quota": tc.unlimited}).Error)
			plan := SubscriptionPlan{Title: "fixture", QuotaResetPeriod: SubscriptionResetNever}
			require.NoError(t, s.db.Create(&plan).Error)
			sub := UserSubscription{UserId: a.UserID, PlanId: plan.Id, AmountTotal: tc.subTotal, AllowWalletOverflow: tc.overflow, Status: "active", EndTime: time.Now().Add(time.Hour).Unix()}
			require.NoError(t, s.db.Create(&sub).Error)
			j, err := s.CreateIntent(ctx, a)
			require.NoError(t, err)
			result, err := s.Reserve(ctx, j.ID)
			if tc.wantErr {
				require.ErrorIs(t, err, ErrOCRInsufficient)
				ocrBalances(t, s, a, tc.wallet, tc.token, 0)
				require.NoError(t, s.db.First(&sub, sub.Id).Error)
				assert.Zero(t, sub.AmountUsed)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.wantFunding, result.Funding)
			wallet := tc.wallet
			if tc.wantFunding == "wallet" {
				wallet -= 100
			}
			ocrBalances(t, s, a, wallet, tc.token-100, 100)
		})
	}
}
func TestOCRFreeAndInvalidPrice(t *testing.T) {
	for _, price := range []float64{0, 0.0000001, -1, math.NaN(), math.Inf(1), 1e30} {
		t.Run(fmt.Sprint(price), func(t *testing.T) {
			s, a := ocrFixture(t)
			a.Price = price
			a.Preference = "subscription_only"
			j, err := s.CreateIntent(context.Background(), a)
			if price != 0 {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			result, err := s.Reserve(context.Background(), j.ID)
			require.NoError(t, err)
			assert.Zero(t, result.Quota)
			ocrBalances(t, s, a, 1000, 500, 0)
		})
	}
}
func TestOCRSQLRollbackAndCommitRecovery(t *testing.T) {
	s, a := ocrFixture(t)
	ctx := context.Background()
	j, err := s.CreateIntent(ctx, a)
	require.NoError(t, err)
	injected := errors.New("fixture receipt persistence failure")
	require.NoError(t, s.db.Callback().Update().Before("gorm:update").Register("ocr_receipt_failure", func(tx *gorm.DB) {
		if op, ok := tx.Statement.Dest.(*OCROperation); ok && op.Kind == "token" {
			tx.AddError(injected)
		}
	}))
	_, err = s.Reserve(ctx, j.ID)
	require.ErrorIs(t, err, injected)
	ocrBalances(t, s, a, 1000, 500, 0)
	require.NoError(t, s.db.Callback().Update().Remove("ocr_receipt_failure"))
	_, err = s.Reserve(ctx, j.ID)
	require.NoError(t, err)
	restarted := NewOCRBillingStore(s.db, nil)
	_, err = restarted.Reserve(ctx, j.ID)
	require.NoError(t, err)
	ocrBalances(t, s, a, 900, 400, 100)
}
func TestOCRDeletedTokenRefundIsAccountingOnly(t *testing.T) {
	for _, hard := range []bool{false, true} {
		t.Run(fmt.Sprint(hard), func(t *testing.T) {
			s, a := ocrFixture(t)
			ctx := context.Background()
			j, err := s.CreateIntent(ctx, a)
			require.NoError(t, err)
			_, err = s.Reserve(ctx, j.ID)
			require.NoError(t, err)
			if hard {
				require.NoError(t, s.db.Unscoped().Delete(&Token{}, a.TokenID).Error)
			} else {
				require.NoError(t, s.db.Delete(&Token{}, a.TokenID).Error)
			}
			result, err := s.Finish(ctx, j.ID, "cancel")
			if hard {
				require.Error(t, err)
				assert.NotEqual(t, "refunded", result.BillingState)
				return
			}
			require.NoError(t, err)
			var tok Token
			require.NoError(t, s.db.Unscoped().First(&tok, a.TokenID).Error)
			assert.True(t, tok.DeletedAt.Valid)
			assert.Equal(t, 500, tok.RemainQuota)
		})
	}
}
func TestOCRUnknownSubmissionNeverResubmitsOrRefunds(t *testing.T) {
	s, a := ocrFixture(t)
	ctx := context.Background()
	j, err := s.CreateIntent(ctx, a)
	require.NoError(t, err)
	_, err = s.Reserve(ctx, j.ID)
	require.NoError(t, err)
	claim, err := s.ClaimSubmission(ctx, j.ID, ocrTestSelection(a.ChannelID))
	require.NoError(t, err)
	require.NoError(t, s.RecordSubmission(ctx, claim, "", true))
	_, err = s.Finish(ctx, j.ID, "cancel")
	require.ErrorIs(t, err, ErrOCRConflict)
	_, err = s.Finish(ctx, j.ID, "failure")
	require.ErrorIs(t, err, ErrOCRConflict)
	_, err = s.ClaimSubmission(ctx, j.ID, ocrTestSelection(a.ChannelID))
	require.ErrorIs(t, err, ErrOCRConflict)
	ocrBalances(t, s, a, 900, 400, 100)
}

func TestOCRLazyResetAndMixedOverflow(t *testing.T) {
	s, a := ocrFixture(t)
	a.Preference = "subscription_first"
	ctx := context.Background()
	plan := SubscriptionPlan{Title: "daily", QuotaResetPeriod: SubscriptionResetDaily}
	require.NoError(t, s.db.Create(&plan).Error)
	now := time.Now()
	sub := UserSubscription{UserId: a.UserID, PlanId: plan.Id, AmountTotal: 1000, AmountUsed: 1000, Status: "active", StartTime: now.Add(-48 * time.Hour).Unix(), LastResetTime: now.Add(-48 * time.Hour).Unix(), NextResetTime: now.Add(-24 * time.Hour).Unix(), EndTime: now.Add(time.Hour).Unix()}
	require.NoError(t, s.db.Create(&sub).Error)
	j, err := s.CreateIntent(ctx, a)
	require.NoError(t, err)
	_, err = s.Reserve(ctx, j.ID)
	require.NoError(t, err)
	require.NoError(t, s.db.First(&sub, sub.Id).Error)
	assert.EqualValues(t, 100, sub.AmountUsed)
	assert.EqualValues(t, 1, sub.ResetGeneration)
	require.NoError(t, s.db.Model(&sub).Updates(map[string]any{"amount_used": 1000, "allow_wallet_overflow": true}).Error)
	second := sub
	second.Id = 0
	second.AllowWalletOverflow = false
	require.NoError(t, s.db.Create(&second).Error)
	a.IdempotencyKey = uuid.NewString()
	j, err = s.CreateIntent(ctx, a)
	require.NoError(t, err)
	_, err = s.Reserve(ctx, j.ID)
	require.ErrorIs(t, err, ErrOCRInsufficient)
	ocrBalances(t, s, a, 1000, 400, 100)
}
func TestOCRAuditFailureNeverReplaysMoneyAndConflictingFinish(t *testing.T) {
	s, a := ocrFixture(t)
	ctx := context.Background()
	j, err := s.CreateIntent(ctx, a)
	require.NoError(t, err)
	_, err = s.Reserve(ctx, j.ID)
	require.NoError(t, err)
	claim, err := s.ClaimSubmission(ctx, j.ID, ocrTestSelection(a.ChannelID))
	require.NoError(t, err)
	require.NoError(t, s.RecordSubmission(ctx, claim, "provider", false))
	injected := errors.New("fixture audit insert failure")
	require.NoError(t, s.db.Callback().Create().Before("gorm:create").Register("ocr_audit_fail", func(tx *gorm.DB) {
		if _, ok := tx.Statement.Dest.(*OCRAuditEvent); ok {
			tx.AddError(injected)
		}
	}))
	_, err = s.Finish(ctx, j.ID, "success")
	require.ErrorIs(t, err, injected)
	var user User
	require.NoError(t, s.db.First(&user, a.UserID).Error)
	assert.Zero(t, user.RequestCount)
	ocrBalances(t, s, a, 900, 400, 100)
	require.NoError(t, s.db.Callback().Create().Remove("ocr_audit_fail"))
	_, err = s.Finish(ctx, j.ID, "success")
	require.NoError(t, err)
	require.ErrorIs(t, s.ProjectAudit(ctx, 10, func(context.Context, OCRAuditEvent) error { return injected }), injected)
	var events []OCRAuditEvent
	require.NoError(t, s.db.Find(&events).Error)
	require.Len(t, events, 1)
	assert.Zero(t, events[0].ProjectedAt)
	require.NoError(t, s.ProjectAudit(ctx, 10, func(_ context.Context, event OCRAuditEvent) error {
		assert.Equal(t, events[0].ID, event.ID)
		return nil
	}))
	ocrBalances(t, s, a, 900, 400, 100)
	_, err = s.Finish(ctx, j.ID, "failure")
	require.ErrorIs(t, err, ErrOCRConflict)
	require.NoError(t, s.db.First(&user, a.UserID).Error)
	assert.Equal(t, 1, user.RequestCount)
}
func TestOCRRevokeBeforeSubmissionBlocksClaim(t *testing.T) {
	s, a := ocrFixture(t)
	ctx := context.Background()
	j, err := s.CreateIntent(ctx, a)
	require.NoError(t, err)
	_, err = s.Reserve(ctx, j.ID)
	require.NoError(t, err)
	require.NoError(t, s.db.Model(&Token{}).Where("id = ?", a.TokenID).Update("status", common.TokenStatusDisabled).Error)
	_, err = s.ClaimSubmission(ctx, j.ID, ocrTestSelection(a.ChannelID))
	require.ErrorIs(t, err, ErrOCRConflict)
	_, err = s.Finish(ctx, j.ID, "cancel")
	require.NoError(t, err)
	ocrBalances(t, s, a, 1000, 500, 0)
	var tok Token
	require.NoError(t, s.db.First(&tok, a.TokenID).Error)
	assert.Equal(t, common.TokenStatusDisabled, tok.Status)
}

func TestOCRConfirmedCreateRejectionCanRefundWithoutProviderID(t *testing.T) {
	s, a := ocrFixture(t)
	ctx := context.Background()
	j, err := s.CreateIntent(ctx, a)
	require.NoError(t, err)
	_, err = s.Reserve(ctx, j.ID)
	require.NoError(t, err)
	claim, err := s.ClaimSubmission(ctx, j.ID, ocrTestSelection(a.ChannelID))
	require.NoError(t, err)
	require.NoError(t, s.RecordSubmissionFailure(ctx, claim))
	_, err = s.Finish(ctx, j.ID, "success")
	require.ErrorIs(t, err, ErrOCRConflict)
	out, err := s.Finish(ctx, j.ID, "failure")
	require.NoError(t, err)
	assert.Equal(t, "refunded", out.BillingState)
	ocrBalances(t, s, a, 1000, 500, 0)
}

func TestOCRClaimFreezesSafeAccountSelection(t *testing.T) {
	s, a := ocrFixture(t)
	ctx := context.Background()
	j, err := s.CreateIntent(ctx, a)
	require.NoError(t, err)
	_, err = s.Reserve(ctx, j.ID)
	require.NoError(t, err)
	selection := ocrTestSelection(a.ChannelID)
	wrong := selection
	wrong.Ref.ChannelID++
	_, err = s.ClaimSubmission(ctx, j.ID, wrong)
	require.ErrorIs(t, err, ErrOCRConflict)
	_, err = s.ClaimSubmission(ctx, j.ID, selection)
	require.NoError(t, err)
	journal, err := s.Inspect(ctx, j.ID)
	require.NoError(t, err)
	var binding OCRAccountBinding
	require.NoError(t, common.UnmarshalJsonStr(journal.Job.AccountBinding, &binding))
	assert.Equal(t, selection.Ref, binding.Ref)
	assert.Equal(t, selection.Sequence, binding.Sequence)
}

func ocrTestSelection(channelID int) UpstreamAccountSelection {
	return UpstreamAccountSelection{Ref: UpstreamAccountRef{PoolID: fmt.Sprintf("channel:%d", channelID), MemberID: "94689dcc-c280-463e-891e-5b3eaa30c103", ChannelID: channelID, KeyIndex: 0}, KeyIndex: 0, SelectedAt: time.Now(), Sequence: 1}
}

func TestOCRStableOwnerMayDifferFromHealthGeneration(t *testing.T) {
	s, a := ocrFixture(t)
	ctx := context.Background()
	j, err := s.CreateIntent(ctx, a)
	require.NoError(t, err)
	_, err = s.Reserve(ctx, j.ID)
	require.NoError(t, err)
	selection := ocrTestSelection(a.ChannelID)
	owner := selection.Ref
	owner.MemberID = uuid.NewString()
	wrong := owner
	wrong.PoolID = "other-pool"
	_, err = s.ClaimSubmission(ctx, j.ID, selection, wrong)
	require.ErrorIs(t, err, ErrOCRConflict)
	claim, err := s.ClaimSubmission(ctx, j.ID, selection, owner)
	require.NoError(t, err)
	assert.Equal(t, owner.MemberID, claim.MemberID)
	journal, err := s.Inspect(ctx, j.ID)
	require.NoError(t, err)
	var binding OCRAccountBinding
	require.NoError(t, common.UnmarshalJsonStr(journal.Job.AccountBinding, &binding))
	assert.Equal(t, selection.Ref, binding.Ref)
	require.NotNil(t, binding.Owner)
	assert.Equal(t, owner, *binding.Owner)
}

// Existing installations can contain int64 quota balances from before the
// int32 billing guard. A free request has no monetary delta to clamp or rewrite.
func TestOCRFreeLegacyBalancesRemainUnchanged(t *testing.T) {
	for _, cached := range []bool{false, true} {
		t.Run(fmt.Sprintf("redis=%t", cached), func(t *testing.T) {
			var s *OCRBillingStore
			var a OCRAdmission
			if cached {
				s, a, _ = ocrRedisFixture(t)
			} else {
				s, a = ocrFixture(t)
			}
			legacy := int(common.MaxQuota) + 1000
			require.NoError(t, s.db.Model(&User{}).Where("id = ?", a.UserID).Updates(map[string]any{"quota": legacy, "used_quota": legacy}).Error)
			require.NoError(t, s.db.Model(&Token{}).Where("id = ?", a.TokenID).Updates(map[string]any{"remain_quota": legacy, "used_quota": legacy}).Error)
			require.NoError(t, s.db.Model(&Channel{}).Where("id = ?", a.ChannelID).Update("used_quota", legacy).Error)
			a.Price = 0
			ctx := context.Background()
			job, err := s.CreateIntent(ctx, a)
			require.NoError(t, err)
			reserved, err := s.Reserve(ctx, job.ID)
			require.NoError(t, err)
			assert.Equal(t, "reserved", reserved.BillingState)
			claim, err := s.ClaimSubmission(ctx, job.ID, ocrTestSelection(a.ChannelID))
			require.NoError(t, err)
			require.NoError(t, s.RecordSubmission(ctx, claim, "free-provider-job", false))
			done, err := s.Finish(ctx, job.ID, "success")
			require.NoError(t, err)
			assert.Equal(t, "settled", done.BillingState)
			_, err = s.Finish(ctx, job.ID, "success")
			require.NoError(t, err)
			ocrBalances(t, s, a, legacy, legacy, legacy)
			var u User
			require.NoError(t, s.db.First(&u, a.UserID).Error)
			assert.Equal(t, 1, u.RequestCount)
			// Nonzero charges must retain the bounded-arithmetic gate.
			a.IdempotencyKey = uuid.NewString()
			a.Price = 0.0002
			paid, err := s.CreateIntent(ctx, a)
			require.NoError(t, err)
			_, err = s.Reserve(ctx, paid.ID)
			require.ErrorIs(t, err, ErrOCRReconciliation)
			ocrBalances(t, s, a, legacy, legacy, legacy)
		})
	}
}
