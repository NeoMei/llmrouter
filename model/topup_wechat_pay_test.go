package model

import (
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWeChatTopUpMigrationPersistsExactSnapshots(t *testing.T) {
	truncateTables(t)
	assert.True(t, DB.Migrator().HasIndex(&TopUp{}, "idx_topups_provider_trade"))

	providerTradeNo := "4200000000202608311234567890"
	topUp := &TopUp{
		UserId:            701,
		TradeNo:           "WXN0123456789abcdef0123456789abc",
		PaymentMethod:     PaymentMethodWeChatNative,
		PaymentProvider:   PaymentProviderWeChatPay,
		Currency:          "CNY",
		PaidAmountMinor:   5001,
		CreditedQuota:     3425342,
		ProviderTradeNo:   &providerTradeNo,
		NextReconcileTime: 1_788_187_230,
		ReconcileAttempts: 2,
		CreateTime:        1_788_187_200,
		Status:            common.TopUpStatusPending,
	}
	require.NoError(t, topUp.Insert())

	reloaded := GetTopUpByTradeNo(topUp.TradeNo)
	require.NotNil(t, reloaded)
	assert.Equal(t, "CNY", reloaded.Currency)
	assert.Equal(t, int64(5001), reloaded.PaidAmountMinor)
	assert.Equal(t, 3425342, reloaded.CreditedQuota)
	require.NotNil(t, reloaded.ProviderTradeNo)
	assert.Equal(t, providerTradeNo, *reloaded.ProviderTradeNo)
	assert.Equal(t, int64(1_788_187_230), reloaded.NextReconcileTime)
	assert.Equal(t, 2, reloaded.ReconcileAttempts)
}

func TestCreateOrReuseWeChatTopUpReusesIdenticalSnapshot(t *testing.T) {
	truncateTables(t)
	insertUserForPaymentGuardTest(t, 702, 0)

	input := WeChatTopUpOrderInput{
		UserID:        702,
		TradeNo:       "WXN1123456789abcdef0123456789abc",
		AmountMinor:   5000,
		CreditedQuota: 3424658,
		CreateTime:    time.Date(2026, 8, 31, 22, 0, 0, 0, time.UTC).Unix(),
	}
	created, reused, err := CreateOrReuseWeChatTopUp(input)
	require.NoError(t, err)
	assert.False(t, reused)
	assert.Equal(t, PaymentProviderWeChatPay, created.PaymentProvider)
	assert.Equal(t, PaymentMethodWeChatNative, created.PaymentMethod)
	assert.Equal(t, "CNY", created.Currency)
	assert.Equal(t, input.AmountMinor, created.PaidAmountMinor)
	assert.Equal(t, input.CreditedQuota, created.CreditedQuota)
	assert.Equal(t, input.CreateTime+30, created.NextReconcileTime)
	assert.Equal(t, common.TopUpStatusPending, created.Status)

	retry := input
	retry.CreateTime += 60
	winner, reused, err := CreateOrReuseWeChatTopUp(retry)
	require.NoError(t, err)
	assert.True(t, reused)
	assert.Equal(t, created.Id, winner.Id)
	assert.Equal(t, input.CreateTime, winner.CreateTime)
	assert.Equal(t, input.CreateTime+30, winner.NextReconcileTime)

	var count int64
	require.NoError(t, DB.Model(&TopUp{}).Where("trade_no = ?", input.TradeNo).Count(&count).Error)
	assert.Equal(t, int64(1), count)
}

func TestCreateOrReuseWeChatTopUpRejectsConflictingSnapshot(t *testing.T) {
	truncateTables(t)
	insertUserForPaymentGuardTest(t, 703, 0)

	input := WeChatTopUpOrderInput{
		UserID:        703,
		TradeNo:       "WXN2123456789abcdef0123456789abc",
		AmountMinor:   5000,
		CreditedQuota: 3424658,
		CreateTime:    1_788_187_200,
	}
	_, _, err := CreateOrReuseWeChatTopUp(input)
	require.NoError(t, err)

	input.AmountMinor++
	_, _, err = CreateOrReuseWeChatTopUp(input)
	require.ErrorIs(t, err, ErrTopUpOrderConflict)
}

func createWeChatTopUpForTest(t *testing.T, userID int, tradeNo string, amountMinor int64, creditedQuota int) *TopUp {
	t.Helper()
	created, reused, err := CreateOrReuseWeChatTopUp(WeChatTopUpOrderInput{
		UserID:        userID,
		TradeNo:       tradeNo,
		AmountMinor:   amountMinor,
		CreditedQuota: creditedQuota,
		CreateTime:    time.Now().Unix(),
	})
	require.NoError(t, err)
	assert.False(t, reused)
	return created
}

func settleWeChatTopUpForTest(t *testing.T, topUp *TopUp, providerTradeNo string) (bool, int, int, error) {
	t.Helper()
	return SettleWeChatTopUp(WeChatTopUpSettlement{
		TradeNo:         topUp.TradeNo,
		ProviderTradeNo: providerTradeNo,
		AmountMinor:     topUp.PaidAmountMinor,
		Currency:        topUp.Currency,
		CallerIP:        "127.0.0.1",
	})
}

func TestSettleWeChatTopUpCreditsStoredQuotaExactlyOnce(t *testing.T) {
	truncateTables(t)
	user := insertUserForPaymentGuardTest(t, 704, 10)
	topUp := createWeChatTopUpForTest(t, user.Id, "WXN3123456789abcdef0123456789abc", 5000, 3424658)

	oldExchangeRate := operation_setting.USDExchangeRate
	oldQuotaPerUnit := common.QuotaPerUnit
	operation_setting.USDExchangeRate = 1000
	common.QuotaPerUnit = 1
	t.Cleanup(func() {
		operation_setting.USDExchangeRate = oldExchangeRate
		common.QuotaPerUnit = oldQuotaPerUnit
	})

	alreadyDone, userID, creditedQuota, err := settleWeChatTopUpForTest(t, topUp, "4200000000202608310000000001")
	require.NoError(t, err)
	assert.False(t, alreadyDone)
	assert.Equal(t, user.Id, userID)
	assert.Equal(t, topUp.CreditedQuota, creditedQuota)
	assert.Equal(t, 10+topUp.CreditedQuota, getUserQuotaForPaymentGuardTest(t, user.Id))

	reloaded := GetTopUpByTradeNo(topUp.TradeNo)
	require.NotNil(t, reloaded)
	assert.Equal(t, common.TopUpStatusSuccess, reloaded.Status)
	require.NotNil(t, reloaded.ProviderTradeNo)
	assert.Equal(t, "4200000000202608310000000001", *reloaded.ProviderTradeNo)
	assert.NotZero(t, reloaded.CompleteTime)

	alreadyDone, userID, creditedQuota, err = settleWeChatTopUpForTest(t, topUp, "4200000000202608310000000001")
	require.NoError(t, err)
	assert.True(t, alreadyDone)
	assert.Equal(t, user.Id, userID)
	assert.Equal(t, topUp.CreditedQuota, creditedQuota)
	assert.Equal(t, 10+topUp.CreditedQuota, getUserQuotaForPaymentGuardTest(t, user.Id))
}

func TestSettleWeChatTopUpConcurrentCallsCreditOnce(t *testing.T) {
	truncateTables(t)
	user := insertUserForPaymentGuardTest(t, 705, 0)
	topUp := createWeChatTopUpForTest(t, user.Id, "WXN4123456789abcdef0123456789abc", 100, 68493)

	start := make(chan struct{})
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, _, _, err := settleWeChatTopUpForTest(t, topUp, "4200000000202608310000000002")
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	assert.Equal(t, topUp.CreditedQuota, getUserQuotaForPaymentGuardTest(t, user.Id))
}

func TestSettleWeChatTopUpRejectsSnapshotAndStateMismatches(t *testing.T) {
	tests := []struct {
		name       string
		mutateTop  func(topUp *TopUp)
		mutateCall func(settlement *WeChatTopUpSettlement)
		wantErr    error
	}{
		{name: "amount mismatch", mutateCall: func(settlement *WeChatTopUpSettlement) { settlement.AmountMinor++ }, wantErr: ErrTopUpAmountMismatch},
		{name: "currency mismatch", mutateCall: func(settlement *WeChatTopUpSettlement) { settlement.Currency = "USD" }, wantErr: ErrTopUpCurrencyMismatch},
		{name: "foreign provider", mutateTop: func(topUp *TopUp) { topUp.PaymentProvider = PaymentProviderStripe }, wantErr: ErrPaymentMethodMismatch},
		{name: "terminal failure", mutateTop: func(topUp *TopUp) { topUp.Status = common.TopUpStatusFailed }, wantErr: ErrTopUpStatusInvalid},
	}

	for index, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			truncateTables(t)
			user := insertUserForPaymentGuardTest(t, 710+index, 0)
			topUp := createWeChatTopUpForTest(t, user.Id, "WXN5123456789abcdef0123456789ab"+string(rune('a'+index)), 5000, 3424658)
			if tt.mutateTop != nil {
				tt.mutateTop(topUp)
				require.NoError(t, DB.Save(topUp).Error)
			}
			settlement := WeChatTopUpSettlement{
				TradeNo:         topUp.TradeNo,
				ProviderTradeNo: "4200000000202608310000000010",
				AmountMinor:     topUp.PaidAmountMinor,
				Currency:        topUp.Currency,
			}
			if tt.mutateCall != nil {
				tt.mutateCall(&settlement)
			}

			_, _, _, err := SettleWeChatTopUp(settlement)
			require.ErrorIs(t, err, tt.wantErr)
			assert.Equal(t, 0, getUserQuotaForPaymentGuardTest(t, user.Id))
		})
	}
}

