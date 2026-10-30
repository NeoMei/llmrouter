package model

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	userkit "github.com/NeoMei/UniHub/packages/userkit-go"
	"math"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// Native admissions accepted while batch mode is enabled survive loss of the
// accepting process's queue and of Redis. SQL token projection can lag, but the
// accepted hold must remain visible to every other admission.
func TestNativeQuotaAcceptedHoldSurvivesBatchQueueAndRedisLoss(t *testing.T) {
	truncateTables(t)
	resetBatchUpdateTestState(t)
	server := useUserCacheMiniRedis(t)
	common.BatchUpdateEnabled = true
	token := createReserveTestToken(t, 100)
	require.NoError(t, DB.Model(&token).Update("auth_realm", NativeFundingLocalOnly).Error)
	enrollNativeQuotaFixture(t, token.Id)
	ok, err := TryReserveTokenQuota(token.Id, token.Key, 80, false)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, 100, getTokenFromDB(t, token.Id).RemainQuota)
	batchUpdateLocks[BatchUpdateTypeTokenQuota].Lock()
	batchUpdateStores[BatchUpdateTypeTokenQuota] = make(map[int]int)
	batchUpdateLocks[BatchUpdateTypeTokenQuota].Unlock()
	server.FlushAll()
	ok, err = TryReserveTokenQuota(token.Id, token.Key, 30, false)
	require.NoError(t, err)
	assert.False(t, ok, "losing Redis and the accepting instance's RAM queue must not release an accepted hold")
	FlushBatchUpdates()
	assert.Equal(t, 20, getTokenFromDB(t, token.Id).RemainQuota)
	assert.Equal(t, 80, getTokenFromDB(t, token.Id).UsedQuota)
	cached, err := common.RDB.HGet(context.Background(), getTokenCacheKey(token.Key), "RemainQuota").Int()
	require.NoError(t, err)
	assert.Equal(t, 20, cached)
}

// Fixture enrollment is deliberately explicit. Tests use synthetic frozen
// rows; production must provide an independently approved cutover manifest.
func enrollNativeQuotaFixture(t *testing.T, ids ...int) {
	t.Helper()
	require.NoError(t, RegisterNativeTokenQuotaModels(DB))
	manifest := NativeTokenQuotaManifest{Version: 1, CutoverEvidence: strings.Repeat("a", 64)}
	for _, id := range ids {
		var token Token
		require.NoError(t, DB.First(&token, id).Error)
		digest := sha256.Sum256([]byte(token.Key))
		manifest.Tokens = append(manifest.Tokens, NativeTokenQuotaImport{TokenID: token.Id, UserID: token.UserId, KeySHA256: hex.EncodeToString(digest[:]), RemainQuota: token.RemainQuota, UsedQuota: token.UsedQuota, PolicyGeneration: token.QuotaPolicyGeneration, UnlimitedQuota: token.UnlimitedQuota, Status: token.Status, ExpiredTime: token.ExpiredTime})
	}
	raw, err := common.Marshal(manifest)
	require.NoError(t, err)
	digest := sha256.Sum256(raw)
	require.NoError(t, ImportNativeTokenQuota(context.Background(), DB, raw, hex.EncodeToString(digest[:])))
}

