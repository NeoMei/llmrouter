package model

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"github.com/QuantumNous/new-api/common"
	"github.com/alicebob/miniredis/v2"
	"github.com/go-redis/redis/v8"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"
)

func ocrRedisFixture(t *testing.T) (*OCRBillingStore, OCRAdmission, *miniredis.Miniredis) {
	t.Helper()
	s, a := ocrFixture(t)
	mr := miniredis.RunT(t)
	shared := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { require.NoError(t, shared.Close()) })
	rs := NewOCRBillingStore(s.db, shared)
	t.Cleanup(func() { require.NoError(t, rs.Close()) })
	return rs, a, mr
}
func TestOCRRedisSynchronousAndReceiptLoss(t *testing.T) {
	for _, batch := range []bool{false, true} {
		t.Run(map[bool]string{false: "direct", true: "batch"}[batch], func(t *testing.T) {
			s, a, mr := ocrRedisFixture(t)
			old := common.BatchUpdateEnabled
			common.BatchUpdateEnabled = batch
			t.Cleanup(func() { common.BatchUpdateEnabled = old })
			ctx := context.Background()
			j, err := s.CreateIntent(ctx, a)
			require.NoError(t, err)
			result, err := s.Reserve(ctx, j.ID)
			require.NoError(t, err)
			assert.Equal(t, "reserved", result.BillingState)
			ocrBalances(t, s, a, 900, 400, 100)
			assert.Equal(t, "900", mr.HGet(getUserCacheKey(a.UserID), "Quota"))
			_, err = s.Reserve(ctx, j.ID)
			require.NoError(t, err)
			ocrBalances(t, s, a, 900, 400, 100)
			mr.Del(getUserCacheKey(a.UserID))
			_, err = s.ClaimSubmission(ctx, j.ID, ocrTestSelection(a.ChannelID))
			require.ErrorIs(t, err, ErrOCRReconciliation)
			_, err = s.Reserve(ctx, j.ID)
			require.ErrorIs(t, err, ErrOCRReconciliation)
			ocrBalances(t, s, a, 900, 400, 100)
		})
	}
}
func TestOCRRedisKnownPartialRefund(t *testing.T) {
	s, a, mr := ocrRedisFixture(t)
	ctx := context.Background()
	require.NoError(t, s.db.Model(&Token{}).Where("id = ?", a.TokenID).Update("remain_quota", 40).Error)
	j, err := s.CreateIntent(ctx, a)
	require.NoError(t, err)
	_, err = s.Reserve(ctx, j.ID)
	require.ErrorIs(t, err, ErrOCRInsufficient)
	ocrBalances(t, s, a, 900, 40, 0)
	out, err := s.Finish(ctx, j.ID, "cancel")
	require.NoError(t, err)
	assert.Equal(t, "refunded", out.BillingState)
	ocrBalances(t, s, a, 1000, 40, 0)
	assert.Equal(t, "1000", mr.HGet(getUserCacheKey(a.UserID), "Quota"))
}
func TestOCRRedisMalformedTokenCannotPartiallyDebit(t *testing.T) {
	s, a, mr := ocrRedisFixture(t)
	ctx := context.Background()
	var tok Token
	require.NoError(t, s.db.First(&tok, a.TokenID).Error)
	mr.HSet(getTokenCacheKey(tok.Key), "Id", "1", "UserId", "1", "RemainQuota", "500", "UsedQuota", "not-integer")
	j, err := s.CreateIntent(ctx, a)
	require.NoError(t, err)
	_, err = s.Reserve(ctx, j.ID)
	require.Error(t, err)
	assert.Equal(t, "500", mr.HGet(getTokenCacheKey(tok.Key), "RemainQuota"))
	ocrBalances(t, s, a, 900, 500, 0)
}

// The connection delivers the command to a real Redis protocol endpoint and
// discards its successful response. Counting wire writes catches hidden retries.
type ocrDropReplyConn struct {
	net.Conn
	money   *atomic.Int32
	drop    *atomic.Bool
	pending bool
}

