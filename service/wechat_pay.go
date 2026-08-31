package service

import (
	"context"
	"crypto/rsa"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/system_setting"

	"github.com/shopspring/decimal"
	"github.com/wechatpay-apiv3/wechatpay-go/core"
	"github.com/wechatpay-apiv3/wechatpay-go/core/auth"
	"github.com/wechatpay-apiv3/wechatpay-go/core/auth/validators"
	"github.com/wechatpay-apiv3/wechatpay-go/core/auth/verifiers"
	"github.com/wechatpay-apiv3/wechatpay-go/core/consts"
	"github.com/wechatpay-apiv3/wechatpay-go/core/notify"
	"github.com/wechatpay-apiv3/wechatpay-go/core/option"
	"github.com/wechatpay-apiv3/wechatpay-go/services/payments"
	"github.com/wechatpay-apiv3/wechatpay-go/services/payments/native"
	wechatpayutils "github.com/wechatpay-apiv3/wechatpay-go/utils"
)

const (
	defaultWeChatPayMinAmountCNY = "1"
	defaultWeChatPayMaxAmountCNY = "5000"
	defaultWeChatPayDescription  = "LLMRouter wallet top-up"
	wechatPayNotifyPath          = "/api/wechat-pay/notify"
)

var (
	wechatPayAmountPattern      = regexp.MustCompile(`^(0|[1-9][0-9]*)(\.[0-9]{1,2})?$`)
	wechatPayMerchantIDPattern  = regexp.MustCompile(`^[0-9]{8,32}$`)
	wechatPayAppIDPattern       = regexp.MustCompile(`^wx[0-9A-Za-z]{16}$`)
	wechatPayCertSerialPattern  = regexp.MustCompile(`^[0-9A-Fa-f]{16,64}$`)
	wechatPayPublicKeyIDPattern = regexp.MustCompile(
		`^PUB_KEY_ID_[0-9]+$`,
	)
)

type WeChatPayConfig struct {
	Enabled                bool
	MchID                  string
	AppID                  string
	MerchantCertSerial     string
	MerchantPrivateKeyPath string
	APIV3KeyPath           string
	PublicKeyID            string
	PublicKeyPath          string
	NextPublicKeyID        string
	NextPublicKeyPath      string
	NotifyURL              string
	MinAmountMinor         int64
	MaxAmountMinor         int64
	Description            string
}

type WeChatPayCreateRequest struct {
	OutTradeNo  string
	AmountMinor int64
	ExpiresAt   time.Time
}

type WeChatPayCreateResult struct {
	CodeURL   string
	RequestID string
}

type WeChatPayTransaction struct {
	AppID         string
	MchID         string
	OutTradeNo    string
	TransactionID string
	TradeType     string
	TradeState    string
	Currency      string
	SuccessTime   string
	AmountMinor   int64
}

type WeChatPayQueryResult struct {
	Transaction WeChatPayTransaction
	RequestID   string
}

type WeChatPayNotifyResult struct {
	EventID     string
	EventType   string
	Transaction WeChatPayTransaction
}

type WeChatPayCloseResult struct {
	RequestID string
}

type WeChatPayProvider interface {
	CreateNative(ctx context.Context, request WeChatPayCreateRequest) (WeChatPayCreateResult, error)
	QueryOrder(ctx context.Context, outTradeNo string) (WeChatPayQueryResult, error)
	CloseOrder(ctx context.Context, outTradeNo string) (WeChatPayCloseResult, error)
	ParseNotify(ctx context.Context, request *http.Request) (WeChatPayNotifyResult, error)
}

type weChatPayNativeAPI interface {
	Prepay(ctx context.Context, request native.PrepayRequest) (*native.PrepayResponse, *core.APIResult, error)
	QueryOrderByOutTradeNo(ctx context.Context, request native.QueryOrderByOutTradeNoRequest) (*payments.Transaction, *core.APIResult, error)
	CloseOrder(ctx context.Context, request native.CloseOrderRequest) (*core.APIResult, error)
}

type weChatPayNotifyParser interface {
	ParseNotifyRequest(ctx context.Context, request *http.Request, content interface{}) (*notify.Request, error)
}