func TestNativeQuotaReceiptAndSessionSurviveIndependentReconstruction(t *testing.T) {
	truncateTables(t)
	resetBatchUpdateTestState(t)
	server := useUserCacheMiniRedis(t)
	common.BatchUpdateEnabled = true
	token := createReserveTestToken(t, 100)
	require.NoError(t, DB.Model(&token).Update("auth_realm", NativeFundingLocalOnly).Error)
	enrollNativeQuotaFixture(t, token.Id)
	ok, err := ReserveNativeTokenQuota(token.Id, token.Key, "durable-session", 80)
	require.NoError(t, err)
	require.True(t, ok)
	server.FlushAll()
	ok, err = ReserveNativeTokenQuota(token.Id, token.Key, "durable-session", 80)
	require.NoError(t, err)
	require.True(t, ok)
	remain, used, err := NativeTokenQuotaBalance(context.Background(), token.Id, 0)
	require.NoError(t, err)
	assert.Equal(t, 20, remain)
	assert.Equal(t, 80, used)
	require.NoError(t, SettleNativeTokenQuota(token.Id, token.Key, "durable-session", 60))
	require.NoError(t, SettleNativeTokenQuota(token.Id, token.Key, "durable-session", 60))
	require.ErrorIs(t, RefundNativeTokenQuota(token.Id, token.Key, "durable-session"), ErrNativeQuotaConflict)
	require.ErrorIs(t, SettleNativeTokenQuota(token.Id, token.Key, "durable-session", 70), ErrNativeQuotaConflict)
	require.NoError(t, FlushNativeTokenQuotaProjections())
	assert.Equal(t, 40, getTokenFromDB(t, token.Id).RemainQuota)
	assert.Equal(t, 60, getTokenFromDB(t, token.Id).UsedQuota)
}

func TestNativeQuotaSameOperationRejectsDifferentAmountAndUnknownFunding(t *testing.T) {
	truncateTables(t)
	resetBatchUpdateTestState(t)
	useUserCacheMiniRedis(t)
	common.BatchUpdateEnabled = true
	token := createReserveTestToken(t, 100)
	require.NoError(t, DB.Model(&token).Update("auth_realm", NativeFundingLocalOnly).Error)
	enrollNativeQuotaFixture(t, token.Id)
	ok, err := TryReserveTokenQuota(token.Id, token.Key, 60, false, "same-operation")
	require.NoError(t, err)
	require.True(t, ok)
	ok, err = TryReserveTokenQuota(token.Id, token.Key, 61, false, "same-operation")
	require.ErrorIs(t, err, ErrNativeQuotaConflict)
	assert.False(t, ok)
	ok, err = ReserveNativeTokenQuota(token.Id, token.Key, "unknown-funding", 20)
	require.NoError(t, err)
	require.True(t, ok)
	require.NoError(t, BeginNativeTokenFundingStage(token.Id, "unknown-funding", "preconsume", 20))
	require.ErrorIs(t, RefundNativeTokenQuota(token.Id, token.Key, "unknown-funding"), ErrNativeFundingReconciliation)
	require.ErrorIs(t, SettleNativeTokenQuota(token.Id, token.Key, "unknown-funding", 10), ErrNativeFundingReconciliation)
	require.ErrorIs(t, BeginNativeTokenFundingStage(token.Id, "unknown-funding", "preconsume", 20), ErrNativeFundingReconciliation)
	remain, used, err := NativeTokenQuotaBalance(context.Background(), token.Id, 0)
	require.NoError(t, err)
	assert.Equal(t, 20, remain)
	assert.Equal(t, 80, used)
}

func TestNativeQuotaRequiresExplicitFrozenImport(t *testing.T) {
	truncateTables(t)
	resetBatchUpdateTestState(t)
	token := createReserveTestToken(t, 100)
	require.NoError(t, DB.Model(&token).Update("auth_realm", NativeFundingLocalOnly).Error)
	ok, err := TryReserveTokenQuota(token.Id, token.Key, 1, false, "not-enrolled")
	require.ErrorIs(t, err, ErrNativeQuotaNotEnrolled)
	assert.False(t, ok)
	assert.Equal(t, 100, getTokenFromDB(t, token.Id).RemainQuota)
	var stateCount int64
	require.NoError(t, DB.Model(&NativeTokenQuotaState{}).Where("token_id = ?", token.Id).Count(&stateCount).Error)
	assert.Zero(t, stateCount)
}

