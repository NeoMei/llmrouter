package model

import (
	"context"
	"database/sql"
	"io"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type ocrRaceContextKey struct{}

func ocrConcurrentFixture(t *testing.T, dialect string) (*OCRBillingStore, OCRAdmission, UserSubscription, SubscriptionPlan) {
	t.Helper()
	env := "LLMROUTER_ACCOUNT_POOL_MYSQL_DSN"
	if dialect == "postgres96" {
		env = "LLMROUTER_ACCOUNT_POOL_POSTGRES_DSN"
	}
	dsn := os.Getenv(env)
	if dsn == "" {
		t.Skip(env + " not configured")
	}
	var driver gorm.Dialector = mysql.Open(dsn)
	if dialect == "postgres96" {
		driver = postgres.New(postgres.Config{DSN: dsn, PreferSimpleProtocol: true})
	}
	db, err := gorm.Open(driver, &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	conn, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, conn.Close()) })
	user := User{Username: "ocr-race-" + uuid.NewString()[:10], AffCode: uuid.NewString()[:16], Quota: 1000, Status: common.UserStatusEnabled, AuthVersion: 1}
	require.NoError(t, db.Create(&user).Error)
	token := Token{UserId: user.Id, Key: uuid.NewString(), RemainQuota: 1000, Status: common.TokenStatusEnabled, ExpiredTime: -1}
	require.NoError(t, db.Create(&token).Error)
	channel := Channel{Name: "ocr-race-" + uuid.NewString()}
	require.NoError(t, db.Omit("ChannelInfo").Create(&channel).Error)
	plan := SubscriptionPlan{Title: "ocr-race", QuotaResetPeriod: SubscriptionResetNever}
	require.NoError(t, db.Create(&plan).Error)
	sub := UserSubscription{UserId: user.Id, PlanId: plan.Id, AmountTotal: 1000, AllowWalletOverflow: true, Status: "active", EndTime: time.Now().Add(time.Hour).Unix()}
	require.NoError(t, db.Create(&sub).Error)
	t.Cleanup(func() {
		var ids []string
		require.NoError(t, db.Model(&OCRJob{}).Where("user_id = ?", user.Id).Pluck("id", &ids).Error)
		if len(ids) > 0 {
			require.NoError(t, db.Where("job_id IN ?", ids).Delete(&OCROperation{}).Error)
			require.NoError(t, db.Where("job_id IN ?", ids).Delete(&OCRAuditEvent{}).Error)
			require.NoError(t, db.Where("id IN ?", ids).Delete(&OCRJob{}).Error)
		}
		require.NoError(t, db.Delete(&sub).Error)
		require.NoError(t, db.Delete(&plan).Error)
		require.NoError(t, db.Unscoped().Delete(&token).Error)
		require.NoError(t, db.Unscoped().Delete(&user).Error)
		require.NoError(t, db.Delete(&channel).Error)
	})
	return NewOCRBillingStore(db, nil), OCRAdmission{IdempotencyKey: uuid.NewString(), UserID: user.Id, TokenID: token.Id, ChannelID: channel.Id, Model: "fixture-ocr", Group: "default", Preference: "wallet_first", Price: 100, GroupRatio: 1, QuotaPerUnit: 1}, sub, plan
}