type sdkWeChatPayProvider struct {
	config       WeChatPayConfig
	api          weChatPayNativeAPI
	notifyParser weChatPayNotifyParser
}

type weChatPayPublicKeyVerifier struct {
	currentID string
	verifiers map[string]auth.Verifier
}

type weChatPayVerifierOption struct {
	verifier auth.Verifier
}

func (option weChatPayVerifierOption) Apply(settings *core.DialSettings) error {
	settings.Validator = validators.NewWechatPayResponseValidator(option.verifier)
	return nil
}

func newWeChatPayPublicKeyVerifier(
	currentID string,
	currentKey *rsa.PublicKey,
	nextID string,
	nextKey *rsa.PublicKey,
) auth.Verifier {
	keyVerifiers := map[string]auth.Verifier{
		currentID: verifiers.NewSHA256WithRSAPubkeyVerifier(currentID, *currentKey),
	}
	if nextID != "" && nextKey != nil {
		keyVerifiers[nextID] = verifiers.NewSHA256WithRSAPubkeyVerifier(nextID, *nextKey)
	}
	return &weChatPayPublicKeyVerifier{
		currentID: currentID,
		verifiers: keyVerifiers,
	}
}

func (verifier *weChatPayPublicKeyVerifier) Verify(
	ctx context.Context,
	serial string,
	message string,
	signature string,
) error {
	keyVerifier, ok := verifier.verifiers[serial]
	if !ok {
		return fmt.Errorf("unrecognized WeChat Pay public key ID: %s", serial)
	}
	return keyVerifier.Verify(ctx, serial, message, signature)
}

func (verifier *weChatPayPublicKeyVerifier) GetSerial(_ context.Context) (string, error) {
	return verifier.currentID, nil
}

type WeChatPayError struct {
	Operation  string
	Code       string
	RequestID  string
	StatusCode int
	Ambiguous  bool
	cause      error
}

func ValidateWeChatPaySuccessfulTransaction(
	config WeChatPayConfig,
	transaction WeChatPayTransaction,
	expectedTradeNo string,
	expectedAmountMinor int64,
) error {
	if transaction.TradeState != "SUCCESS" {
		return errors.New("unexpected WeChat Pay trade state")
	}
	if err := ValidateWeChatPayTransactionIdentity(config, transaction, expectedTradeNo, expectedAmountMinor); err != nil {
		return err
	}
	if transaction.TransactionID == "" {
		return errors.New("missing WeChat Pay transaction ID")
	}
	return nil
}

func ValidateWeChatPayTransactionIdentity(
	config WeChatPayConfig,
	transaction WeChatPayTransaction,
	expectedTradeNo string,
	expectedAmountMinor int64,
) error {
	if transaction.TradeType != "NATIVE" {
		return errors.New("unexpected WeChat Pay trade type")
	}
	if transaction.MchID != config.MchID {
		return errors.New("WeChat Pay merchant mismatch")
	}
	if transaction.AppID != "" && transaction.AppID != config.AppID {
		return errors.New("WeChat Pay AppID mismatch")
	}
	if transaction.OutTradeNo != expectedTradeNo {
		return errors.New("WeChat Pay merchant order mismatch")
	}
	if transaction.Currency != "CNY" {
		return errors.New("WeChat Pay currency mismatch")
	}
	if transaction.AmountMinor != expectedAmountMinor {
		return errors.New("WeChat Pay amount mismatch")
	}
	return nil
}

func (e *WeChatPayError) Error() string {
	if e.Code != "" {
		return fmt.Sprintf("WeChat Pay %s failed: %s", e.Operation, e.Code)
	}
	return fmt.Sprintf("WeChat Pay %s failed", e.Operation)
}

func (e *WeChatPayError) Unwrap() error {
	return e.cause
}

func IsAmbiguousWeChatPayError(err error) bool {
	var payErr *WeChatPayError
	return errors.As(err, &payErr) && payErr.Ambiguous
}

func newSDKWeChatPayProvider(
	config WeChatPayConfig,
	api weChatPayNativeAPI,
	notifyParser weChatPayNotifyParser,
) WeChatPayProvider {
	return &sdkWeChatPayProvider{
		config:       config,
		api:          api,
		notifyParser: notifyParser,
	}
}