func TestNativeQuotaPreservesExistingLargeBalancesAndRejectsOverflow(t *testing.T) {
	truncateTables(t)
	resetBatchUpdateTestState(t)
	token := createReserveTestToken(t, int(1<<32))
	require.NoError(t, DB.Model(&token).Update("auth_realm", NativeFundingLocalOnly).Error)
	enrollNativeQuotaFixture(t, token.Id)
	ok, err := TryReserveTokenQuota(token.Id, token.Key, 60, false, "large-reserve")
	require.NoError(t, err)
	require.True(t, ok)
	require.NoError(t, IncreaseTokenQuota(token.Id, token.Key, 60, "large-refund"))
	assert.Equal(t, int(1<<32), getTokenFromDB(t, token.Id).RemainQuota)
	_, err = nativeQuotaCheckedAdd(math.MaxInt, 1)
	require.ErrorIs(t, err, ErrNativeQuotaConflict)
	_, err = nativeQuotaCheckedAdd(math.MinInt, -1)
	require.ErrorIs(t, err, ErrNativeQuotaConflict)
}

func TestNativeFundingEarlierStageCannotReplayAfterLaterStage(t *testing.T) {
	truncateTables(t)
	resetBatchUpdateTestState(t)
	token := createReserveTestToken(t, 100)
	require.NoError(t, DB.Model(&token).Update("auth_realm", NativeFundingLocalOnly).Error)
	enrollNativeQuotaFixture(t, token.Id)
	ok, err := ReserveNativeTokenQuota(token.Id, token.Key, "funding-stage-history", 20)
	require.NoError(t, err)
	require.True(t, ok)
	require.NoError(t, BeginNativeTokenFundingStage(token.Id, "funding-stage-history", "preconsume", 20))
	require.NoError(t, FinishNativeTokenFundingStage(token.Id, "funding-stage-history", "preconsume", "confirmed"))
	require.NoError(t, BeginNativeTokenFundingStage(token.Id, "funding-stage-history", "reserve:30", 10))
	require.NoError(t, FinishNativeTokenFundingStage(token.Id, "funding-stage-history", "reserve:30", "confirmed"))
	require.ErrorIs(t, BeginNativeTokenFundingStage(token.Id, "funding-stage-history", "preconsume", 20), ErrNativeFundingReconciliation)
	var operations int64
	require.NoError(t, DB.Model(&NativeTokenFundingOperation{}).Where("session_id = ?", "funding-stage-history").Count(&operations).Error)
	assert.EqualValues(t, 2, operations)
}

func TestNativeTokenCachePreservesIssuedPurposeAndOrigin(t *testing.T) {
	truncateTables(t)
	resetBatchUpdateTestState(t)
	useUserCacheMiniRedis(t)
	common.BatchUpdateEnabled = true
	// Equivalent to the existing native chat credential issuer: immutable
	// purpose/origin are supplied to Insert, alongside native realm and TTL.
	enrollNativeQuotaFixture(t)
	token := Token{UserId: 1, Key: "scoped-chat-fixture", Name: "chat:preset", AuthRealm: NativeFundingLocalOnly, Status: common.TokenStatusEnabled, UnlimitedQuota: true, Purpose: "chat_embed", BoundOrigin: "https://chat.example", ExpiredTime: common.GetTimestamp() + 300}
	require.NoError(t, token.Insert())
	_, err := GetTokenByKey(token.Key, false)
	require.NoError(t, err)
	cached, err := cacheGetTokenByKey(token.Key)
	require.NoError(t, err)
	assert.Equal(t, "chat_embed", cached.Purpose)
	assert.Equal(t, "https://chat.example", cached.BoundOrigin)
	assert.Equal(t, NativeFundingLocalOnly, cached.AuthRealm)
}

