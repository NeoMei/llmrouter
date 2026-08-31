package controller

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type fakeControllerWeChatPayProvider struct {
	createRequest service.WeChatPayCreateRequest
	createResult  service.WeChatPayCreateResult
	createErr     error
	createCalls   int
	queryResult   service.WeChatPayQueryResult
	queryErr      error
	closeResult   service.WeChatPayCloseResult
	closeErr      error
	notifyResult  service.WeChatPayNotifyResult
	notifyErr     error
}

func (f *fakeControllerWeChatPayProvider) CreateNative(_ context.Context, request service.WeChatPayCreateRequest) (service.WeChatPayCreateResult, error) {
	f.createCalls++
	f.createRequest = request
	return f.createResult, f.createErr
}

func (f *fakeControllerWeChatPayProvider) QueryOrder(_ context.Context, _ string) (service.WeChatPayQueryResult, error) {
	return f.queryResult, f.queryErr
}

func (f *fakeControllerWeChatPayProvider) CloseOrder(_ context.Context, _ string) (service.WeChatPayCloseResult, error) {
	return f.closeResult, f.closeErr
}

func (f *fakeControllerWeChatPayProvider) ParseNotify(_ context.Context, _ *http.Request) (service.WeChatPayNotifyResult, error) {
	return f.notifyResult, f.notifyErr
}

func setupWeChatPayControllerTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	originalDB := model.DB
	originalLogDB := model.LOG_DB
	originalRedisEnabled := common.RedisEnabled
	originalBatchUpdateEnabled := common.BatchUpdateEnabled
	dsn := fmt.Sprintf("file:wechat-controller-%d?mode=memory&cache=shared", time.Now().UnixNano())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.TopUp{}, &model.Log{}))
	model.DB = db
	model.LOG_DB = db
	common.RedisEnabled = false
	common.BatchUpdateEnabled = false
	common.SetDatabaseTypes(common.DatabaseTypeSQLite, common.DatabaseTypeSQLite)
	t.Cleanup(func() {
		model.DB = originalDB
		model.LOG_DB = originalLogDB
		common.RedisEnabled = originalRedisEnabled
		common.BatchUpdateEnabled = originalBatchUpdateEnabled
	})
	return db
}

func insertWeChatPayControllerUser(t *testing.T, id int, quota int) {
	t.Helper()
	require.NoError(t, model.DB.Create(&model.User{
		Id:       id,
		Username: fmt.Sprintf("wechat-controller-%d", id),
		AffCode:  fmt.Sprintf("wechat-aff-%d", id),
		Status:   common.UserStatusEnabled,
		Quota:    quota,
	}).Error)
}

func validControllerWeChatPayConfig() service.WeChatPayConfig {
	return service.WeChatPayConfig{
		Enabled:        true,
		MchID:          "1295594101",
		AppID:          "wxa0b1bc308cdea2b2",
		NotifyURL:      "https://llmrouter.quukk.com/api/wechat-pay/notify",
		MinAmountMinor: 100,
		MaxAmountMinor: 500000,
		Description:    "LLMRouter wallet top-up",
	}
}

func setWeChatPayControllerDependencies(
	t *testing.T,
	provider service.WeChatPayProvider,
	config service.WeChatPayConfig,
	readyErr error,
) {
	t.Helper()
	originalReady := loadReadyWeChatPayConfig
	originalProvider := loadWeChatPayProvider
	originalCallbackProvider := loadWeChatPayCallbackProvider
	loadReadyWeChatPayConfig = func() (service.WeChatPayConfig, error) {
		return config, readyErr
	}
	loadWeChatPayProvider = func(context.Context) (service.WeChatPayProvider, service.WeChatPayConfig, error) {
		if readyErr != nil {
			return nil, service.WeChatPayConfig{}, readyErr
		}
		return provider, config, nil
	}
	loadWeChatPayCallbackProvider = loadWeChatPayProvider
	t.Cleanup(func() {
		loadReadyWeChatPayConfig = originalReady
		loadWeChatPayProvider = originalProvider
		loadWeChatPayCallbackProvider = originalCallbackProvider
	})
}