func (c *ocrDropReplyConn) Write(p []byte) (int, error) {
	if bytes.Contains(p, []byte("local typ = redis.call")) {
		c.money.Add(1)
		c.pending = c.drop.CompareAndSwap(true, false)
	}
	return c.Conn.Write(p)
}
func (c *ocrDropReplyConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	if c.pending && n > 0 {
		c.pending = false
		_ = c.Conn.Close()
		return 0, io.ErrUnexpectedEOF
	}
	return n, err
}
func TestOCRRedisLostReplyNoTransportRetryAndReadOnlyRecovery(t *testing.T) {
	s, a, mr := ocrRedisFixture(t)
	var commands atomic.Int32
	var drop atomic.Bool
	drop.Store(true)
	shared := redis.NewClient(&redis.Options{Addr: mr.Addr(), MaxRetries: 4, Dialer: func(ctx context.Context, network, addr string) (net.Conn, error) {
		c, err := (&net.Dialer{}).DialContext(ctx, network, addr)
		if err != nil {
			return nil, err
		}
		return &ocrDropReplyConn{Conn: c, money: &commands, drop: &drop}, nil
	}})
	t.Cleanup(func() { require.NoError(t, shared.Close()) })
	s = NewOCRBillingStore(s.db, shared)
	t.Cleanup(func() { require.NoError(t, s.Close()) })
	ctx := context.Background()
	j, err := s.CreateIntent(ctx, a)
	require.NoError(t, err)
	_, err = s.Reserve(ctx, j.ID)
	require.ErrorIs(t, err, ErrOCRReconciliation)
	assert.EqualValues(t, 1, commands.Load())
	assert.Equal(t, "900", mr.HGet(getUserCacheKey(a.UserID), "Quota"))
	ocrBalances(t, s, a, 1000, 500, 0)
	restarted := NewOCRBillingStore(s.db, shared)
	defer restarted.Close()
	out, err := restarted.Reserve(ctx, j.ID)
	require.NoError(t, err)
	assert.Equal(t, "reserved", out.BillingState)
	assert.EqualValues(t, 2, commands.Load(), "wallet is never invoked again; second command is token")
	ocrBalances(t, s, a, 900, 400, 100)
}
func TestOCRRedisAttemptWithoutEvidenceCannotRetryOrFinishRefund(t *testing.T) {
	s, a, _ := ocrRedisFixture(t)
	ctx := context.Background()
	j, err := s.CreateIntent(ctx, a)
	require.NoError(t, err)
	require.NoError(t, s.db.Model(&OCROperation{}).Where("job_id = ? AND kind = ?", j.ID, "wallet").Updates(map[string]any{"state": "cache_attempting", "cache_attempt": "crashed-before-network"}).Error)
	_, err = s.Reserve(ctx, j.ID)
	require.ErrorIs(t, err, ErrOCRReconciliation)
	_, err = s.ClaimSubmission(ctx, j.ID, ocrTestSelection(a.ChannelID))
	require.Error(t, err)
	out, err := s.Finish(ctx, j.ID, "cancel")
	require.ErrorIs(t, err, ErrOCRReconciliation)
	assert.NotEqual(t, "refunded", out.BillingState)
	ocrBalances(t, s, a, 1000, 500, 0)
}
func TestOCRRedisSQLFailureRecoversWithoutAnotherCacheDelta(t *testing.T) {
	s, a, mr := ocrRedisFixture(t)
	ctx := context.Background()
	j, err := s.CreateIntent(ctx, a)
	require.NoError(t, err)
	injected := errors.New("receipt commit fixture failure")
	require.NoError(t, s.db.Callback().Update().Before("gorm:update").Register("ocr_sql_fail", func(tx *gorm.DB) {
		if op, ok := tx.Statement.Dest.(*OCROperation); ok && op.State == "applied" {
			tx.AddError(injected)
		}
	}))
	_, err = s.Reserve(ctx, j.ID)
	require.ErrorIs(t, err, injected)
	ocrBalances(t, s, a, 1000, 500, 0)
	assert.Equal(t, "900", mr.HGet(getUserCacheKey(a.UserID), "Quota"))
	require.NoError(t, s.db.Callback().Update().Remove("ocr_sql_fail"))
	_, err = s.Reserve(ctx, j.ID)
	require.NoError(t, err)
	ocrBalances(t, s, a, 900, 400, 100)
	assert.Equal(t, "900", mr.HGet(getUserCacheKey(a.UserID), "Quota"))
}
func TestOCRRedisReceiptOrIncarnationLossHoldsSQLGap(t *testing.T) {
	for _, loss := range []string{"receipt", "incarnation"} {
		t.Run(loss, func(t *testing.T) {
			s, a, mr := ocrRedisFixture(t)
			ctx := context.Background()
			j, err := s.CreateIntent(ctx, a)
			require.NoError(t, err)
			require.NoError(t, s.db.Callback().Update().Before("gorm:update").Register("ocr_sql_fail", func(tx *gorm.DB) {
				if op, ok := tx.Statement.Dest.(*OCROperation); ok && op.State == "applied" {
					tx.AddError(errors.New("fixture"))
				}
			}))
			_, err = s.Reserve(ctx, j.ID)
			require.Error(t, err)
			require.NoError(t, s.db.Callback().Update().Remove("ocr_sql_fail"))
			op, err := ocrOperation(s.db, j.ID, "wallet", "reserve")
			require.NoError(t, err)
			if loss == "receipt" {
				mr.Del(ocrReceiptKey(op))
			} else {
				mr.HSet(getUserCacheKey(a.UserID), "OCRIncarnation", "recreated")
			}
			_, err = s.Reserve(ctx, j.ID)
			require.ErrorIs(t, err, ErrOCRReconciliation)
			ocrBalances(t, s, a, 1000, 500, 0)
			assert.Equal(t, "900", mr.HGet(getUserCacheKey(a.UserID), "Quota"))
		})
	}
}