func NewConfiguredWeChatPayProvider(ctx context.Context) (WeChatPayProvider, WeChatPayConfig, error) {
	return newConfiguredWeChatPayProvider(ctx, true)
}

func NewConfiguredWeChatPayCallbackProvider(ctx context.Context) (WeChatPayProvider, WeChatPayConfig, error) {
	return newConfiguredWeChatPayProvider(ctx, false)
}

func newConfiguredWeChatPayProvider(ctx context.Context, requireEnabled bool) (WeChatPayProvider, WeChatPayConfig, error) {
	config, err := LoadWeChatPayConfig()
	if err != nil {
		return nil, WeChatPayConfig{}, err
	}
	if requireEnabled {
		err = config.Ready()
	} else {
		err = config.CredentialsReady()
	}
	if err != nil {
		return nil, WeChatPayConfig{}, err
	}

	privateKey, err := wechatpayutils.LoadPrivateKeyWithPath(config.MerchantPrivateKeyPath)
	if err != nil {
		return nil, WeChatPayConfig{}, fmt.Errorf("load merchant private key: %w", err)
	}
	publicKey, err := wechatpayutils.LoadPublicKeyWithPath(config.PublicKeyPath)
	if err != nil {
		return nil, WeChatPayConfig{}, fmt.Errorf("load WeChat Pay public key: %w", err)
	}
	apiV3Key, err := os.ReadFile(config.APIV3KeyPath)
	if err != nil {
		return nil, WeChatPayConfig{}, fmt.Errorf("read APIv3 key: %w", err)
	}

	var nextPublicKey *rsa.PublicKey
	if config.NextPublicKeyPath != "" {
		nextPublicKey, err = wechatpayutils.LoadPublicKeyWithPath(config.NextPublicKeyPath)
		if err != nil {
			return nil, WeChatPayConfig{}, fmt.Errorf("load next WeChat Pay public key: %w", err)
		}
	}
	publicKeyVerifier := newWeChatPayPublicKeyVerifier(
		config.PublicKeyID,
		publicKey,
		config.NextPublicKeyID,
		nextPublicKey,
	)

	client, err := core.NewClient(
		ctx,
		option.WithWechatPayPublicKeyAuthCipher(
			config.MchID,
			config.MerchantCertSerial,
			privateKey,
			config.PublicKeyID,
			publicKey,
		),
		weChatPayVerifierOption{verifier: publicKeyVerifier},
	)
	if err != nil {
		return nil, WeChatPayConfig{}, fmt.Errorf("create WeChat Pay client: %w", err)
	}
	notifyParser, err := notify.NewRSANotifyHandler(
		strings.TrimSpace(string(apiV3Key)),
		publicKeyVerifier,
	)
	if err != nil {
		return nil, WeChatPayConfig{}, fmt.Errorf("create WeChat Pay notify handler: %w", err)
	}

	provider := newSDKWeChatPayProvider(
		config,
		&native.NativeApiService{Client: client},
		notifyParser,
	)
	return provider, config, nil
}

func (provider *sdkWeChatPayProvider) CreateNative(
	ctx context.Context,
	request WeChatPayCreateRequest,
) (WeChatPayCreateResult, error) {
	response, result, err := provider.api.Prepay(ctx, native.PrepayRequest{
		Appid:       core.String(provider.config.AppID),
		Mchid:       core.String(provider.config.MchID),
		Description: core.String(provider.config.Description),
		OutTradeNo:  core.String(request.OutTradeNo),
		TimeExpire:  core.Time(request.ExpiresAt),
		NotifyUrl:   core.String(provider.config.NotifyURL),
		Amount: &native.Amount{
			Total:    core.Int64(request.AmountMinor),
			Currency: core.String("CNY"),
		},
	})
	if err != nil {
		return WeChatPayCreateResult{}, wrapWeChatPayError("create native order", result, err, classifyWeChatPayCreateAmbiguity(err))
	}
	if response == nil || response.CodeUrl == nil || strings.TrimSpace(*response.CodeUrl) == "" {
		return WeChatPayCreateResult{}, wrapWeChatPayError("create native order", result, errors.New("missing code URL"), true)
	}
	return WeChatPayCreateResult{
		CodeURL:   *response.CodeUrl,
		RequestID: weChatPayRequestID(result),
	}, nil
}