func TestSettleWeChatTopUpRollsBackQuotaOverflow(t *testing.T) {
	truncateTables(t)
	user := insertUserForPaymentGuardTest(t, 720, common.MaxQuota-10)
	topUp := &TopUp{
		UserId:          user.Id,
		TradeNo:         "WXN6123456789abcdef0123456789abc",
		PaymentMethod:   PaymentMethodWeChatNative,
		PaymentProvider: PaymentProviderWeChatPay,
		Currency:        "CNY",
		PaidAmountMinor: 100,
		CreditedQuota:   20,
		CreateTime:      time.Now().Unix(),
		Status:          common.TopUpStatusPending,
	}
	require.NoError(t, topUp.Insert())

	_, _, _, err := settleWeChatTopUpForTest(t, topUp, "4200000000202608310000000020")
	require.ErrorIs(t, err, ErrTopUpQuotaLimitExceeded)
	assert.Equal(t, common.MaxQuota-10, getUserQuotaForPaymentGuardTest(t, user.Id))
	reloaded := GetTopUpByTradeNo(topUp.TradeNo)
	require.NotNil(t, reloaded)
	assert.Equal(t, common.TopUpStatusPending, reloaded.Status)
	assert.Nil(t, reloaded.ProviderTradeNo)
}

func TestSettleWeChatTopUpRejectsProviderTransactionReuse(t *testing.T) {
	truncateTables(t)
	firstUser := &User{Id: 721, Username: "wechat_transaction_user_1", AffCode: "wechat-aff-721", Status: common.UserStatusEnabled}
	secondUser := &User{Id: 722, Username: "wechat_transaction_user_2", AffCode: "wechat-aff-722", Status: common.UserStatusEnabled}
	require.NoError(t, DB.Create(firstUser).Error)
	require.NoError(t, DB.Create(secondUser).Error)
	first := createWeChatTopUpForTest(t, firstUser.Id, "WXN7123456789abcdef0123456789abc", 100, 68493)
	second := createWeChatTopUpForTest(t, secondUser.Id, "WXN8123456789abcdef0123456789abc", 100, 68493)
	providerTradeNo := "4200000000202608310000000021"

	_, _, _, err := settleWeChatTopUpForTest(t, first, providerTradeNo)
	require.NoError(t, err)
	_, _, _, err = settleWeChatTopUpForTest(t, second, providerTradeNo)
	require.ErrorIs(t, err, ErrTopUpProviderTradeConflict)
	assert.Equal(t, first.CreditedQuota, getUserQuotaForPaymentGuardTest(t, firstUser.Id))
	assert.Equal(t, 0, getUserQuotaForPaymentGuardTest(t, secondUser.Id))
	assert.Equal(t, common.TopUpStatusPending, GetTopUpByTradeNo(second.TradeNo).Status)
}