func TestOCRLateAppliedReserveCannotBecomeFalseRefund(t *testing.T) {
	s, a, _ := ocrRedisFixture(t)
	ctx := context.Background()
	j, err := s.CreateIntent(ctx, a)
	require.NoError(t, err)
	// Cancellation prepared while an attempt was unresolved. The original attempt
	// then commits its discoverable SQL receipt before the cancellation finalizer.
	require.NoError(t, s.applyOCRCacheOperation(ctx, j.ID, "wallet", "reserve"))
	require.NoError(t, s.db.Model(&OCRJob{}).Where("id = ?", j.ID).Updates(map[string]any{"decision": "cancel", "billing_state": "refunding"}).Error)
	out, err := s.refundRedis(ctx, j.ID)
	require.ErrorIs(t, err, ErrOCRReconciliation)
	assert.NotEqual(t, "refunded", out.BillingState)
	ocrBalances(t, s, a, 900, 500, 0)
	out, err = s.Finish(ctx, j.ID, "cancel")
	require.NoError(t, err)
	assert.Equal(t, "refunded", out.BillingState)
	ocrBalances(t, s, a, 1000, 500, 0)
}

func TestOCRRedisRefundReplyLossKeepsHoldUntilReceiptRecovery(t *testing.T) {
	s, a, mr := ocrRedisFixture(t)
	var commands atomic.Int32
	var drop atomic.Bool
	shared := redis.NewClient(&redis.Options{Addr: mr.Addr(), MaxRetries: 4, Dialer: func(ctx context.Context, network, addr string) (net.Conn, error) {
		c, err := (&net.Dialer{}).DialContext(ctx, network, addr)
		if err != nil {
			return nil, err
		}
		return &ocrDropReplyConn{Conn: c, money: &commands, drop: &drop}, nil
	}})
	t.Cleanup(func() { require.NoError(t, shared.Close()) })
	s = NewOCRBillingStore(s.db, shared)
	t.Cleanup(func() { require.NoError(t, s.Close()) })
	ctx := context.Background()
	j, err := s.CreateIntent(ctx, a)
	require.NoError(t, err)
	_, err = s.Reserve(ctx, j.ID)
	require.NoError(t, err)
	drop.Store(true)
	out, err := s.Finish(ctx, j.ID, "cancel")
	require.ErrorIs(t, err, ErrOCRReconciliation)
	assert.NotEqual(t, "refunded", out.BillingState)
	assert.EqualValues(t, 3, commands.Load())
	out, err = s.Finish(ctx, j.ID, "cancel")
	require.NoError(t, err)
	assert.Equal(t, "refunded", out.BillingState)
	assert.EqualValues(t, 4, commands.Load())
	ocrBalances(t, s, a, 1000, 500, 0)
}

func TestOCRBillingModeChangeCannotSwitchMoneyAuthority(t *testing.T) {
	for _, redisFirst := range []bool{false, true} {
		t.Run(fmt.Sprint(redisFirst), func(t *testing.T) {
			redisStore, a, _ := ocrRedisFixture(t)
			dbStore := NewOCRBillingStore(redisStore.db, nil)
			initial, changed := dbStore, redisStore
			if redisFirst {
				initial, changed = redisStore, dbStore
			}
			ctx := context.Background()
			j, err := initial.CreateIntent(ctx, a)
			require.NoError(t, err)
			_, err = changed.Reserve(ctx, j.ID)
			require.ErrorIs(t, err, ErrOCRReconciliation)
			ocrBalances(t, initial, a, 1000, 500, 0)
			// A configuration change also cannot refund a reservation via another authority.
			_, err = initial.Reserve(ctx, j.ID)
			require.NoError(t, err)
			_, err = changed.Finish(ctx, j.ID, "cancel")
			require.ErrorIs(t, err, ErrOCRReconciliation)
			ocrBalances(t, initial, a, 900, 400, 100)
		})
	}
}