func TestOCRMixedPreferenceFundingLocks(t *testing.T) {
	for _, dialect := range []string{"mysql57", "postgres96"} {
		t.Run(dialect, func(t *testing.T) {
			s, a, sub, _ := ocrConcurrentFixture(t, dialect)
			require.NoError(t, s.db.Model(&User{}).Where("id = ?", a.UserID).Update("quota", 20).Error)
			require.NoError(t, s.db.Model(&sub).Update("amount_total", 200).Error)
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			wallet, err := s.CreateIntent(ctx, a)
			require.NoError(t, err)
			a.IdempotencyKey = uuid.NewString()
			a.Preference = "subscription_first"
			a.Price = 300
			subscription, err := s.CreateIntent(ctx, a)
			require.NoError(t, err)
			walletLocked, subscriptionApproachingUser := make(chan struct{}), make(chan struct{})
			var walletOnce, subscriptionOnce sync.Once
			require.NoError(t, s.db.Callback().Query().After("gorm:query").Register("ocr_wallet_lock_barrier", func(tx *gorm.DB) {
				if tx.Statement.Context.Value(ocrRaceContextKey{}) != "wallet" {
					return
				}
				if _, ok := tx.Statement.Dest.(*User); ok {
					walletOnce.Do(func() {
						close(walletLocked)
						select {
						case <-subscriptionApproachingUser:
						case <-ctx.Done():
							tx.AddError(ctx.Err())
						}
					})
				}
			}))
			require.NoError(t, s.db.Callback().Query().Before("gorm:query").Register("ocr_subscription_lock_barrier", func(tx *gorm.DB) {
				if tx.Statement.Context.Value(ocrRaceContextKey{}) != "subscription" {
					return
				}
				if _, ok := tx.Statement.Dest.(*User); ok {
					subscriptionOnce.Do(func() { close(subscriptionApproachingUser) })
				}
			}))
			walletResult := make(chan error, 1)
			go func() {
				_, err := s.Reserve(context.WithValue(ctx, ocrRaceContextKey{}, "wallet"), wallet.ID)
				walletResult <- err
			}()
			select {
			case <-walletLocked:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			_, subscriptionErr := s.Reserve(context.WithValue(ctx, ocrRaceContextKey{}, "subscription"), subscription.ID)
			walletErr := <-walletResult
			require.NoError(t, walletErr, "the 100-unit job must remain fundable")
			require.ErrorIs(t, subscriptionErr, ErrOCRInsufficient, "the 300-unit job is financially insufficient, never a deadlock victim")
			require.NoError(t, s.db.First(&sub, sub.Id).Error)
			assert.EqualValues(t, 100, sub.AmountUsed)
			ocrBalances(t, s, a, 20, 900, 100)
		})
	}
}

// Wrap only the GORM transaction's Commit boundary: the real SQL COMMIT succeeds
// then its acknowledgement is lost. All SQL statements and receipts remain real.
type ocrCommitAckPool struct {
	gorm.ConnPool
	begin gorm.TxBeginner
	lose  *atomic.Int32
}

func (p ocrCommitAckPool) BeginTx(ctx context.Context, opts *sql.TxOptions) (gorm.ConnPool, error) {
	tx, err := p.begin.BeginTx(ctx, opts)
	if err != nil {
		return nil, err
	}
	return &ocrCommitAckTx{Tx: tx, lose: p.lose}, nil
}

type ocrCommitAckTx struct {
	*sql.Tx
	lose *atomic.Int32
}

func (tx ocrCommitAckTx) Commit() error {
	if err := tx.Tx.Commit(); err != nil {
		return err
	}
	if tx.lose.Add(-1) == 0 {
		return io.ErrUnexpectedEOF
	}
	return nil
}

func TestOCRLostSQLCommitAcknowledgement(t *testing.T) {
	for _, phase := range []string{"reserve", "success", "refund"} {
		t.Run(phase, func(t *testing.T) {
			s, a := ocrFixture(t)
			ctx := context.Background()
			j, err := s.CreateIntent(ctx, a)
			require.NoError(t, err)
			if phase != "reserve" {
				_, err = s.Reserve(ctx, j.ID)
				require.NoError(t, err)
			}
			if phase == "success" {
				claim, err := s.ClaimSubmission(ctx, j.ID, ocrTestSelection(a.ChannelID))
				require.NoError(t, err)
				require.NoError(t, s.RecordSubmission(ctx, claim, "fixture", false))
			}
			var lose atomic.Int32
			pool := ocrCommitAckPool{ConnPool: s.db.ConnPool, begin: s.db.ConnPool.(gorm.TxBeginner), lose: &lose}
			faultDB := s.db.Session(&gorm.Session{Context: ctx, NewDB: true})
			faultDB.ConnPool = pool
			faultDB.Statement.ConnPool = pool
			faultStore := NewOCRBillingStore(faultDB, nil)
			lose.Store(1)
			if phase == "refund" {
				lose.Store(2)
			}
			switch phase {
			case "reserve":
				_, err = faultStore.Reserve(ctx, j.ID)
			case "success":
				_, err = faultStore.Finish(ctx, j.ID, "success")
			case "refund":
				_, err = faultStore.Finish(ctx, j.ID, "cancel")
			}
			require.ErrorIs(t, err, io.ErrUnexpectedEOF)
			recovered := NewOCRBillingStore(s.db, nil)
			if phase == "reserve" {
				_, err = recovered.Reserve(ctx, j.ID)
			} else {
				outcome := "success"
				if phase == "refund" {
					outcome = "cancel"
				}
				_, err = recovered.Finish(ctx, j.ID, outcome)
			}
			require.NoError(t, err)
			wallet, remain, used := 900, 400, 100
			if phase == "refund" {
				wallet, remain, used = 1000, 500, 0
			}
			ocrBalances(t, s, a, wallet, remain, used)
			journal, err := recovered.Inspect(ctx, j.ID)
			require.NoError(t, err)
			for _, op := range journal.Operations {
				if op.Kind == "subscription" {
					continue
				}
				assert.Equal(t, "applied", op.State)
			}
			var count int64
			require.NoError(t, s.db.Model(&OCRAuditEvent{}).Where("job_id = ?", j.ID).Count(&count).Error)
			if phase == "reserve" {
				assert.Zero(t, count)
			} else {
				assert.EqualValues(t, 1, count)
			}
		})
	}
}

func TestOCRConflictingTerminalTransactions(t *testing.T) {
	for _, dialect := range []string{"mysql57", "postgres96"} {
		t.Run(dialect, func(t *testing.T) {
			for _, winningOutcome := range []string{"success", "failure", "cancel"} {
				t.Run(winningOutcome, func(t *testing.T) {
					s, a, _, _ := ocrConcurrentFixture(t, dialect)
					a.Preference = "wallet_only"
					ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
					defer cancel()
					j, err := s.CreateIntent(ctx, a)
					require.NoError(t, err)
					_, err = s.Reserve(ctx, j.ID)
					require.NoError(t, err)
					if winningOutcome != "cancel" {
						claim, err := s.ClaimSubmission(ctx, j.ID, ocrTestSelection(a.ChannelID))
						require.NoError(t, err)
						require.NoError(t, s.RecordSubmission(ctx, claim, "fixture", false))
					}
					locked, losersReady := make(chan struct{}), make(chan struct{}, 2)
					var winnerOnce sync.Once
					var contenderOnce [2]sync.Once
					require.NoError(t, s.db.Callback().Query().After("gorm:query").Register("ocr_terminal_winner", func(tx *gorm.DB) {
						if _, ok := tx.Statement.Clauses["FOR"]; !ok {
							return
						}
						if tx.Statement.Context.Value(ocrRaceContextKey{}) != "winner" {
							return
						}
						if _, ok := tx.Statement.Dest.(*OCRJob); ok {
							winnerOnce.Do(func() {
								close(locked)
								for i := 0; i < 2; i++ {
									select {
									case <-losersReady:
									case <-ctx.Done():
										tx.AddError(ctx.Err())
									}
								}
							})
						}
					}))
					require.NoError(t, s.db.Callback().Query().Before("gorm:query").Register("ocr_terminal_contender", func(tx *gorm.DB) {
						if _, ok := tx.Statement.Clauses["FOR"]; !ok {
							return
						}
						idx, ok := tx.Statement.Context.Value(ocrRaceContextKey{}).(int)
						if !ok {
							return
						}
						if _, ok := tx.Statement.Dest.(*OCRJob); ok {
							contenderOnce[idx].Do(func() { losersReady <- struct{}{} })
						}
					}))
					winnerResult := make(chan error, 1)
					go func() {
						_, err := s.Finish(context.WithValue(ctx, ocrRaceContextKey{}, "winner"), j.ID, winningOutcome)
						winnerResult <- err
					}()
					select {
					case <-locked:
					case <-ctx.Done():
						t.Fatal(ctx.Err())
					}
					loserResults := make(chan error, 2)
					idx := 0
					for _, outcome := range []string{"success", "failure", "cancel"} {
						if outcome == winningOutcome {
							continue
						}
						go func(index int, outcome string) {
							_, err := s.Finish(context.WithValue(ctx, ocrRaceContextKey{}, index), j.ID, outcome)
							loserResults <- err
						}(idx, outcome)
						idx++
					}
					require.NoError(t, <-winnerResult)
					for i := 0; i < 2; i++ {
						require.ErrorIs(t, <-loserResults, ErrOCRConflict)
					}
					wallet, remain, used := 1000, 1000, 0
					wantState := "refunded"
					if winningOutcome == "success" {
						wallet, remain, used = 900, 900, 100
						wantState = "settled"
					}
					ocrBalances(t, s, a, wallet, remain, used)
					journal, err := s.Inspect(ctx, j.ID)
					require.NoError(t, err)
					assert.Equal(t, winningOutcome, journal.Job.Decision)
					assert.Equal(t, wantState, journal.Job.BillingState)
					var events []OCRAuditEvent
					require.NoError(t, s.db.Where("job_id = ?", j.ID).Find(&events).Error)
					require.Len(t, events, 1)
					assert.Equal(t, winningOutcome, events[0].Kind)
					var user User
					require.NoError(t, s.db.First(&user, a.UserID).Error)
					if winningOutcome == "success" {
						assert.Equal(t, 1, user.RequestCount)
						assert.Equal(t, 100, user.UsedQuota)
					} else {
						assert.Zero(t, user.RequestCount)
						assert.Zero(t, user.UsedQuota)
					}
				})
			}
		})
	}
}

func TestOCRResetRefundSerialization(t *testing.T) {
	for _, dialect := range []string{"mysql57", "postgres96"} {
		t.Run(dialect, func(t *testing.T) {
			for _, first := range []string{"reset", "refund"} {
				t.Run(first, func(t *testing.T) {
					s, a, sub, plan := ocrConcurrentFixture(t, dialect)
					a.Preference = "subscription_only"
					ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
					defer cancel()
					j, err := s.CreateIntent(ctx, a)
					require.NoError(t, err)
					_, err = s.Reserve(ctx, j.ID)
					require.NoError(t, err)
					locked, contenderReady := make(chan struct{}), make(chan struct{})
					var firstOnce, secondOnce sync.Once
					require.NoError(t, s.db.Callback().Query().After("gorm:query").Register("ocr_reset_first", func(tx *gorm.DB) {
						if _, ok := tx.Statement.Clauses["FOR"]; !ok {
							return
						}
						if tx.Statement.Context.Value(ocrRaceContextKey{}) != first {
							return
						}
						if _, ok := tx.Statement.Dest.(*UserSubscription); ok {
							firstOnce.Do(func() {
								close(locked)
								select {
								case <-contenderReady:
								case <-ctx.Done():
									tx.AddError(ctx.Err())
								}
							})
						}
					}))
					require.NoError(t, s.db.Callback().Query().Before("gorm:query").Register("ocr_reset_contender", func(tx *gorm.DB) {
						if _, ok := tx.Statement.Clauses["FOR"]; !ok {
							return
						}
						label, ok := tx.Statement.Context.Value(ocrRaceContextKey{}).(string)
						if !ok || label == first {
							return
						}
						if _, ok := tx.Statement.Dest.(*UserSubscription); ok {
							secondOnce.Do(func() { close(contenderReady) })
						}
					}))
					run := func(kind string) error {
						callCtx := context.WithValue(ctx, ocrRaceContextKey{}, kind)
						if kind == "refund" {
							_, err := s.Finish(callCtx, j.ID, "cancel")
							return err
						}
						return s.db.WithContext(callCtx).Transaction(func(tx *gorm.DB) error {
							var current UserSubscription
							if err := lockForUpdate(tx).First(&current, sub.Id).Error; err != nil {
								return err
							}
							if err := resetUserSubscriptionTx(tx, &current, &plan, time.Now().Unix(), false); err != nil {
								return err
							}
							return tx.Model(&current).Update("amount_used", 37).Error
						})
					}
					firstResult := make(chan error, 1)
					go func() { firstResult <- run(first) }()
					select {
					case <-locked:
					case <-ctx.Done():
						t.Fatal(ctx.Err())
					}
					second := "reset"
					if first == "reset" {
						second = "refund"
					}
					secondErr := run(second)
					firstErr := <-firstResult
					require.NoError(t, firstErr)
					require.NoError(t, secondErr)
					require.NoError(t, s.db.First(&sub, sub.Id).Error)
					assert.EqualValues(t, 37, sub.AmountUsed)
					assert.EqualValues(t, 1, sub.ResetGeneration)
					ocrBalances(t, s, a, 1000, 1000, 0)
					journal, err := s.Inspect(ctx, j.ID)
					require.NoError(t, err)
					assert.Equal(t, "refunded", journal.Job.BillingState)
				})
			}
		})
	}
}
