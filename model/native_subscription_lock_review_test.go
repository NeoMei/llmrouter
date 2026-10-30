package model

import (
	"context"
	"net/url"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/metering"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// Opt-in, disposable local database only. No connection string is logged.
func nativeReviewPostgres(t *testing.T) {
	t.Helper()
	dsn := os.Getenv("NATIVE_REALM_LOCK_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("NATIVE_REALM_LOCK_POSTGRES_DSN not configured")
	}
	u, err := url.Parse(dsn)
	require.NoError(t, err)
	require.True(t, u.Scheme == "postgres" || u.Scheme == "postgresql")
	require.Equal(t, "127.0.0.1", u.Hostname())
	require.Equal(t, "/lr_native_review", u.Path)
	require.Empty(t, u.Fragment)
	for key, values := range u.Query() {
		require.Equal(t, "sslmode", key)
		require.Equal(t, []string{"disable"}, values)
	}
	for _, key := range []string{"PGHOST", "PGHOSTADDR", "PGPORT", "PGDATABASE", "PGUSER", "PGPASSWORD", "PGSERVICE", "PGSERVICEFILE"} {
		require.Empty(t, os.Getenv(key), "PostgreSQL routing environment must be unset")
	}
	db, err := gorm.Open(postgres.New(postgres.Config{DSN: dsn, PreferSimpleProtocol: true}), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	conn, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&User{}, &Token{}, &Channel{}, &SubscriptionPlan{}, &UserSubscription{}, &OCRJob{}, &OCROperation{}, &OCRAuditEvent{}))
	require.NoError(t, conn.Close())
	t.Setenv("LLMROUTER_ACCOUNT_POOL_POSTGRES_DSN", dsn)
}

func TestNativeSubscriptionManagementSerializesWithOCR(t *testing.T) {
	nativeReviewPostgres(t)
	for _, mutation := range []string{"invalidate", "delete", "reset-user", "reset-plan"} {
		for _, operation := range []string{"reserve", "refund"} {
			t.Run(mutation+"/"+operation, func(t *testing.T) {
				s, a, sub, plan := ocrConcurrentFixture(t, "postgres96")
				require.NoError(t, s.db.Model(&User{}).Where("id = ?", a.UserID).Update("funding_realm", NativeFundingLocalOnly).Error)
				a.Preference = "subscription_only"
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				j, err := s.CreateIntent(ctx, a)
				require.NoError(t, err)
				if operation == "refund" {
					_, err = s.Reserve(ctx, j.ID)
					require.NoError(t, err)
				}
				oldDB, oldType, oldRedis := DB, common.MainDatabaseType(), common.RedisEnabled
				DB = s.db.WithContext(context.WithValue(ctx, ocrRaceContextKey{}, "admin"))
				common.SetMainDatabaseType(common.DatabaseTypePostgreSQL)
				common.RedisEnabled = false
				initCol()
				t.Cleanup(func() { DB = oldDB; common.SetMainDatabaseType(oldType); common.RedisEnabled = oldRedis; initCol() })
				subLocked, adminAtSub := make(chan struct{}), make(chan struct{})
				var held, approaching sync.Once
				require.NoError(t, s.db.Callback().Query().After("gorm:query").Register("native_review_hold_sub", func(tx *gorm.DB) {
					if tx.Statement.Context.Value(ocrRaceContextKey{}) != "ocr" {
						return
					}
					if _, ok := tx.Statement.Clauses["FOR"]; !ok {
						return
					}
					if _, ok := tx.Statement.Dest.(*UserSubscription); ok {
						held.Do(func() {
							close(subLocked)
							select {
							case <-adminAtSub:
							case <-ctx.Done():
								tx.AddError(ctx.Err())
							}
						})
					}
				}))
				require.NoError(t, s.db.Callback().Query().Before("gorm:query").Register("native_review_admin_sub", func(tx *gorm.DB) {
					if tx.Statement.Context.Value(ocrRaceContextKey{}) != "admin" {
						return
					}
					if _, ok := tx.Statement.Clauses["FOR"]; !ok {
						return
					}
					switch tx.Statement.Dest.(type) {
					case *UserSubscription, *[]UserSubscription:
						approaching.Do(func() { close(adminAtSub) })
					}
				}))
				done := make(chan error, 1)
				go func() {
					var e error
					call := context.WithValue(ctx, ocrRaceContextKey{}, "ocr")
					if operation == "reserve" {
						_, e = s.Reserve(call, j.ID)
					} else {
						_, e = s.Finish(call, j.ID, "cancel")
					}
					done <- e
				}()
				select {
				case <-subLocked:
				case <-ctx.Done():
					t.Fatal("OCR did not reach subscription lock")
				}
				switch mutation {
				case "invalidate":
					_, err = AdminInvalidateUserSubscription(sub.Id, NativeFundingLocalOnly)
				case "delete":
					_, err = AdminDeleteUserSubscription(sub.Id, NativeFundingLocalOnly)
				case "reset-user":
					_, err = AdminResetUserSubscriptionsByPlan(a.UserID, plan.Id, false, NativeFundingLocalOnly)
				case "reset-plan":
					_, err = AdminResetPlanSubscriptions(plan.Id, false, NativeFundingLocalOnly)
				}
				require.NoError(t, err, "management must not form a User/subscription lock cycle")
				require.NoError(t, <-done, "OCR must not be a deadlock victim")
				state, err := s.Inspect(ctx, j.ID)
				require.NoError(t, err)
				want := "reserved"
				if operation == "refund" {
					want = "refunded"
				}
				assert.Equal(t, want, state.Job.BillingState)
			})
		}
	}
}