func (provider *sdkWeChatPayProvider) QueryOrder(
	ctx context.Context,
	outTradeNo string,
) (WeChatPayQueryResult, error) {
	transaction, result, err := provider.api.QueryOrderByOutTradeNo(ctx, native.QueryOrderByOutTradeNoRequest{
		Mchid:      core.String(provider.config.MchID),
		OutTradeNo: core.String(outTradeNo),
	})
	if err != nil {
		return WeChatPayQueryResult{}, wrapWeChatPayError("query order", result, err, true)
	}
	if transaction == nil {
		return WeChatPayQueryResult{}, wrapWeChatPayError("query order", result, errors.New("missing transaction"), true)
	}
	return WeChatPayQueryResult{
		Transaction: normalizeWeChatPayTransaction(transaction),
		RequestID:   weChatPayRequestID(result),
	}, nil
}

func (provider *sdkWeChatPayProvider) CloseOrder(
	ctx context.Context,
	outTradeNo string,
) (WeChatPayCloseResult, error) {
	result, err := provider.api.CloseOrder(ctx, native.CloseOrderRequest{
		Mchid:      core.String(provider.config.MchID),
		OutTradeNo: core.String(outTradeNo),
	})
	if err != nil {
		return WeChatPayCloseResult{}, wrapWeChatPayError("close order", result, err, true)
	}
	return WeChatPayCloseResult{RequestID: weChatPayRequestID(result)}, nil
}

func (provider *sdkWeChatPayProvider) ParseNotify(
	ctx context.Context,
	request *http.Request,
) (WeChatPayNotifyResult, error) {
	transaction := &payments.Transaction{}
	event, err := provider.notifyParser.ParseNotifyRequest(ctx, request, transaction)
	if err != nil {
		return WeChatPayNotifyResult{}, wrapWeChatPayError("parse notification", nil, err, false)
	}
	if event == nil {
		return WeChatPayNotifyResult{}, wrapWeChatPayError("parse notification", nil, errors.New("missing notification event"), false)
	}
	return WeChatPayNotifyResult{
		EventID:     event.ID,
		EventType:   event.EventType,
		Transaction: normalizeWeChatPayTransaction(transaction),
	}, nil
}

func normalizeWeChatPayTransaction(transaction *payments.Transaction) WeChatPayTransaction {
	normalized := WeChatPayTransaction{}
	if transaction.Appid != nil {
		normalized.AppID = *transaction.Appid
	}
	if transaction.Mchid != nil {
		normalized.MchID = *transaction.Mchid
	}
	if transaction.OutTradeNo != nil {
		normalized.OutTradeNo = *transaction.OutTradeNo
	}
	if transaction.TransactionId != nil {
		normalized.TransactionID = *transaction.TransactionId
	}
	if transaction.TradeType != nil {
		normalized.TradeType = *transaction.TradeType
	}
	if transaction.TradeState != nil {
		normalized.TradeState = *transaction.TradeState
	}
	if transaction.SuccessTime != nil {
		normalized.SuccessTime = *transaction.SuccessTime
	}
	if transaction.Amount != nil {
		if transaction.Amount.Total != nil {
			normalized.AmountMinor = *transaction.Amount.Total
		}
		if transaction.Amount.Currency != nil {
			normalized.Currency = *transaction.Amount.Currency
		}
	}
	return normalized
}

func wrapWeChatPayError(operation string, result *core.APIResult, err error, ambiguous bool) error {
	payErr := &WeChatPayError{
		Operation: operation,
		RequestID: weChatPayRequestID(result),
		Ambiguous: ambiguous,
		cause:     err,
	}
	var apiErr *core.APIError
	if errors.As(err, &apiErr) {
		payErr.Code = apiErr.Code
		payErr.StatusCode = apiErr.StatusCode
		if payErr.RequestID == "" && apiErr.Header != nil {
			payErr.RequestID = apiErr.Header.Get(consts.RequestID)
		}
	}
	return payErr
}