func performWeChatNativeCreate(t *testing.T, userID int, body string) (*httptest.ResponseRecorder, map[string]interface{}) {
	t.Helper()
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Set("id", userID)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/api/user/wechat-pay/native", bytes.NewBufferString(body))
	ctx.Request.Header.Set("Content-Type", "application/json")
	RequestWeChatNativePay(ctx)

	response := map[string]interface{}{}
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
	return recorder, response
}

func TestWeChatPayAvailabilityRequiresComplianceAndReadyConfig(t *testing.T) {
	config := validControllerWeChatPayConfig()
	provider := &fakeControllerWeChatPayProvider{}
	setWeChatPayControllerDependencies(t, provider, config, nil)

	paymentSetting := operation_setting.GetPaymentSetting()
	originalConfirmed := paymentSetting.ComplianceConfirmed
	originalVersion := paymentSetting.ComplianceTermsVersion
	t.Cleanup(func() {
		paymentSetting.ComplianceConfirmed = originalConfirmed
		paymentSetting.ComplianceTermsVersion = originalVersion
	})

	paymentSetting.ComplianceConfirmed = false
	paymentSetting.ComplianceTermsVersion = operation_setting.CurrentComplianceTermsVersion
	assert.False(t, isWeChatPayTopUpEnabled())

	paymentSetting.ComplianceConfirmed = true
	assert.True(t, isWeChatPayTopUpEnabled())

	setWeChatPayControllerDependencies(t, provider, config, errors.New("credentials unavailable"))
	assert.False(t, isWeChatPayTopUpEnabled())
}

func TestGetTopUpInfoExposesWeChatPayOnlyWhenReady(t *testing.T) {
	confirmPaymentComplianceForTest(t)
	config := validControllerWeChatPayConfig()
	setWeChatPayControllerDependencies(t, &fakeControllerWeChatPayProvider{}, config, nil)

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/api/user/topup/info", nil)
	GetTopUpInfo(ctx)
	response := map[string]interface{}{}
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
	data, ok := response["data"].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, true, data["enable_wechat_pay"])
	assert.Equal(t, "1.00", data["wechat_pay_min_amount"])
	assert.Equal(t, "5000.00", data["wechat_pay_max_amount"])
}

func TestRequestWeChatNativePayCreatesExactIdempotentOrder(t *testing.T) {
	setupWeChatPayControllerTestDB(t)
	confirmPaymentComplianceForTest(t)
	insertWeChatPayControllerUser(t, 801, 0)
	config := validControllerWeChatPayConfig()
	provider := &fakeControllerWeChatPayProvider{
		createResult: service.WeChatPayCreateResult{
			CodeURL:   "weixin://wxpay/bizpayurl?pr=controller-test",
			RequestID: "wechat-request-1",
		},
	}
	setWeChatPayControllerDependencies(t, provider, config, nil)

	originalExchangeRate := operation_setting.USDExchangeRate
	originalQuotaPerUnit := common.QuotaPerUnit
	operation_setting.USDExchangeRate = 7.3
	common.QuotaPerUnit = 500000
	t.Cleanup(func() {
		operation_setting.USDExchangeRate = originalExchangeRate
		common.QuotaPerUnit = originalQuotaPerUnit
	})

	_, response := performWeChatNativeCreate(t, 801, `{"amount":"50.01","idempotency_key":"123e4567-e89b-12d3-a456-426614174000"}`)
	assert.Equal(t, true, response["success"])
	data, ok := response["data"].(map[string]interface{})
	require.True(t, ok)
	tradeNo := weChatPayTradeNo(801, "123e4567-e89b-12d3-a456-426614174000")
	assert.Equal(t, tradeNo, data["trade_no"])
	assert.Equal(t, "50.01", data["amount"])
	assert.Equal(t, "CNY", data["currency"])
	assert.Equal(t, "weixin://wxpay/bizpayurl?pr=controller-test", data["code_url"])
	assert.Equal(t, float64(3425342), data["credited_quota"])

	assert.Equal(t, 1, provider.createCalls)
	assert.Equal(t, tradeNo, provider.createRequest.OutTradeNo)
	assert.Equal(t, int64(5001), provider.createRequest.AmountMinor)
	assert.Equal(t, time.Unix(provider.createRequest.ExpiresAt.Unix(), 0), provider.createRequest.ExpiresAt)

	stored := model.GetTopUpByTradeNo(tradeNo)
	require.NotNil(t, stored)
	assert.Equal(t, int64(5001), stored.PaidAmountMinor)
	assert.Equal(t, 3425342, stored.CreditedQuota)
	assert.Equal(t, common.TopUpStatusPending, stored.Status)

	_, retryResponse := performWeChatNativeCreate(t, 801, `{"amount":"50.01","idempotency_key":"123e4567-e89b-12d3-a456-426614174000"}`)
	assert.Equal(t, true, retryResponse["success"])
	assert.Equal(t, 2, provider.createCalls)
	assert.Equal(t, provider.createRequest.OutTradeNo, tradeNo)
	var count int64
	require.NoError(t, model.DB.Model(&model.TopUp{}).Where("trade_no = ?", tradeNo).Count(&count).Error)
	assert.Equal(t, int64(1), count)
}