func TestNativeQuotaAcceptedReservationAccountsAfterSoftDeleteWithoutRevival(t *testing.T) {
	for _, terminal := range []string{"settle", "refund"} {
		t.Run(terminal, func(t *testing.T) {
			truncateTables(t)
			resetBatchUpdateTestState(t)
			useUserCacheMiniRedis(t)
			common.BatchUpdateEnabled = true
			token := createReserveTestToken(t, 100)
			require.NoError(t, DB.Model(&token).Update("auth_realm", NativeFundingLocalOnly).Error)
			enrollNativeQuotaFixture(t, token.Id)
			ok, err := ReserveNativeTokenQuota(token.Id, token.Key, "accepted-before-delete", 60)
			require.NoError(t, err)
			require.True(t, ok)
			require.NoError(t, token.Delete())
			ok, err = ReserveNativeTokenQuota(token.Id, token.Key, "new-after-delete", 1)
			require.Error(t, err)
			assert.False(t, ok)
			if terminal == "settle" {
				require.NoError(t, SettleNativeTokenQuota(token.Id, token.Key, "accepted-before-delete", 40))
			} else {
				require.NoError(t, RefundNativeTokenQuota(token.Id, token.Key, "accepted-before-delete"))
			}
			require.NoError(t, FlushNativeTokenQuotaProjections())
			var deleted Token
			require.NoError(t, DB.Unscoped().First(&deleted, token.Id).Error)
			assert.True(t, deleted.DeletedAt.Valid)
			if terminal == "settle" {
				assert.Equal(t, 60, deleted.RemainQuota)
				assert.Equal(t, 40, deleted.UsedQuota)
			} else {
				assert.Equal(t, 100, deleted.RemainQuota)
				assert.Zero(t, deleted.UsedQuota)
			}
			_, err = GetTokenByKey(token.Key, true, true)
			require.ErrorIs(t, err, gorm.ErrRecordNotFound)
		})
	}
}

func TestNativeQuotaOrdinaryAndOCRShareOneAdmissionCap(t *testing.T) {
	store, admission := ocrFixture(t)
	previousDB := DB
	DB = store.db
	t.Cleanup(func() { DB = previousDB })
	resetBatchUpdateTestState(t)
	useUserCacheMiniRedis(t)
	common.BatchUpdateEnabled = true
	require.NoError(t, DB.Model(&Token{}).Where("id = ?", admission.TokenID).Updates(map[string]any{"auth_realm": NativeFundingLocalOnly, "remain_quota": 100}).Error)
	enrollNativeQuotaFixture(t, admission.TokenID)
	var token Token
	require.NoError(t, DB.First(&token, admission.TokenID).Error)
	ok, err := TryReserveTokenQuota(token.Id, token.Key, 80, false, "ordinary-before-ocr")
	require.NoError(t, err)
	require.True(t, ok)
	admission.Price, admission.GroupRatio, admission.QuotaPerUnit = 30, 1, 1
	job, err := store.CreateIntent(context.Background(), admission)
	require.NoError(t, err)
	_, err = store.Reserve(context.Background(), job.ID)
	require.ErrorIs(t, err, ErrOCRInsufficient, "OCR must use the same durable native remainder as ordinary relay")
	require.NoError(t, FlushNativeTokenQuotaProjections())
	assert.Equal(t, 20, getTokenFromDB(t, token.Id).RemainQuota)
	assert.Equal(t, 80, getTokenFromDB(t, token.Id).UsedQuota)
}