func TestNativeAdmissionRechecksCredentialAfterEnrollmentFence(t *testing.T) {
	nativeReviewPostgres(t)
	s, a, _, _ := ocrConcurrentFixture(t, "postgres96")
	require.NoError(t, s.db.AutoMigrate(&ProductFundingAccount{}, &NativeBilling{}, &NativeUsageReceipt{}, &QuotaData{}))
	oldDB, oldType := DB, common.MainDatabaseType()
	DB = s.db
	common.SetMainDatabaseType(common.DatabaseTypePostgreSQL)
	initCol()
	t.Cleanup(func() { DB = oldDB; common.SetMainDatabaseType(oldType); initCol() })
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ownerApproached, enrolled := make(chan struct{}), make(chan struct{})
	var once sync.Once
	require.NoError(t, s.db.Callback().Query().Before("gorm:query").Register("native_review_admission_owner", func(tx *gorm.DB) {
		if tx.Statement.Context.Value(ocrRaceContextKey{}) != "admission" {
			return
		}
		if _, ok := tx.Statement.Clauses["FOR"]; !ok {
			return
		}
		if _, ok := tx.Statement.Dest.(*User); ok {
			once.Do(func() {
				close(ownerApproached)
				select {
				case <-enrolled:
				case <-ctx.Done():
					tx.AddError(ctx.Err())
				}
			})
		}
	}))
	price := metering.Price{Version: "metered_v2", Operation: "speech", Policy: "provider_reported_v1", Terms: []metering.Term{{Key: "characters", Unit: "character", Price: metering.Rational{Numerator: "1", Denominator: "1000"}}}}
	admission := NativeAdmission{CallID: a.IdempotencyKey, UserID: a.UserID, TokenID: a.TokenID, ChannelID: a.ChannelID, Model: a.Model, Group: a.Group, Preference: "wallet_only", Price: price, GroupRatio: "1", QuotaPerUnit: "100", Reservation: nativeUsage("1000")}
	store := NewNativeBillingStore(s.db, nil)
	done := make(chan error, 1)
	go func() {
		_, err := store.Begin(context.WithValue(ctx, ocrRaceContextKey{}, "admission"), admission)
		done <- err
	}()
	select {
	case <-ownerApproached:
	case <-ctx.Done():
		t.Fatal("admission did not reach owner fence")
	}
	err := PinNativeFundingRealm(ctx, a.UserID, "synthetic-reviewed-cohort")
	close(enrolled)
	require.NoError(t, err)
	require.ErrorIs(t, <-done, ErrNativeCredentialUnverified)
	ocrBalances(t, s, a, 1000, 1000, 0)
	var journals int64
	require.NoError(t, s.db.Model(&NativeBilling{}).Where("user_id = ?", a.UserID).Count(&journals).Error)
	assert.Zero(t, journals, "failed credential admission must not create a recoverable money journal")
}