func TestRequestWeChatNativePayRejectsInvalidInputBeforeProvider(t *testing.T) {
	setupWeChatPayControllerTestDB(t)
	confirmPaymentComplianceForTest(t)
	insertWeChatPayControllerUser(t, 802, 0)
	provider := &fakeControllerWeChatPayProvider{}
	setWeChatPayControllerDependencies(t, provider, validControllerWeChatPayConfig(), nil)

	tests := []string{
		`{"amount":"0","idempotency_key":"123e4567-e89b-12d3-a456-426614174000"}`,
		`{"amount":"1.001","idempotency_key":"123e4567-e89b-12d3-a456-426614174000"}`,
		`{"amount":"50","idempotency_key":"short"}`,
	}
	for _, body := range tests {
		_, response := performWeChatNativeCreate(t, 802, body)
		assert.Equal(t, false, response["success"])
	}
	assert.Zero(t, provider.createCalls)
}

func TestRequestWeChatNativePayClassifiesProviderCreateFailures(t *testing.T) {
	tests := []struct {
		name       string
		ambiguous  bool
		wantStatus string
	}{
		{name: "definitive rejection", ambiguous: false, wantStatus: common.TopUpStatusFailed},
		{name: "ambiguous timeout", ambiguous: true, wantStatus: common.TopUpStatusPending},
	}

	for index, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setupWeChatPayControllerTestDB(t)
			confirmPaymentComplianceForTest(t)
			userID := 810 + index
			insertWeChatPayControllerUser(t, userID, 0)
			provider := &fakeControllerWeChatPayProvider{
				createErr: &service.WeChatPayError{
					Operation: "create native order",
					Code:      "TEST_FAILURE",
					Ambiguous: tt.ambiguous,
				},
			}
			setWeChatPayControllerDependencies(t, provider, validControllerWeChatPayConfig(), nil)
			idempotencyKey := fmt.Sprintf("123e4567-e89b-12d3-a456-42661417400%d", index)

			_, response := performWeChatNativeCreate(t, userID, fmt.Sprintf(`{"amount":"50","idempotency_key":"%s"}`, idempotencyKey))
			assert.Equal(t, false, response["success"])
			stored := model.GetTopUpByTradeNo(weChatPayTradeNo(userID, idempotencyKey))
			require.NotNil(t, stored)
			assert.Equal(t, tt.wantStatus, stored.Status)
		})
	}
}

