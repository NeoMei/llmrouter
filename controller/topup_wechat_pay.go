package controller

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/operation_setting"

	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"
)

var (
	weChatPayIdempotencyKeyPattern = regexp.MustCompile(`^[0-9A-Za-z_-]{16,64}$`)
	loadReadyWeChatPayConfig       = func() (service.WeChatPayConfig, error) {
		config, err := service.LoadWeChatPayConfig()
		if err != nil {
			return service.WeChatPayConfig{}, err
		}
		if err := config.Ready(); err != nil {
			return service.WeChatPayConfig{}, err
		}
		return config, nil
	}
	loadWeChatPayProvider         = service.NewConfiguredWeChatPayProvider
	loadWeChatPayCallbackProvider = service.NewConfiguredWeChatPayCallbackProvider
	recordWeChatPayIncident       = model.RecordWeChatPayIncident
	settleWeChatTopUp             = model.SettleWeChatTopUp
)

type weChatNativePayRequest struct {
	Amount         string `json:"amount"`
	IdempotencyKey string `json:"idempotency_key"`
}

func RequestWeChatNativePay(c *gin.Context) {
	if !operation_setting.IsPaymentComplianceConfirmed() {
		common.ApiErrorMsg(c, "payment compliance confirmation is required")
		return
	}

	provider, config, err := loadWeChatPayProvider(c.Request.Context())
	if err != nil {
		logger.LogWarn(c.Request.Context(), fmt.Sprintf("WeChat Pay create rejected reason=provider_unavailable user_id=%d", c.GetInt("id")))
		common.ApiErrorMsg(c, "WeChat Pay is not available")
		return
	}

	request := weChatNativePayRequest{}
	if err := common.DecodeJson(c.Request.Body, &request); err != nil {
		common.ApiErrorMsg(c, "invalid WeChat Pay request")
		return
	}
	if !weChatPayIdempotencyKeyPattern.MatchString(request.IdempotencyKey) {
		common.ApiErrorMsg(c, "invalid payment idempotency key")
		return
	}
	amountMinor, err := service.ParseWeChatCNYAmount(request.Amount)
	if err != nil || amountMinor < config.MinAmountMinor || amountMinor > config.MaxAmountMinor {
		common.ApiErrorMsg(c, "invalid CNY top-up amount")
		return
	}

	userID := c.GetInt("id")
	tradeNo := weChatPayTradeNo(userID, request.IdempotencyKey)
	topUp, err := model.GetUserWeChatTopUp(userID, tradeNo)
	if err == nil {
		if topUp.PaidAmountMinor != amountMinor || topUp.Currency != "CNY" {
			common.ApiError(c, model.ErrTopUpOrderConflict)
			return
		}
		if topUp.Status != common.TopUpStatusPending {
			writeWeChatTopUpResponse(c, topUp, "")
			return
		}
	} else {
		if !errors.Is(err, model.ErrTopUpNotFound) {
			common.ApiErrorMsg(c, "failed to load WeChat Pay order")
			return
		}
		creditedQuota, quotaErr := service.CalculateWeChatCreditedQuota(
			amountMinor,
			decimal.NewFromFloat(operation_setting.USDExchangeRate),
			decimal.NewFromFloat(common.QuotaPerUnit),
		)
		if quotaErr != nil {
			common.ApiErrorMsg(c, "invalid WeChat Pay top-up conversion")
			return
		}
		topUp, _, err = model.CreateOrReuseWeChatTopUp(model.WeChatTopUpOrderInput{
			UserID:        userID,
			TradeNo:       tradeNo,
			AmountMinor:   amountMinor,
			CreditedQuota: creditedQuota,
			CreateTime:    time.Now().Unix(),
		})
		if err != nil {
			common.ApiError(c, err)
			return
		}
	}

	createResult, err := provider.CreateNative(c.Request.Context(), service.WeChatPayCreateRequest{
		OutTradeNo:  topUp.TradeNo,
		AmountMinor: topUp.PaidAmountMinor,
		ExpiresAt:   time.Unix(topUp.CreateTime+model.WeChatTopUpOrderLifetimeSeconds, 0),
	})
	if err != nil {
		ambiguous := service.IsAmbiguousWeChatPayError(err)
		if !ambiguous {
			statusErr := model.UpdatePendingTopUpStatus(topUp.TradeNo, model.PaymentProviderWeChatPay, common.TopUpStatusFailed)
			if statusErr == nil {
				topUp.Status = common.TopUpStatusFailed
			} else if !errors.Is(statusErr, model.ErrTopUpStatusInvalid) {
				logger.LogError(c.Request.Context(), fmt.Sprintf("WeChat Pay definitive create rejection status update failed trade_no=%s error=%q", topUp.TradeNo, statusErr.Error()))
			}
		}
		logger.LogWarn(c.Request.Context(), fmt.Sprintf("WeChat Pay create failed trade_no=%s ambiguous=%t error=%q", topUp.TradeNo, ambiguous, err.Error()))
		c.JSON(http.StatusOK, gin.H{
			"success": false,
			"message": "failed to create WeChat Pay order",
			"data":    weChatTopUpData(topUp, ""),
		})
		return
	}

	logger.LogInfo(c.Request.Context(), fmt.Sprintf("WeChat Pay order created trade_no=%s request_id=%s amount_minor=%d", topUp.TradeNo, createResult.RequestID, topUp.PaidAmountMinor))
	writeWeChatTopUpResponse(c, topUp, createResult.CodeURL)
}

