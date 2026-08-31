package service

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wechatpay-apiv3/wechatpay-go/core"
	"github.com/wechatpay-apiv3/wechatpay-go/core/consts"
	"github.com/wechatpay-apiv3/wechatpay-go/core/notify"
	"github.com/wechatpay-apiv3/wechatpay-go/services/payments"
	"github.com/wechatpay-apiv3/wechatpay-go/services/payments/native"
)

func TestParseWeChatCNYAmount(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  int64
	}{
		{name: "whole yuan", input: "50", want: 5000},
		{name: "one decimal place", input: "50.1", want: 5010},
		{name: "two decimal places", input: "50.01", want: 5001},
		{name: "leading zero", input: "0.01", want: 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseWeChatCNYAmount(tt.input)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestParseWeChatCNYAmountRejectsInvalidForms(t *testing.T) {
	inputs := []string{
		"",
		" ",
		"0",
		"0.00",
		"-1",
		"+1",
		"1.",
		".5",
		"1.001",
		"1e2",
		"NaN",
		"92233720368547758.08",
	}

	for _, input := range inputs {
		t.Run(input, func(t *testing.T) {
			_, err := ParseWeChatCNYAmount(input)
			require.Error(t, err)
		})
	}
}

func TestCalculateWeChatCreditedQuotaUsesOrderSnapshotInputs(t *testing.T) {
	quota, err := CalculateWeChatCreditedQuota(
		5000,
		decimal.RequireFromString("7.3"),
		decimal.NewFromInt(500000),
	)
	require.NoError(t, err)
	assert.Equal(t, 3424658, quota)
}

func TestCalculateWeChatCreditedQuotaRejectsInvalidOrOverflowingInputs(t *testing.T) {
	tests := []struct {
		name         string
		amountMinor  int64
		exchangeRate decimal.Decimal
		quotaPerUnit decimal.Decimal
	}{
		{name: "zero amount", amountMinor: 0, exchangeRate: decimal.NewFromInt(7), quotaPerUnit: decimal.NewFromInt(500000)},
		{name: "zero exchange rate", amountMinor: 1, exchangeRate: decimal.Zero, quotaPerUnit: decimal.NewFromInt(500000)},
		{name: "negative exchange rate", amountMinor: 1, exchangeRate: decimal.NewFromInt(-7), quotaPerUnit: decimal.NewFromInt(500000)},
		{name: "zero quota unit", amountMinor: 1, exchangeRate: decimal.NewFromInt(7), quotaPerUnit: decimal.Zero},
		{name: "quota overflow", amountMinor: math.MaxInt64, exchangeRate: decimal.NewFromInt(1), quotaPerUnit: decimal.NewFromInt(math.MaxInt32)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := CalculateWeChatCreditedQuota(tt.amountMinor, tt.exchangeRate, tt.quotaPerUnit)
			require.Error(t, err)
		})
	}
}

func writeWeChatPayTestKeys(t *testing.T, dir string) (string, string) {
	t.Helper()
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	privateDER, err := x509.MarshalPKCS8PrivateKey(privateKey)
	require.NoError(t, err)
	privatePath := filepath.Join(dir, "apiclient_key.pem")
	require.NoError(t, os.WriteFile(privatePath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateDER}), 0o600))

	publicDER, err := x509.MarshalPKIXPublicKey(&privateKey.PublicKey)
	require.NoError(t, err)
	publicPath := filepath.Join(dir, "wechatpay_public_key.pem")
	require.NoError(t, os.WriteFile(publicPath, pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: publicDER}), 0o600))
	return privatePath, publicPath
}