func classifyWeChatPayCreateAmbiguity(err error) bool {
	var apiErr *core.APIError
	if !errors.As(err, &apiErr) {
		return true
	}
	switch apiErr.Code {
	case "PARAM_ERROR", "APPID_MCHID_NOT_MATCH", "NO_AUTH", "MCH_NOT_EXISTS", "INVALID_REQUEST":
		return false
	default:
		return true
	}
}

func weChatPayRequestID(result *core.APIResult) string {
	if result == nil || result.Response == nil {
		return ""
	}
	return result.Response.Header.Get(consts.RequestID)
}

func ParseWeChatCNYAmount(input string) (int64, error) {
	if !wechatPayAmountPattern.MatchString(input) {
		return 0, errors.New("invalid CNY amount")
	}
	amount, err := decimal.NewFromString(input)
	if err != nil || !amount.IsPositive() {
		return 0, errors.New("invalid CNY amount")
	}
	minor := amount.Shift(2)
	if minor.GreaterThan(decimal.NewFromInt(math.MaxInt64)) {
		return 0, errors.New("CNY amount exceeds int64")
	}
	return minor.IntPart(), nil
}

func CalculateWeChatCreditedQuota(
	amountMinor int64,
	exchangeRate decimal.Decimal,
	quotaPerUnit decimal.Decimal,
) (int, error) {
	if amountMinor <= 0 || !exchangeRate.IsPositive() || !quotaPerUnit.IsPositive() {
		return 0, errors.New("invalid WeChat top-up conversion input")
	}
	quotaDecimal := decimal.NewFromInt(amountMinor).
		Div(decimal.NewFromInt(100)).
		Div(exchangeRate).
		Mul(quotaPerUnit)
	quota, err := common.QuotaFromDecimalStrict(quotaDecimal)
	if err != nil || quota <= 0 {
		if err != nil {
			return 0, err
		}
		return 0, errors.New("invalid credited quota")
	}
	return quota, nil
}

func LoadWeChatPayConfig() (WeChatPayConfig, error) {
	config := WeChatPayConfig{
		Enabled:                common.GetEnvOrDefaultBool("WECHATPAY_ENABLED", false),
		MchID:                  strings.TrimSpace(os.Getenv("WECHATPAY_MCH_ID")),
		AppID:                  strings.TrimSpace(os.Getenv("WECHATPAY_APP_ID")),
		MerchantCertSerial:     strings.TrimSpace(os.Getenv("WECHATPAY_MCH_CERT_SERIAL")),
		MerchantPrivateKeyPath: strings.TrimSpace(os.Getenv("WECHATPAY_MCH_PRIVATE_KEY_PATH")),
		APIV3KeyPath:           strings.TrimSpace(os.Getenv("WECHATPAY_API_V3_KEY_PATH")),
		PublicKeyID:            strings.TrimSpace(os.Getenv("WECHATPAY_PUBLIC_KEY_ID")),
		PublicKeyPath:          strings.TrimSpace(os.Getenv("WECHATPAY_PUBLIC_KEY_PATH")),
		NextPublicKeyID:        strings.TrimSpace(os.Getenv("WECHATPAY_NEXT_PUBLIC_KEY_ID")),
		NextPublicKeyPath:      strings.TrimSpace(os.Getenv("WECHATPAY_NEXT_PUBLIC_KEY_PATH")),
		Description:            strings.TrimSpace(os.Getenv("WECHATPAY_DESCRIPTION")),
	}
	if config.Description == "" {
		config.Description = defaultWeChatPayDescription
	}

	minAmount := strings.TrimSpace(os.Getenv("WECHATPAY_MIN_TOPUP_CNY"))
	if minAmount == "" {
		minAmount = defaultWeChatPayMinAmountCNY
	}
	maxAmount := strings.TrimSpace(os.Getenv("WECHATPAY_MAX_TOPUP_CNY"))
	if maxAmount == "" {
		maxAmount = defaultWeChatPayMaxAmountCNY
	}
	var err error
	config.MinAmountMinor, err = ParseWeChatCNYAmount(minAmount)
	if err != nil {
		return WeChatPayConfig{}, fmt.Errorf("invalid WECHATPAY_MIN_TOPUP_CNY: %w", err)
	}
	config.MaxAmountMinor, err = ParseWeChatCNYAmount(maxAmount)
	if err != nil {
		return WeChatPayConfig{}, fmt.Errorf("invalid WECHATPAY_MAX_TOPUP_CNY: %w", err)
	}

	baseURL := strings.TrimRight(strings.TrimSpace(system_setting.ServerAddress), "/")
	config.NotifyURL = baseURL + wechatPayNotifyPath
	return config, nil
}