func TestNativeQuotaOCRRedisLegsKeepIndependentReceiptsAndRefund(t *testing.T) {
	store, admission := ocrFixture(t)
	previousDB := DB
	DB = store.db
	t.Cleanup(func() { DB = previousDB })
	resetBatchUpdateTestState(t)
	useUserCacheMiniRedis(t)
	common.BatchUpdateEnabled = true
	redisStore := NewOCRBillingStore(store.db, common.RDB)
	defer redisStore.Close()
	require.NoError(t, DB.Model(&Token{}).Where("id = ?", admission.TokenID).Updates(map[string]any{"auth_realm": NativeFundingLocalOnly, "remain_quota": 100}).Error)
	enrollNativeQuotaFixture(t, admission.TokenID)
	var token Token
	require.NoError(t, DB.First(&token, admission.TokenID).Error)
	ok, err := TryReserveTokenQuota(token.Id, token.Key, 80, false, "ordinary-before-redis-ocr")
	require.NoError(t, err)
	require.True(t, ok)
	admission.Price, admission.GroupRatio, admission.QuotaPerUnit = 30, 1, 1
	job, err := redisStore.CreateIntent(context.Background(), admission)
	require.NoError(t, err)
	_, err = redisStore.Reserve(context.Background(), job.ID)
	require.ErrorIs(t, err, ErrOCRInsufficient)
	journal, err := redisStore.Inspect(context.Background(), job.ID)
	require.NoError(t, err)
	for _, op := range journal.Operations {
		if op.Kind == "wallet" {
			assert.Equal(t, "applied", op.State)
			assert.NotEmpty(t, op.CacheAttempt)
		}
		if op.Kind == "token" {
			assert.NotEqual(t, "applied", op.State)
			assert.Empty(t, op.CacheAttempt)
		}
	}
	var wallet User
	require.NoError(t, DB.First(&wallet, admission.UserID).Error)
	assert.Equal(t, 970, wallet.Quota)
	_, err = redisStore.ClaimSubmission(context.Background(), job.ID, ocrTestSelection(admission.ChannelID))
	require.Error(t, err, "a wallet receipt cannot stand in for rejected native token admission")
	for i := 0; i < 2; i++ {
		refunded, err := redisStore.Finish(context.Background(), job.ID, "cancel")
		require.NoError(t, err)
		assert.Equal(t, "refunded", refunded.BillingState)
	}
	require.NoError(t, DB.First(&wallet, admission.UserID).Error)
	assert.Equal(t, 1000, wallet.Quota)
	remain, used, err := NativeTokenQuotaBalance(context.Background(), token.Id, 0)
	require.NoError(t, err)
	assert.Equal(t, 20, remain)
	assert.Equal(t, 80, used)
	// A smaller OCR hold commits its native token receipt and the Redis wallet
	// receipt separately. Cancellation releases each original operation once.
	admission.IdempotencyKey = "ocr-within-remaining"
	admission.Price = 10
	job, err = redisStore.CreateIntent(context.Background(), admission)
	require.NoError(t, err)
	_, err = redisStore.Reserve(context.Background(), job.ID)
	require.NoError(t, err)
	remain, used, err = NativeTokenQuotaBalance(context.Background(), token.Id, 0)
	require.NoError(t, err)
	assert.Equal(t, 10, remain)
	assert.Equal(t, 90, used)
	for i := 0; i < 2; i++ {
		_, err = redisStore.Finish(context.Background(), job.ID, "cancel")
		require.NoError(t, err)
	}
	remain, used, err = NativeTokenQuotaBalance(context.Background(), token.Id, 0)
	require.NoError(t, err)
	assert.Equal(t, 20, remain)
	assert.Equal(t, 80, used)
	require.NoError(t, DB.First(&wallet, admission.UserID).Error)
	assert.Equal(t, 1000, wallet.Quota)
	require.NoError(t, FlushNativeTokenQuotaProjections())
	assert.Equal(t, 20, getTokenFromDB(t, token.Id).RemainQuota)
}