func validWeChatPayConfigForTest(t *testing.T) WeChatPayConfig {
	t.Helper()
	dir := t.TempDir()
	privatePath, publicPath := writeWeChatPayTestKeys(t, dir)
	apiV3KeyPath := filepath.Join(dir, "apiv3-key")
	require.NoError(t, os.WriteFile(apiV3KeyPath, []byte("12345678901234567890123456789012"), 0o600))

	return WeChatPayConfig{
		Enabled:                true,
		MchID:                  "1295594101",
		AppID:                  "wxa0b1bc308cdea2b2",
		MerchantCertSerial:     "1DDE55AD98E1234567890ABCDEF1234567890ABC",
		MerchantPrivateKeyPath: privatePath,
		APIV3KeyPath:           apiV3KeyPath,
		PublicKeyID:            "PUB_KEY_ID_0000000000000000000000000000000001",
		PublicKeyPath:          publicPath,
		NotifyURL:              "https://llmrouter.quukk.com/api/wechat-pay/notify",
		MinAmountMinor:         100,
		MaxAmountMinor:         500000,
		Description:            "LLMRouter wallet top-up",
	}
}

func TestWeChatPayConfigReadyAcceptsCompleteConfig(t *testing.T) {
	config := validWeChatPayConfigForTest(t)
	require.NoError(t, config.Ready())
}

func TestWeChatPayConfigCredentialsReadyAllowsCallbacksWhileNewOrdersDisabled(t *testing.T) {
	config := validWeChatPayConfigForTest(t)
	config.Enabled = false
	require.Error(t, config.Ready())
	require.NoError(t, config.CredentialsReady())
}

func TestWeChatPayConfigReadyAcceptsOptionalNextPublicKey(t *testing.T) {
	config := validWeChatPayConfigForTest(t)
	_, nextPublicKeyPath := writeWeChatPayTestKeys(t, t.TempDir())
	config.NextPublicKeyID = "PUB_KEY_ID_0000000000000000000000000000000002"
	config.NextPublicKeyPath = nextPublicKeyPath
	require.NoError(t, config.Ready())

	config.NextPublicKeyPath = ""
	require.Error(t, config.Ready())
}

func TestWeChatPayPublicKeyVerifierAcceptsRotationOverlap(t *testing.T) {
	currentKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	nextKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	const currentID = "PUB_KEY_ID_0000000000000000000000000000000001"
	const nextID = "PUB_KEY_ID_0000000000000000000000000000000002"
	verifier := newWeChatPayPublicKeyVerifier(
		currentID,
		&currentKey.PublicKey,
		nextID,
		&nextKey.PublicKey,
	)

	message := "rotation-overlap-message"
	for keyID, key := range map[string]*rsa.PrivateKey{
		currentID: currentKey,
		nextID:    nextKey,
	} {
		digest := sha256.Sum256([]byte(message))
		signature, signErr := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
		require.NoError(t, signErr)
		require.NoError(t, verifier.Verify(context.Background(), keyID, message, base64.StdEncoding.EncodeToString(signature)))
	}

	serial, err := verifier.GetSerial(context.Background())
	require.NoError(t, err)
	assert.Equal(t, currentID, serial)
	require.Error(t, verifier.Verify(context.Background(), "PUB_KEY_ID_UNKNOWN", message, "invalid"))
}

func TestWeChatPayConfigReadyRejectsUnsafeOrIncompleteConfig(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(t *testing.T, config *WeChatPayConfig)
	}{
		{name: "disabled", mutate: func(_ *testing.T, config *WeChatPayConfig) { config.Enabled = false }},
		{name: "missing merchant id", mutate: func(_ *testing.T, config *WeChatPayConfig) { config.MchID = "" }},
		{name: "missing app id", mutate: func(_ *testing.T, config *WeChatPayConfig) { config.AppID = "" }},
		{name: "invalid public key id", mutate: func(_ *testing.T, config *WeChatPayConfig) { config.PublicKeyID = "123" }},
		{name: "http notify url", mutate: func(_ *testing.T, config *WeChatPayConfig) {
			config.NotifyURL = "http://llmrouter.quukk.com/api/wechat-pay/notify"
		}},
		{name: "notify url query", mutate: func(_ *testing.T, config *WeChatPayConfig) { config.NotifyURL += "?token=secret" }},
		{name: "missing private key", mutate: func(_ *testing.T, config *WeChatPayConfig) {
			config.MerchantPrivateKeyPath = filepath.Join(t.TempDir(), "missing")
		}},
		{name: "missing public key", mutate: func(_ *testing.T, config *WeChatPayConfig) {
			config.PublicKeyPath = filepath.Join(t.TempDir(), "missing")
		}},
		{name: "invalid api v3 key length", mutate: func(t *testing.T, config *WeChatPayConfig) {
			path := filepath.Join(t.TempDir(), "short-key")
			require.NoError(t, os.WriteFile(path, []byte("too-short"), 0o600))
			config.APIV3KeyPath = path
		}},
		{name: "inverted limits", mutate: func(_ *testing.T, config *WeChatPayConfig) { config.MinAmountMinor = config.MaxAmountMinor + 1 }},
		{name: "empty description", mutate: func(_ *testing.T, config *WeChatPayConfig) { config.Description = "" }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := validWeChatPayConfigForTest(t)
			tt.mutate(t, &config)
			require.Error(t, config.Ready())
		})
	}
}