func (config WeChatPayConfig) Ready() error {
	if !config.Enabled {
		return errors.New("WeChat Pay is disabled")
	}
	return config.CredentialsReady()
}

func (config WeChatPayConfig) CredentialsReady() error {
	if !wechatPayMerchantIDPattern.MatchString(config.MchID) {
		return errors.New("invalid WeChat Pay merchant ID")
	}
	if !wechatPayAppIDPattern.MatchString(config.AppID) {
		return errors.New("invalid WeChat Pay AppID")
	}
	if !wechatPayCertSerialPattern.MatchString(config.MerchantCertSerial) {
		return errors.New("invalid WeChat Pay merchant certificate serial")
	}
	if !wechatPayPublicKeyIDPattern.MatchString(config.PublicKeyID) {
		return errors.New("invalid WeChat Pay public key ID")
	}
	if config.MinAmountMinor <= 0 || config.MaxAmountMinor < config.MinAmountMinor {
		return errors.New("invalid WeChat Pay top-up limits")
	}
	if strings.TrimSpace(config.Description) == "" || len(config.Description) > 127 {
		return errors.New("invalid WeChat Pay description")
	}

	notifyURL, err := url.Parse(config.NotifyURL)
	if err != nil || notifyURL.Scheme != "https" || notifyURL.Host == "" || notifyURL.RawQuery != "" || notifyURL.Fragment != "" || notifyURL.Path != wechatPayNotifyPath {
		return errors.New("invalid WeChat Pay notify URL")
	}

	if err := validateRestrictedFile(config.MerchantPrivateKeyPath); err != nil {
		return fmt.Errorf("invalid merchant private key file: %w", err)
	}
	if _, err := wechatpayutils.LoadPrivateKeyWithPath(config.MerchantPrivateKeyPath); err != nil {
		return fmt.Errorf("invalid merchant private key: %w", err)
	}
	if err := validateRestrictedFile(config.APIV3KeyPath); err != nil {
		return fmt.Errorf("invalid APIv3 key file: %w", err)
	}
	apiV3Key, err := os.ReadFile(config.APIV3KeyPath)
	if err != nil {
		return fmt.Errorf("read APIv3 key: %w", err)
	}
	if len(strings.TrimSpace(string(apiV3Key))) != 32 {
		return errors.New("APIv3 key must contain exactly 32 bytes")
	}
	if _, err := wechatpayutils.LoadPublicKeyWithPath(config.PublicKeyPath); err != nil {
		return fmt.Errorf("invalid WeChat Pay public key: %w", err)
	}
	hasNextPublicKeyID := config.NextPublicKeyID != ""
	hasNextPublicKeyPath := config.NextPublicKeyPath != ""
	if hasNextPublicKeyID != hasNextPublicKeyPath {
		return errors.New("next WeChat Pay public key ID and path must be configured together")
	}
	if hasNextPublicKeyID {
		if !wechatPayPublicKeyIDPattern.MatchString(config.NextPublicKeyID) || config.NextPublicKeyID == config.PublicKeyID {
			return errors.New("invalid next WeChat Pay public key ID")
		}
		if _, err := wechatpayutils.LoadPublicKeyWithPath(config.NextPublicKeyPath); err != nil {
			return fmt.Errorf("invalid next WeChat Pay public key: %w", err)
		}
	}
	return nil
}

func validateRestrictedFile(path string) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("path is empty")
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("path is not a regular file")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return errors.New("file must not be readable or writable by group or others")
	}
	return nil
}

func formatWeChatPayAmountMinor(amountMinor int64) string {
	return strconv.FormatInt(amountMinor/100, 10) + "." + fmt.Sprintf("%02d", amountMinor%100)
}