func writeWeChatTopUpResponse(c *gin.Context, topUp *model.TopUp, codeURL string) {
	common.ApiSuccess(c, weChatTopUpData(topUp, codeURL))
}

func weChatTopUpData(topUp *model.TopUp, codeURL string) gin.H {
	return gin.H{
		"trade_no":       topUp.TradeNo,
		"code_url":       codeURL,
		"amount":         decimal.NewFromInt(topUp.PaidAmountMinor).Shift(-2).StringFixed(2),
		"currency":       topUp.Currency,
		"credited_quota": topUp.CreditedQuota,
		"expires_at":     topUp.CreateTime + model.WeChatTopUpOrderLifetimeSeconds,
		"status":         topUp.Status,
	}
}

func weChatPayTradeNo(userID int, idempotencyKey string) string {
	digest := sha256.Sum256([]byte(strconv.Itoa(userID) + ":" + idempotencyKey))
	return "WXN" + fmt.Sprintf("%x", digest)[:29]
}

func GetWeChatPayOrder(c *gin.Context) {
	topUp, err := model.GetUserWeChatTopUp(c.GetInt("id"), c.Param("trade_no"))
	if err != nil {
		if errors.Is(err, model.ErrTopUpNotFound) {
			c.AbortWithStatus(http.StatusNotFound)
			return
		}
		c.AbortWithStatus(http.StatusServiceUnavailable)
		return
	}
	writeWeChatTopUpResponse(c, topUp, "")
}