type fakeWeChatPayNativeAPI struct {
	prepayRequest  native.PrepayRequest
	queryRequest   native.QueryOrderByOutTradeNoRequest
	closeRequest   native.CloseOrderRequest
	prepayResponse *native.PrepayResponse
	queryResponse  *payments.Transaction
	prepayResult   *core.APIResult
	queryResult    *core.APIResult
	closeResult    *core.APIResult
	prepayErr      error
	queryErr       error
	closeErr       error
}

func (f *fakeWeChatPayNativeAPI) Prepay(_ context.Context, req native.PrepayRequest) (*native.PrepayResponse, *core.APIResult, error) {
	f.prepayRequest = req
	return f.prepayResponse, f.prepayResult, f.prepayErr
}

func (f *fakeWeChatPayNativeAPI) QueryOrderByOutTradeNo(_ context.Context, req native.QueryOrderByOutTradeNoRequest) (*payments.Transaction, *core.APIResult, error) {
	f.queryRequest = req
	return f.queryResponse, f.queryResult, f.queryErr
}

func (f *fakeWeChatPayNativeAPI) CloseOrder(_ context.Context, req native.CloseOrderRequest) (*core.APIResult, error) {
	f.closeRequest = req
	return f.closeResult, f.closeErr
}

type fakeWeChatPayNotifyParser struct {
	event       *notify.Request
	transaction payments.Transaction
	err         error
}

func (f *fakeWeChatPayNotifyParser) ParseNotifyRequest(_ context.Context, _ *http.Request, content interface{}) (*notify.Request, error) {
	if f.err != nil {
		return nil, f.err
	}
	transaction, ok := content.(*payments.Transaction)
	if !ok {
		return nil, errors.New("unexpected callback content type")
	}
	*transaction = f.transaction
	return f.event, nil
}

func weChatPayTestAPIResult(requestID string) *core.APIResult {
	response := &http.Response{Header: make(http.Header)}
	response.Header.Set(consts.RequestID, requestID)
	return &core.APIResult{Response: response}
}

func TestWeChatPayProviderMapsNativeCreateRequestAndResponse(t *testing.T) {
	config := validWeChatPayConfigForTest(t)
	codeURL := "weixin://wxpay/bizpayurl?pr=test"
	api := &fakeWeChatPayNativeAPI{
		prepayResponse: &native.PrepayResponse{CodeUrl: core.String(codeURL)},
		prepayResult:   weChatPayTestAPIResult("request-create-1"),
	}
	provider := newSDKWeChatPayProvider(config, api, &fakeWeChatPayNotifyParser{})
	expiresAt := time.Date(2026, 8, 31, 22, 15, 0, 0, time.FixedZone("CST", 8*60*60))

	result, err := provider.CreateNative(context.Background(), WeChatPayCreateRequest{
		OutTradeNo:  "WXN0123456789abcdef0123456789abc",
		AmountMinor: 5001,
		ExpiresAt:   expiresAt,
	})
	require.NoError(t, err)
	assert.Equal(t, WeChatPayCreateResult{CodeURL: codeURL, RequestID: "request-create-1"}, result)
	require.NotNil(t, api.prepayRequest.Appid)
	assert.Equal(t, config.AppID, *api.prepayRequest.Appid)
	require.NotNil(t, api.prepayRequest.Mchid)
	assert.Equal(t, config.MchID, *api.prepayRequest.Mchid)
	require.NotNil(t, api.prepayRequest.Description)
	assert.Equal(t, config.Description, *api.prepayRequest.Description)
	require.NotNil(t, api.prepayRequest.NotifyUrl)
	assert.Equal(t, config.NotifyURL, *api.prepayRequest.NotifyUrl)
	require.NotNil(t, api.prepayRequest.TimeExpire)
	assert.Equal(t, expiresAt, *api.prepayRequest.TimeExpire)
	require.NotNil(t, api.prepayRequest.Amount)
	require.NotNil(t, api.prepayRequest.Amount.Total)
	assert.Equal(t, int64(5001), *api.prepayRequest.Amount.Total)
	require.NotNil(t, api.prepayRequest.Amount.Currency)
	assert.Equal(t, "CNY", *api.prepayRequest.Amount.Currency)
}