func TestSettleWeChatTopUpSynchronizesQuotaCacheAfterCommit(t *testing.T) {
	truncateTables(t)
	useUserCacheMiniRedis(t)
	user := insertUserForPaymentGuardTest(t, 723, 7)
	require.NoError(t, populateUserCache(*user))
	topUp := createWeChatTopUpForTest(t, user.Id, "WXN9123456789abcdef0123456789abc", 100, 68493)

	_, _, _, err := settleWeChatTopUpForTest(t, topUp, "4200000000202608310000000022")
	require.NoError(t, err)
	cached, err := cacheGetUserBase(user.Id)
	require.NoError(t, err)
	assert.Equal(t, 7+topUp.CreditedQuota, cached.Quota)
}

func TestManualCompleteWeChatTopUpUsesStoredQuota(t *testing.T) {
	truncateTables(t)
	user := insertUserForPaymentGuardTest(t, 724, 0)
	topUp := createWeChatTopUpForTest(t, user.Id, "WXNa123456789abcdef0123456789abc", 5000, 3424658)

	oldQuotaPerUnit := common.QuotaPerUnit
	common.QuotaPerUnit = 1
	t.Cleanup(func() { common.QuotaPerUnit = oldQuotaPerUnit })

	require.NoError(t, ManualCompleteTopUp(topUp.TradeNo, "127.0.0.1"))
	assert.Equal(t, topUp.CreditedQuota, getUserQuotaForPaymentGuardTest(t, user.Id))
}