func WeChatPayNotify(c *gin.Context) {
	provider, config, err := loadWeChatPayCallbackProvider(c.Request.Context())
	if err != nil {
		logger.LogError(c.Request.Context(), fmt.Sprintf("WeChat Pay callback unavailable path=%q client_ip=%s", c.Request.RequestURI, c.ClientIP()))
		c.AbortWithStatus(http.StatusServiceUnavailable)
		return
	}

	notification, err := provider.ParseNotify(c.Request.Context(), c.Request)
	if err != nil {
		logger.LogWarn(c.Request.Context(), fmt.Sprintf("WeChat Pay callback verification failed path=%q client_ip=%s error_type=%T", c.Request.RequestURI, c.ClientIP(), err))
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}
	if notification.EventType != "TRANSACTION.SUCCESS" {
		acknowledgeWeChatPayIncident(c, notification, 0, "unexpected WeChat Pay event type")
		return
	}

	topUp, err := model.GetTopUpByTradeNoWithError(notification.Transaction.OutTradeNo)
	if errors.Is(err, model.ErrTopUpNotFound) {
		acknowledgeWeChatPayIncident(c, notification, 0, "unknown WeChat Pay order")
		return
	}
	if err != nil {
		logger.LogError(c.Request.Context(), fmt.Sprintf("WeChat Pay callback order lookup failed event_id=%s trade_no=%s", notification.EventID, notification.Transaction.OutTradeNo))
		c.AbortWithStatus(http.StatusServiceUnavailable)
		return
	}
	if err := service.ValidateWeChatPaySuccessfulTransaction(
		config,
		notification.Transaction,
		topUp.TradeNo,
		topUp.PaidAmountMinor,
	); err != nil {
		acknowledgeWeChatPayIncident(c, notification, topUp.UserId, err.Error())
		return
	}

	alreadyDone, _, _, err := settleWeChatTopUp(model.WeChatTopUpSettlement{
		TradeNo:         notification.Transaction.OutTradeNo,
		ProviderTradeNo: notification.Transaction.TransactionID,
		AmountMinor:     notification.Transaction.AmountMinor,
		Currency:        notification.Transaction.Currency,
		CallerIP:        c.ClientIP(),
	})
	if err != nil {
		if errors.Is(err, model.ErrTopUpNotFound) ||
			errors.Is(err, model.ErrPaymentMethodMismatch) ||
			errors.Is(err, model.ErrTopUpStatusInvalid) ||
			errors.Is(err, model.ErrTopUpAmountMismatch) ||
			errors.Is(err, model.ErrTopUpCurrencyMismatch) ||
			errors.Is(err, model.ErrTopUpProviderTradeConflict) {
			acknowledgeWeChatPayIncident(c, notification, topUp.UserId, err.Error())
			return
		}
		logger.LogError(c.Request.Context(), fmt.Sprintf("WeChat Pay callback settlement failed event_id=%s trade_no=%s transaction_id=%s error=%q", notification.EventID, notification.Transaction.OutTradeNo, notification.Transaction.TransactionID, err.Error()))
		c.AbortWithStatus(http.StatusServiceUnavailable)
		return
	}

	logger.LogInfo(c.Request.Context(), fmt.Sprintf("WeChat Pay callback settled event_id=%s trade_no=%s transaction_id=%s already_done=%t", notification.EventID, notification.Transaction.OutTradeNo, notification.Transaction.TransactionID, alreadyDone))
	c.AbortWithStatus(http.StatusNoContent)
}

func acknowledgeWeChatPayIncident(c *gin.Context, notification service.WeChatPayNotifyResult, userID int, reason string) {
	incident := model.WeChatPayIncident{
		UserID:          userID,
		EventID:         notification.EventID,
		EventType:       notification.EventType,
		TradeNo:         notification.Transaction.OutTradeNo,
		ProviderTradeNo: notification.Transaction.TransactionID,
		Reason:          reason,
		RequestID:       c.GetString(common.RequestIdKey),
		CallerIP:        c.ClientIP(),
	}
	if err := recordWeChatPayIncident(incident); err != nil {
		logger.LogError(c.Request.Context(), fmt.Sprintf("WeChat Pay callback incident persistence failed event_id=%s trade_no=%s reason=%q error=%q", incident.EventID, incident.TradeNo, reason, err.Error()))
		c.AbortWithStatus(http.StatusServiceUnavailable)
		return
	}
	logger.LogWarn(c.Request.Context(), fmt.Sprintf("WeChat Pay callback incident recorded event_id=%s trade_no=%s transaction_id=%s reason=%q", incident.EventID, incident.TradeNo, incident.ProviderTradeNo, reason))
	c.AbortWithStatus(http.StatusNoContent)
}