func TestWeChatPayProviderNormalizesQueryAndNotifyTransactions(t *testing.T) {
	config := validWeChatPayConfigForTest(t)
	transaction := payments.Transaction{
		Appid:         core.String(config.AppID),
		Mchid:         core.String(config.MchID),
		OutTradeNo:    core.String("WXN0123456789abcdef0123456789abc"),
		TransactionId: core.String("4200000000202608311234567890"),
		TradeType:     core.String("NATIVE"),
		TradeState:    core.String("SUCCESS"),
		SuccessTime:   core.String("2026-08-31T22:01:02+08:00"),
		Amount: &payments.TransactionAmount{
			Total:    core.Int64(5000),
			Currency: core.String("CNY"),
		},
	}
	api := &fakeWeChatPayNativeAPI{
		queryResponse: &transaction,
		queryResult:   weChatPayTestAPIResult("request-query-1"),
	}
	parser := &fakeWeChatPayNotifyParser{
		event:       &notify.Request{ID: "event-1", EventType: "TRANSACTION.SUCCESS"},
		transaction: transaction,
	}
	provider := newSDKWeChatPayProvider(config, api, parser)

	queryResult, err := provider.QueryOrder(context.Background(), *transaction.OutTradeNo)
	require.NoError(t, err)
	assert.Equal(t, "request-query-1", queryResult.RequestID)
	assert.Equal(t, "SUCCESS", queryResult.Transaction.TradeState)
	assert.Equal(t, int64(5000), queryResult.Transaction.AmountMinor)

	notifyResult, err := provider.ParseNotify(context.Background(), &http.Request{})
	require.NoError(t, err)
	assert.Equal(t, "event-1", notifyResult.EventID)
	assert.Equal(t, "TRANSACTION.SUCCESS", notifyResult.EventType)
	assert.Equal(t, queryResult.Transaction, notifyResult.Transaction)
}

func TestWeChatPayProviderClassifiesCreateFailures(t *testing.T) {
	config := validWeChatPayConfigForTest(t)
	tests := []struct {
		name      string
		err       error
		ambiguous bool
	}{
		{name: "invalid request", err: &core.APIError{StatusCode: http.StatusBadRequest, Code: "PARAM_ERROR"}, ambiguous: false},
		{name: "rate limited", err: &core.APIError{StatusCode: http.StatusTooManyRequests, Code: "FREQUENCY_LIMITED"}, ambiguous: true},
		{name: "provider failure", err: &core.APIError{StatusCode: http.StatusInternalServerError, Code: "SYSTEM_ERROR"}, ambiguous: true},
		{name: "network failure", err: errors.New("connection reset"), ambiguous: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			api := &fakeWeChatPayNativeAPI{prepayErr: tt.err}
			provider := newSDKWeChatPayProvider(config, api, &fakeWeChatPayNotifyParser{})
			_, err := provider.CreateNative(context.Background(), WeChatPayCreateRequest{
				OutTradeNo:  "WXN0123456789abcdef0123456789abc",
				AmountMinor: 5000,
				ExpiresAt:   time.Now().Add(15 * time.Minute),
			})
			require.Error(t, err)
			assert.Equal(t, tt.ambiguous, IsAmbiguousWeChatPayError(err))
		})
	}
}