func TestClaimDueWeChatTopUpsClaimsOnlyEligiblePendingRows(t *testing.T) {
	truncateTables(t)
	user := insertUserForPaymentGuardTest(t, 725, 0)
	now := int64(1_788_187_300)

	due := createWeChatTopUpForTest(t, user.Id, "WXNb123456789abcdef0123456789abc", 100, 68493)
	due.NextReconcileTime = now
	require.NoError(t, DB.Save(due).Error)

	notDue := createWeChatTopUpForTest(t, user.Id, "WXNc123456789abcdef0123456789abc", 100, 68493)
	notDue.NextReconcileTime = now + 1
	require.NoError(t, DB.Save(notDue).Error)

	foreign := createWeChatTopUpForTest(t, user.Id, "WXNd123456789abcdef0123456789abc", 100, 68493)
	foreign.PaymentProvider = PaymentProviderStripe
	foreign.NextReconcileTime = now
	require.NoError(t, DB.Save(foreign).Error)

	terminal := createWeChatTopUpForTest(t, user.Id, "WXNe123456789abcdef0123456789abc", 100, 68493)
	terminal.Status = common.TopUpStatusSuccess
	terminal.NextReconcileTime = now
	require.NoError(t, DB.Save(terminal).Error)

	assert.True(t, HasDueWeChatTopUps(now))
	claimed, err := ClaimDueWeChatTopUps(now, 10)
	require.NoError(t, err)
	require.Len(t, claimed, 1)
	assert.Equal(t, due.Id, claimed[0].Id)
	assert.Equal(t, 1, claimed[0].ReconcileAttempts)
	assert.Greater(t, claimed[0].NextReconcileTime, now)
	assert.False(t, HasDueWeChatTopUps(now))

	claimed, err = ClaimDueWeChatTopUps(now, 10)
	require.NoError(t, err)
	assert.Empty(t, claimed)
}

func TestClaimDueWeChatTopUpsConcurrentClaimsDoNotDuplicateRows(t *testing.T) {
	truncateTables(t)
	user := insertUserForPaymentGuardTest(t, 726, 0)
	now := time.Now().Unix()
	tradeNos := []string{
		"WXNf123456789abcdef0123456789abc",
		"WXN0123456789abcdef0123456789abd",
	}
	for _, tradeNo := range tradeNos {
		topUp := createWeChatTopUpForTest(t, user.Id, tradeNo, 100, 68493)
		topUp.NextReconcileTime = now
		require.NoError(t, DB.Save(topUp).Error)
	}

	start := make(chan struct{})
	results := make(chan []*TopUp, 2)
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			claimed, err := ClaimDueWeChatTopUps(now, 2)
			results <- claimed
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}

	seen := make(map[int]int)
	for claimed := range results {
		for _, topUp := range claimed {
			seen[topUp.Id]++
		}
	}
	assert.Len(t, seen, 2)
	for _, count := range seen {
		assert.Equal(t, 1, count)
	}
}

func TestGetAndExpireWeChatTopUpEnforceOwnerProviderAndPendingState(t *testing.T) {
	truncateTables(t)
	user := insertUserForPaymentGuardTest(t, 727, 0)
	topUp := createWeChatTopUpForTest(t, user.Id, "WXN1123456789abcdef0123456789abd", 100, 68493)

	found, err := GetUserWeChatTopUp(user.Id, topUp.TradeNo)
	require.NoError(t, err)
	assert.Equal(t, topUp.Id, found.Id)
	_, err = GetUserWeChatTopUp(user.Id+1, topUp.TradeNo)
	require.ErrorIs(t, err, ErrTopUpNotFound)

	require.NoError(t, ExpireWeChatTopUp(topUp.TradeNo))
	assert.Equal(t, common.TopUpStatusExpired, GetTopUpByTradeNo(topUp.TradeNo).Status)
	require.ErrorIs(t, ExpireWeChatTopUp(topUp.TradeNo), ErrTopUpStatusInvalid)
}