func TestNativeQuotaHistoricalCentralReceiptCannotMutateNativeKey(t *testing.T) {
	store, db, ledger := productFundingFixture(t)
	ctx := context.Background()
	require.NoError(t, RegisterProductRefundModels(db))
	previousDB := DB
	DB = db
	t.Cleanup(func() { DB = previousDB })
	require.NoError(t, store.Import(ctx, 42, "payer", "native-fence-fixture"))
	_, err := store.Reserve(ctx, ProductFundingAdmission{CallID: "historical-central", UserID: 42, TokenID: 7, Model: "m", Operation: "task", PriceSnapshot: `{"p":1}`, Quota: 20})
	require.NoError(t, err)
	require.NoError(t, store.Settle(ctx, "historical-central", 20, `{"used":20}`))
	// Simulate an old historical receipt after an independently approved native
	// classification. This does not grant a central receipt native authority.
	require.NoError(t, db.Model(&Token{}).Where("id = ?", 7).Updates(map[string]any{"auth_realm": NativeFundingLocalOnly, "funding_account_id": ""}).Error)
	enrollNativeQuotaFixture(t, 7)
	var before Token
	require.NoError(t, db.First(&before, 7).Error)
	var userBefore User
	require.NoError(t, db.First(&userBefore, 42).Error)
	balanceBefore, heldBefore, consumedBefore := ledger.balance, ledger.held, ledger.consumed
	require.ErrorIs(t, store.Refund(ctx, "historical-central", "forbidden-native-credit", 10, "fixture"), ErrProductFundingConflict)
	_, err = store.Reserve(ctx, ProductFundingAdmission{CallID: "forbidden-central-on-native", UserID: 42, TokenID: 7, Model: "m", Operation: "task", PriceSnapshot: `{"p":1}`, Quota: 1})
	require.ErrorIs(t, err, ErrProductFundingConflict)
	var after Token
	require.NoError(t, db.First(&after, 7).Error)
	assert.Equal(t, before.AuthRealm, after.AuthRealm)
	assert.Equal(t, before.FundingAccountID, after.FundingAccountID)
	assert.Equal(t, before.RemainQuota, after.RemainQuota)
	assert.Equal(t, before.UsedQuota, after.UsedQuota)
	var userAfter User
	require.NoError(t, db.First(&userAfter, 42).Error)
	assert.Equal(t, userBefore.Quota, userAfter.Quota)
	assert.Equal(t, userBefore.UsedQuota, userAfter.UsedQuota)
	remain, used, err := NativeTokenQuotaBalance(ctx, 7, 0)
	require.NoError(t, err)
	assert.Equal(t, before.RemainQuota, remain)
	assert.Equal(t, before.UsedQuota, used)
	assert.Equal(t, balanceBefore, ledger.balance)
	assert.Equal(t, heldBefore, ledger.held)
	assert.Equal(t, consumedBefore, ledger.consumed)
	var receipts int64
	require.NoError(t, db.Model(&ProductCallRefund{}).Count(&receipts).Error)
	assert.Zero(t, receipts)
}

func TestNativeQuotaMeteredJournalUsesSharedStateAndCreditsOnce(t *testing.T) {
	_, store, admission, _ := nativeFixture(t)
	previousDB := DB
	DB = store.db
	t.Cleanup(func() { DB = previousDB })
	resetBatchUpdateTestState(t)
	useUserCacheMiniRedis(t)
	common.BatchUpdateEnabled = true
	nativeStore := NewNativeBillingStore(store.db, common.RDB)
	defer nativeStore.Close()
	require.NoError(t, DB.Model(&Token{}).Where("id = ?", admission.TokenID).Updates(map[string]any{"auth_realm": NativeFundingLocalOnly, "remain_quota": 100}).Error)
	enrollNativeQuotaFixture(t, admission.TokenID)
	var token Token
	require.NoError(t, DB.First(&token, admission.TokenID).Error)
	ok, err := TryReserveTokenQuota(token.Id, token.Key, 80, false, "ordinary-before-metered")
	require.NoError(t, err)
	require.True(t, ok)
	admission.Reservation = nativeUsage("100") // 10 quota, from current frozen price.
	job, err := nativeStore.Begin(context.Background(), admission)
	require.NoError(t, err)
	remain, used, err := NativeTokenQuotaBalance(context.Background(), token.Id, 0)
	require.NoError(t, err)
	assert.Equal(t, 10, remain)
	assert.Equal(t, 90, used)
	ok, err = TryReserveTokenQuota(token.Id, token.Key, 11, false, "ordinary-after-metered")
	require.NoError(t, err)
	assert.False(t, ok)
	ok, err = TryReserveTokenQuota(token.Id, token.Key, 10, false, "ordinary-exhausts-during-metered")
	require.NoError(t, err)
	require.True(t, ok)
	require.NoError(t, nativeStore.RecordUsage(context.Background(), job.ID, nativeUsage("0")))
	for i := 0; i < 2; i++ {
		finished, err := nativeStore.Finish(context.Background(), job.ID, "measured")
		require.NoError(t, err)
		assert.Equal(t, "settled", finished.State)
	}
	remain, used, err = NativeTokenQuotaBalance(context.Background(), token.Id, 0)
	require.NoError(t, err)
	assert.Equal(t, 10, remain)
	assert.Equal(t, 90, used)
	authenticated, err := ValidateUserToken(token.Key)
	require.NoError(t, err)
	assert.Equal(t, 10, authenticated.RemainQuota, "committed metered refund is spendable despite cached zero before batch projection")
	require.NoError(t, FlushNativeTokenQuotaProjections())
	assert.Equal(t, 10, getTokenFromDB(t, token.Id).RemainQuota)
}