func TestNativeCachePublicationSerializesWithPolicyUpdate(t *testing.T) {
	nativeReviewPostgres(t)
	s, a, _, _ := ocrConcurrentFixture(t, "postgres96")
	oldDB, oldType := DB, common.MainDatabaseType()
	DB = s.db
	common.SetMainDatabaseType(common.DatabaseTypePostgreSQL)
	initCol()
	t.Cleanup(func() { DB = oldDB; common.SetMainDatabaseType(oldType); initCol() })
	server := useUserCacheMiniRedis(t)
	require.NoError(t, DB.Model(&Token{}).Where("id = ?", a.TokenID).Updates(map[string]any{"auth_realm": NativeFundingLocalOnly, "unlimited_quota": true}).Error)
	var stale Token
	require.NoError(t, DB.First(&stale, a.TokenID).Error)
	enrollNativeQuotaFixture(t, stale.Id)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	locked, updateAtLock := make(chan struct{}), make(chan struct{})
	var first, second sync.Once
	require.NoError(t, DB.Callback().Query().After("gorm:query").Register("native_publish_holds_token", func(tx *gorm.DB) {
		if tx.Statement.Context.Value(ocrRaceContextKey{}) != "publication" {
			return
		}
		if _, ok := tx.Statement.Clauses["FOR"]; !ok {
			return
		}
		if _, ok := tx.Statement.Dest.(*Token); ok {
			first.Do(func() {
				close(locked)
				select {
				case <-updateAtLock:
				case <-ctx.Done():
					tx.AddError(ctx.Err())
				}
			})
		}
	}))
	require.NoError(t, DB.Callback().Query().Before("gorm:query").Register("native_update_awaits_token", func(tx *gorm.DB) {
		if tx.Statement.Context.Value(ocrRaceContextKey{}) != "mutation" {
			return
		}
		if _, ok := tx.Statement.Clauses["FOR"]; !ok {
			return
		}
		if _, ok := tx.Statement.Dest.(*Token); ok {
			second.Do(func() { close(updateAtLock) })
		}
	}))
	DB = s.db.WithContext(context.WithValue(ctx, ocrRaceContextKey{}, "publication"))
	done := make(chan error, 1)
	go func() { _, err := cacheInitToken(stale); done <- err }()
	select {
	case <-locked:
	case <-ctx.Done():
		t.Fatal("publication did not reach token lock")
	}
	// DB is only consulted when each operation starts; neither operation swaps
	// connections or aliases its GORM transaction after this synchronization.
	DB = s.db.WithContext(context.WithValue(ctx, ocrRaceContextKey{}, "mutation"))
	current := stale
	current.UnlimitedQuota = false
	current.RemainQuota = 100
	err := current.Update(NativeFundingLocalOnly)
	publishErr := <-done
	require.NoError(t, err)
	require.NoError(t, publishErr)
	server.FastForward(time.Duration(tokenCacheFenceSeconds+1) * time.Second)
	cached, err := GetTokenByKey(stale.Key, false)
	require.NoError(t, err)
	assert.False(t, cached.UnlimitedQuota)
	assert.Equal(t, 100, cached.RemainQuota)
	staleCode, err := cacheInitToken(stale)
	require.NoError(t, err)
	assert.Zero(t, staleCode)
}

func TestNativeQuotaPolicyAdmissionPostgresTwoConnections(t *testing.T) {
	nativeReviewPostgres(t)
	s, a, _, _ := ocrConcurrentFixture(t, "postgres96")
	oldDB, oldType := DB, common.MainDatabaseType()
	DB = s.db
	common.SetMainDatabaseType(common.DatabaseTypePostgreSQL)
	initCol()
	t.Cleanup(func() { DB = oldDB; common.SetMainDatabaseType(oldType); initCol() })
	server := useUserCacheMiniRedis(t)
	resetBatchUpdateTestState(t)
	common.BatchUpdateEnabled = true
	require.NoError(t, DB.Model(&Token{}).Where("id = ?", a.TokenID).Updates(map[string]any{"auth_realm": NativeFundingLocalOnly, "unlimited_quota": true}).Error)
	var token Token
	require.NoError(t, DB.First(&token, a.TokenID).Error)
	enrollNativeQuotaFixture(t, token.Id)
	_, err := GetTokenByKey(token.Key, false)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	read, release := make(chan struct{}), make(chan struct{})
	var paused atomic.Bool
	require.NoError(t, DB.Callback().Query().After("gorm:query").Register("native_pg_stale_policy", func(tx *gorm.DB) {
		if tx.Statement.Context.Value(ocrRaceContextKey{}) != "reserve" || tx.Statement.Table != "tokens" {
			return
		}
		if paused.CompareAndSwap(false, true) {
			close(read)
			select {
			case <-release:
			case <-ctx.Done():
				tx.AddError(ctx.Err())
			}
		}
	}))
	DB = s.db.WithContext(context.WithValue(ctx, ocrRaceContextKey{}, "reserve"))
	type result struct {
		ok  bool
		err error
	}
	done := make(chan result, 1)
	go func() { ok, err := TryReserveTokenQuota(token.Id, token.Key, 200, true); done <- result{ok, err} }()
	select {
	case <-read:
	case <-ctx.Done():
		t.Fatal("admission did not read original policy")
	}
	DB = s.db.WithContext(context.WithValue(ctx, ocrRaceContextKey{}, "update"))
	token.RemainQuota = 100
	token.UnlimitedQuota = false
	updateErr := token.Update(NativeFundingLocalOnly)
	server.FastForward(time.Duration(tokenCacheFenceSeconds+1) * time.Second)
	close(release)
	got := <-done
	require.NoError(t, updateErr)
	require.NoError(t, got.err)
	assert.False(t, got.ok)
	// The updated policy's available quota is shared across two DB connections
	// and Redis commands, not capped independently against a stale SQL balance.
	both := make(chan result, 2)
	for i := 0; i < 2; i++ {
		go func() { ok, err := TryReserveTokenQuota(token.Id, token.Key, 60, true); both <- result{ok, err} }()
	}
	accepted := 0
	for i := 0; i < 2; i++ {
		r := <-both
		require.NoError(t, r.err)
		if r.ok {
			accepted++
		}
	}
	assert.Equal(t, 1, accepted)
	batchUpdate()
	assert.Equal(t, 40, getTokenFromDB(t, token.Id).RemainQuota)
}