func TestOCRRedisRefundCacheLossBeforeTerminalStaysUnresolved(t *testing.T) {
	s, a, mr := ocrRedisFixture(t)
	ctx := context.Background()
	j, err := s.CreateIntent(ctx, a)
	require.NoError(t, err)
	_, err = s.Reserve(ctx, j.ID)
	require.NoError(t, err)
	require.NoError(t, s.db.Callback().Update().After("gorm:update").Register("ocr_refund_eviction", func(tx *gorm.DB) {
		if op, ok := tx.Statement.Dest.(*OCROperation); ok && op.Phase == "refund" && op.Kind == "wallet" {
			mr.Del(getUserCacheKey(a.UserID))
		}
	}))
	out, err := s.Finish(ctx, j.ID, "cancel")
	require.ErrorIs(t, err, ErrOCRReconciliation)
	assert.NotEqual(t, "refunded", out.BillingState)
}

func TestOCRRedisSubscriptionFallbackAndUnlimited(t *testing.T) {
	for _, pref := range []string{"subscription_only", "subscription_first", "wallet_first"} {
		t.Run(pref, func(t *testing.T) {
			s, a, _ := ocrRedisFixture(t)
			a.Preference = pref
			ctx := context.Background()
			require.NoError(t, s.db.Model(&User{}).Where("id = ?", a.UserID).Update("quota", 20).Error)
			require.NoError(t, s.db.Model(&Token{}).Where("id = ?", a.TokenID).Updates(map[string]any{"remain_quota": 20, "unlimited_quota": true}).Error)
			plan := SubscriptionPlan{Title: "fixture", QuotaResetPeriod: SubscriptionResetNever}
			require.NoError(t, s.db.Create(&plan).Error)
			sub := UserSubscription{UserId: a.UserID, PlanId: plan.Id, AmountTotal: 1000, Status: "active", EndTime: time.Now().Add(time.Hour).Unix()}
			require.NoError(t, s.db.Create(&sub).Error)
			j, err := s.CreateIntent(ctx, a)
			require.NoError(t, err)
			out, err := s.Reserve(ctx, j.ID)
			require.NoError(t, err)
			assert.Equal(t, "subscription", out.Funding)
			ocrBalances(t, s, a, 20, -80, 100)
			require.NoError(t, s.db.First(&sub, sub.Id).Error)
			assert.EqualValues(t, 100, sub.AmountUsed)
			_, err = s.Finish(ctx, j.ID, "cancel")
			require.NoError(t, err)
			ocrBalances(t, s, a, 20, 20, 0)
			require.NoError(t, s.db.First(&sub, sub.Id).Error)
			assert.Zero(t, sub.AmountUsed)
		})
	}
}

func TestOCROwnDeltasAreNotReplayedByLegacyBatchFlush(t *testing.T) {
	s, a, _ := ocrRedisFixture(t)
	ctx := context.Background()
	oldDB, oldRedis, oldEnabled := DB, common.RDB, common.RedisEnabled
	resetBatchUpdateTestState(t)
	DB = s.db
	common.RDB = s.cache
	common.RedisEnabled = true
	common.BatchUpdateEnabled = true
	t.Cleanup(func() { DB = oldDB; common.RDB = oldRedis; common.RedisEnabled = oldEnabled })
	var token Token
	require.NoError(t, s.db.First(&token, a.TokenID).Error)
	ok, err := TryReserveUserQuota(a.UserID, 30)
	require.NoError(t, err)
	require.True(t, ok)
	ok, err = TryReserveTokenQuota(a.TokenID, token.Key, 40, false)
	require.NoError(t, err)
	require.True(t, ok)
	j, err := s.CreateIntent(ctx, a)
	require.NoError(t, err)
	_, err = s.Reserve(ctx, j.ID)
	require.NoError(t, err)
	ocrBalances(t, s, a, 900, 400, 100)
	batchUpdate()
	ocrBalances(t, s, a, 870, 360, 140)
	batchUpdate()
	ocrBalances(t, s, a, 870, 360, 140)
}