type nativeQuotaNoNetworkTransport struct{ attempts *atomic.Int32 }

func (t nativeQuotaNoNetworkTransport) RoundTrip(*http.Request) (*http.Response, error) {
	t.attempts.Add(1)
	return nil, errors.New("synthetic transport: no network")
}

func TestNativeQuotaCentralZeroReserveRejectsBeforeBindingOrRemote(t *testing.T) {
	truncateTables(t)
	resetBatchUpdateTestState(t)
	useUserCacheMiniRedis(t)
	common.BatchUpdateEnabled = true
	require.NoError(t, RegisterProductFundingModels(DB))
	owner := createReserveTestUser(t, 1000)
	token := createReserveTestToken(t, 100)
	require.NoError(t, DB.Model(&token).Updates(map[string]any{"user_id": owner.Id, "auth_realm": NativeFundingLocalOnly}).Error)
	enrollNativeQuotaFixture(t, token.Id)
	var attempts atomic.Int32
	client, err := userkit.NewRuntimeClient(userkit.ServiceConfig{BaseURL: "http://127.0.0.1:1", ProductID: "llmrouter", InstanceID: "default", ServiceToken: strings.Repeat("t", 32), HTTPClient: &http.Client{Transport: nativeQuotaNoNetworkTransport{attempts: &attempts}}})
	require.NoError(t, err)
	store := NewProductFundingStore(DB, client)
	require.NoError(t, DB.Create(&ProductFundingAccount{ScopeDigest: store.ScopeDigest(), UserID: owner.Id, AccountID: "synthetic-central-payer", Asset: userkit.RuntimeAsset, UnitVersion: userkit.RuntimeUnitVersion, State: "central"}).Error)
	t.Cleanup(func() {
		require.NoError(t, DB.Where("user_id = ?", owner.Id).Delete(&ProductFundingAccount{}).Error)
		require.NoError(t, DB.Where("id = ?", "zero-central-native-key").Delete(&ProductRuntimeCall{}).Error)
	})
	_, err = store.Reserve(context.Background(), ProductFundingAdmission{CallID: "zero-central-native-key", UserID: owner.Id, TokenID: token.Id, Model: "m", Operation: "task", PriceSnapshot: `{"p":0}`, Quota: 0})
	require.ErrorIs(t, err, ErrProductFundingConflict)
	assert.Zero(t, attempts.Load(), "native realm guard must run before any external money request")
	var current Token
	require.NoError(t, DB.First(&current, token.Id).Error)
	assert.Equal(t, NativeFundingLocalOnly, current.AuthRealm)
	assert.Empty(t, current.FundingAccountID)
	assert.Equal(t, 100, current.RemainQuota)
	assert.Zero(t, current.UsedQuota)
	var calls int64
	require.NoError(t, DB.Model(&ProductRuntimeCall{}).Where("id = ?", "zero-central-native-key").Count(&calls).Error)
	assert.Zero(t, calls)
	remain, used, err := NativeTokenQuotaBalance(context.Background(), token.Id, 0)
	require.NoError(t, err)
	assert.Equal(t, 100, remain)
	assert.Zero(t, used)
	accepted, err := TryReserveTokenQuota(token.Id, token.Key, 10, false, "native-after-central-zero")
	require.NoError(t, err)
	assert.True(t, accepted)
}