func performWeChatPayNotify(t *testing.T) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/api/wechat-pay/notify", bytes.NewBufferString(`{"fixture":true}`))
	ctx.Request.Header.Set("Content-Type", "application/json")
	WeChatPayNotify(ctx)
	return recorder
}

func successfulWeChatNotify(config service.WeChatPayConfig, topUp *model.TopUp) service.WeChatPayNotifyResult {
	return service.WeChatPayNotifyResult{
		EventID:   "event-success-1",
		EventType: "TRANSACTION.SUCCESS",
		Transaction: service.WeChatPayTransaction{
			AppID:         config.AppID,
			MchID:         config.MchID,
			OutTradeNo:    topUp.TradeNo,
			TransactionID: "4200000000202608310000000081",
			TradeType:     "NATIVE",
			TradeState:    "SUCCESS",
			Currency:      "CNY",
			AmountMinor:   topUp.PaidAmountMinor,
			SuccessTime:   "2026-08-31T22:01:02+08:00",
		},
	}
}

func createControllerWeChatTopUp(t *testing.T, userID int, tradeNo string) *model.TopUp {
	t.Helper()
	topUp, _, err := model.CreateOrReuseWeChatTopUp(model.WeChatTopUpOrderInput{
		UserID:        userID,
		TradeNo:       tradeNo,
		AmountMinor:   5000,
		CreditedQuota: 3424658,
		CreateTime:    time.Now().Unix(),
	})
	require.NoError(t, err)
	return topUp
}

func TestGetWeChatPayOrderReturnsOnlyOwnersLocalState(t *testing.T) {
	setupWeChatPayControllerTestDB(t)
	insertWeChatPayControllerUser(t, 830, 0)
	topUp := createControllerWeChatTopUp(t, 830, "WXN2123456789abcdef0123456789abd")

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Set("id", 830)
	ctx.Params = gin.Params{{Key: "trade_no", Value: topUp.TradeNo}}
	ctx.Request = httptest.NewRequest(http.MethodGet, "/api/user/wechat-pay/orders/"+topUp.TradeNo, nil)
	GetWeChatPayOrder(ctx)
	assert.Equal(t, http.StatusOK, recorder.Code)
	assert.Contains(t, recorder.Body.String(), `"status":"pending"`)

	recorder = httptest.NewRecorder()
	ctx, _ = gin.CreateTestContext(recorder)
	ctx.Set("id", 831)
	ctx.Params = gin.Params{{Key: "trade_no", Value: topUp.TradeNo}}
	ctx.Request = httptest.NewRequest(http.MethodGet, "/api/user/wechat-pay/orders/"+topUp.TradeNo, nil)
	GetWeChatPayOrder(ctx)
	assert.Equal(t, http.StatusNotFound, recorder.Code)
}

func TestWeChatPayNotifySettlesVerifiedSuccessExactlyOnce(t *testing.T) {
	setupWeChatPayControllerTestDB(t)
	insertWeChatPayControllerUser(t, 840, 0)
	topUp := createControllerWeChatTopUp(t, 840, "WXN3123456789abcdef0123456789abd")
	config := validControllerWeChatPayConfig()
	provider := &fakeControllerWeChatPayProvider{notifyResult: successfulWeChatNotify(config, topUp)}
	setWeChatPayControllerDependencies(t, provider, config, nil)

	recorder := performWeChatPayNotify(t)
	assert.Equal(t, http.StatusNoContent, recorder.Code)
	var user model.User
	require.NoError(t, model.DB.First(&user, 840).Error)
	assert.Equal(t, topUp.CreditedQuota, user.Quota)

	recorder = performWeChatPayNotify(t)
	assert.Equal(t, http.StatusNoContent, recorder.Code)
	require.NoError(t, model.DB.First(&user, 840).Error)
	assert.Equal(t, topUp.CreditedQuota, user.Quota)
}

func TestWeChatPayNotifyRejectsUnverifiablePayloads(t *testing.T) {
	setupWeChatPayControllerTestDB(t)
	config := validControllerWeChatPayConfig()
	provider := &fakeControllerWeChatPayProvider{notifyErr: errors.New("SIGNTEST signature verification failed")}
	setWeChatPayControllerDependencies(t, provider, config, nil)

	recorder := performWeChatPayNotify(t)
	assert.Equal(t, http.StatusBadRequest, recorder.Code)
	var count int64
	require.NoError(t, model.DB.Model(&model.Log{}).Count(&count).Error)
	assert.Zero(t, count)
}

func TestWeChatPayNotifyRecordsSemanticIncidentsAndAcknowledges(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(result *service.WeChatPayNotifyResult)
	}{
		{name: "wrong event", mutate: func(result *service.WeChatPayNotifyResult) { result.EventType = "TRANSACTION.CLOSED" }},
		{name: "wrong state", mutate: func(result *service.WeChatPayNotifyResult) { result.Transaction.TradeState = "NOTPAY" }},
		{name: "wrong trade type", mutate: func(result *service.WeChatPayNotifyResult) { result.Transaction.TradeType = "JSAPI" }},
		{name: "wrong merchant", mutate: func(result *service.WeChatPayNotifyResult) { result.Transaction.MchID = "9999999999" }},
		{name: "wrong app", mutate: func(result *service.WeChatPayNotifyResult) { result.Transaction.AppID = "wx0000000000000000" }},
		{name: "wrong amount", mutate: func(result *service.WeChatPayNotifyResult) { result.Transaction.AmountMinor++ }},
		{name: "wrong currency", mutate: func(result *service.WeChatPayNotifyResult) { result.Transaction.Currency = "USD" }},
		{name: "unknown order", mutate: func(result *service.WeChatPayNotifyResult) {
			result.Transaction.OutTradeNo = "WXNunknownorder00000000000000000"
		}},
	}

	for index, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setupWeChatPayControllerTestDB(t)
			userID := 850 + index
			insertWeChatPayControllerUser(t, userID, 0)
			topUp := createControllerWeChatTopUp(t, userID, fmt.Sprintf("WXN4%027d", index))
			config := validControllerWeChatPayConfig()
			result := successfulWeChatNotify(config, topUp)
			tt.mutate(&result)
			provider := &fakeControllerWeChatPayProvider{notifyResult: result}
			setWeChatPayControllerDependencies(t, provider, config, nil)

			recorder := performWeChatPayNotify(t)
			assert.Equal(t, http.StatusNoContent, recorder.Code)
			var count int64
			require.NoError(t, model.DB.Model(&model.Log{}).Count(&count).Error)
			assert.Equal(t, int64(1), count)
			var user model.User
			require.NoError(t, model.DB.First(&user, userID).Error)
			assert.Zero(t, user.Quota)
		})
	}
}

func TestWeChatPayNotifyReturnsRetryableErrorWhenIncidentCannotPersist(t *testing.T) {
	setupWeChatPayControllerTestDB(t)
	insertWeChatPayControllerUser(t, 870, 0)
	topUp := createControllerWeChatTopUp(t, 870, "WXN5123456789abcdef0123456789abd")
	config := validControllerWeChatPayConfig()
	result := successfulWeChatNotify(config, topUp)
	result.Transaction.AmountMinor++
	provider := &fakeControllerWeChatPayProvider{notifyResult: result}
	setWeChatPayControllerDependencies(t, provider, config, nil)

	originalRecorder := recordWeChatPayIncident
	recordWeChatPayIncident = func(model.WeChatPayIncident) error { return errors.New("log database unavailable") }
	t.Cleanup(func() { recordWeChatPayIncident = originalRecorder })

	recorder := performWeChatPayNotify(t)
	assert.Equal(t, http.StatusServiceUnavailable, recorder.Code)
}

func TestWeChatPayNotifyReturnsRetryableErrorOnSettlementDatabaseFailure(t *testing.T) {
	setupWeChatPayControllerTestDB(t)
	insertWeChatPayControllerUser(t, 871, 0)
	topUp := createControllerWeChatTopUp(t, 871, "WXN8123456789abcdef0123456789abd")
	config := validControllerWeChatPayConfig()
	provider := &fakeControllerWeChatPayProvider{notifyResult: successfulWeChatNotify(config, topUp)}
	setWeChatPayControllerDependencies(t, provider, config, nil)

	originalSettlement := settleWeChatTopUp
	settleWeChatTopUp = func(model.WeChatTopUpSettlement) (bool, int, int, error) {
		return false, 0, 0, errors.New("database unavailable")
	}
	t.Cleanup(func() { settleWeChatTopUp = originalSettlement })

	recorder := performWeChatPayNotify(t)
	assert.Equal(t, http.StatusServiceUnavailable, recorder.Code)
}

func TestWeChatPayNotifyReturnsRetryableErrorOnOrderLookupFailure(t *testing.T) {
	setupWeChatPayControllerTestDB(t)
	config := validControllerWeChatPayConfig()
	provider := &fakeControllerWeChatPayProvider{notifyResult: service.WeChatPayNotifyResult{
		EventID:   "event-database-failure",
		EventType: "TRANSACTION.SUCCESS",
		Transaction: service.WeChatPayTransaction{
			OutTradeNo: "WXN9123456789abcdef0123456789abd",
		},
	}}
	setWeChatPayControllerDependencies(t, provider, config, nil)
	require.NoError(t, model.DB.Migrator().DropTable(&model.TopUp{}))

	recorder := performWeChatPayNotify(t)
	assert.Equal(t, http.StatusServiceUnavailable, recorder.Code)
	var incidentCount int64
	require.NoError(t, model.DB.Model(&model.Log{}).Count(&incidentCount).Error)
	assert.Zero(t, incidentCount)
}

func TestWeChatPayNotifyRecordsProviderTransactionConflict(t *testing.T) {
	setupWeChatPayControllerTestDB(t)
	firstUser := &model.User{Id: 880, Username: "wechat-conflict-1", AffCode: "wechat-conflict-aff-1", Status: common.UserStatusEnabled}
	secondUser := &model.User{Id: 881, Username: "wechat-conflict-2", AffCode: "wechat-conflict-aff-2", Status: common.UserStatusEnabled}
	require.NoError(t, model.DB.Create(firstUser).Error)
	require.NoError(t, model.DB.Create(secondUser).Error)
	first := createControllerWeChatTopUp(t, firstUser.Id, "WXN6123456789abcdef0123456789abd")
	second := createControllerWeChatTopUp(t, secondUser.Id, "WXN7123456789abcdef0123456789abd")
	providerTradeNo := "4200000000202608310000000088"
	_, _, _, err := model.SettleWeChatTopUp(model.WeChatTopUpSettlement{
		TradeNo:         first.TradeNo,
		ProviderTradeNo: providerTradeNo,
		AmountMinor:     first.PaidAmountMinor,
		Currency:        first.Currency,
	})
	require.NoError(t, err)

	config := validControllerWeChatPayConfig()
	result := successfulWeChatNotify(config, second)
	result.Transaction.TransactionID = providerTradeNo
	provider := &fakeControllerWeChatPayProvider{notifyResult: result}
	setWeChatPayControllerDependencies(t, provider, config, nil)

	recorder := performWeChatPayNotify(t)
	assert.Equal(t, http.StatusNoContent, recorder.Code)
	var secondReloaded model.User
	require.NoError(t, model.DB.First(&secondReloaded, secondUser.Id).Error)
	assert.Zero(t, secondReloaded.Quota)
	var incidentCount int64
	require.NoError(t, model.DB.Model(&model.Log{}).Where("content = ?", "WeChat Pay callback requires reconciliation").Count(&incidentCount).Error)
	assert.Equal(t, int64(1), incidentCount)
}
