package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
)

type WeChatPayReconcileSummary struct {
	Claimed        int `json:"claimed"`
	Settled        int `json:"settled"`
	AlreadySettled int `json:"already_settled"`
	Pending        int `json:"pending"`
	Expired        int `json:"expired"`
	Incidents      int `json:"incidents"`
	IncidentErrors int `json:"incident_errors"`
	QueryErrors    int `json:"query_errors"`
	CloseErrors    int `json:"close_errors"`
	ModelErrors    int `json:"model_errors"`
	ClaimErrors    int `json:"claim_errors"`
}

func ReconcileWeChatTopUps(
	ctx context.Context,
	provider WeChatPayProvider,
	config WeChatPayConfig,
	now int64,
	limit int,
) WeChatPayReconcileSummary {
	summary := WeChatPayReconcileSummary{}
	claimed, err := model.ClaimDueWeChatTopUps(now, limit)
	if err != nil {
		summary.ClaimErrors++
		logger.LogError(ctx, fmt.Sprintf("WeChat Pay reconciliation claim failed error=%q", err.Error()))
		return summary
	}
	summary.Claimed = len(claimed)

	for index, topUp := range claimed {
		if ctx.Err() != nil {
			summary.Pending += len(claimed) - index
			break
		}
		queryResult, queryErr := provider.QueryOrder(ctx, topUp.TradeNo)
		if queryErr != nil {
			summary.QueryErrors++
			summary.Pending++
			logger.LogWarn(ctx, fmt.Sprintf("WeChat Pay reconciliation query failed trade_no=%s %s", topUp.TradeNo, summarizeWeChatPayError(queryErr)))
			continue
		}
		if !validateReconciledWeChatTransaction(ctx, config, topUp, queryResult.Transaction, &summary) {
			continue
		}

		switch queryResult.Transaction.TradeState {
		case "SUCCESS":
			settleReconciledWeChatTopUp(ctx, topUp, queryResult.Transaction, &summary)
		case "CLOSED":
			expireReconciledWeChatTopUp(ctx, topUp, &summary)
		case "NOTPAY":
			if now < topUp.CreateTime+model.WeChatTopUpOrderLifetimeSeconds {
				summary.Pending++
				continue
			}
			if _, closeErr := provider.CloseOrder(ctx, topUp.TradeNo); closeErr != nil {
				summary.CloseErrors++
				summary.Pending++
				logger.LogWarn(ctx, fmt.Sprintf("WeChat Pay reconciliation close failed trade_no=%s %s", topUp.TradeNo, summarizeWeChatPayError(closeErr)))
				continue
			}
			afterClose, secondQueryErr := provider.QueryOrder(ctx, topUp.TradeNo)
			if secondQueryErr != nil {
				summary.QueryErrors++
				summary.Pending++
				logger.LogWarn(ctx, fmt.Sprintf("WeChat Pay reconciliation post-close query failed trade_no=%s %s", topUp.TradeNo, summarizeWeChatPayError(secondQueryErr)))
				continue
			}
			if !validateReconciledWeChatTransaction(ctx, config, topUp, afterClose.Transaction, &summary) {
				continue
			}
			switch afterClose.Transaction.TradeState {
			case "SUCCESS":
				settleReconciledWeChatTopUp(ctx, topUp, afterClose.Transaction, &summary)
			case "CLOSED", "NOTPAY":
				expireReconciledWeChatTopUp(ctx, topUp, &summary)
			default:
				summary.Pending++
			}
		default:
			summary.Pending++
		}
	}
	return summary
}

func validateReconciledWeChatTransaction(
	ctx context.Context,
	config WeChatPayConfig,
	topUp *model.TopUp,
	transaction WeChatPayTransaction,
	summary *WeChatPayReconcileSummary,
) bool {
	if err := ValidateWeChatPayTransactionIdentity(config, transaction, topUp.TradeNo, topUp.PaidAmountMinor); err != nil {
		recordWeChatReconciliationIncident(ctx, topUp, transaction, err, summary)
		return false
	}
	if transaction.TradeState == "SUCCESS" && transaction.TransactionID == "" {
		recordWeChatReconciliationIncident(ctx, topUp, transaction, errors.New("missing WeChat Pay transaction ID"), summary)
		return false
	}
	return true
}

func settleReconciledWeChatTopUp(
	ctx context.Context,
	topUp *model.TopUp,
	transaction WeChatPayTransaction,
	summary *WeChatPayReconcileSummary,
) {
	alreadyDone, _, _, err := model.SettleWeChatTopUp(model.WeChatTopUpSettlement{
		TradeNo:         topUp.TradeNo,
		ProviderTradeNo: transaction.TransactionID,
		AmountMinor:     transaction.AmountMinor,
		Currency:        transaction.Currency,
	})
	if err != nil {
		if errors.Is(err, model.ErrPaymentMethodMismatch) ||
			errors.Is(err, model.ErrTopUpStatusInvalid) ||
			errors.Is(err, model.ErrTopUpAmountMismatch) ||
			errors.Is(err, model.ErrTopUpCurrencyMismatch) ||
			errors.Is(err, model.ErrTopUpProviderTradeConflict) {
			recordWeChatReconciliationIncident(ctx, topUp, transaction, err, summary)
			return
		}
		summary.ModelErrors++
		logger.LogError(ctx, fmt.Sprintf("WeChat Pay reconciliation settlement failed trade_no=%s transaction_id=%s error=%q", topUp.TradeNo, transaction.TransactionID, err.Error()))
		return
	}
	if alreadyDone {
		summary.AlreadySettled++
		return
	}
	summary.Settled++
}

func expireReconciledWeChatTopUp(ctx context.Context, topUp *model.TopUp, summary *WeChatPayReconcileSummary) {
	if err := model.ExpireWeChatTopUp(topUp.TradeNo); err != nil {
		if errors.Is(err, model.ErrTopUpStatusInvalid) {
			reloaded, loadErr := model.GetTopUpByTradeNoWithError(topUp.TradeNo)
			if loadErr == nil && reloaded.Status == common.TopUpStatusSuccess {
				summary.AlreadySettled++
				return
			}
		}
		summary.ModelErrors++
		logger.LogError(ctx, fmt.Sprintf("WeChat Pay reconciliation expiry failed trade_no=%s error=%q", topUp.TradeNo, err.Error()))
		return
	}
	summary.Expired++
}

func summarizeWeChatPayError(err error) string {
	var payErr *WeChatPayError
	if errors.As(err, &payErr) {
		return fmt.Sprintf(
			"code=%s status=%d request_id=%s ambiguous=%t",
			payErr.Code,
			payErr.StatusCode,
			payErr.RequestID,
			payErr.Ambiguous,
		)
	}
	return fmt.Sprintf("error_type=%T", err)
}

func recordWeChatReconciliationIncident(
	ctx context.Context,
	topUp *model.TopUp,
	transaction WeChatPayTransaction,
	incidentErr error,
	summary *WeChatPayReconcileSummary,
) {
	err := model.RecordWeChatPayIncident(model.WeChatPayIncident{
		UserID:          topUp.UserId,
		EventType:       "SIGNED_QUERY",
		TradeNo:         topUp.TradeNo,
		ProviderTradeNo: transaction.TransactionID,
		Reason:          incidentErr.Error(),
		Source:          "reconciliation",
	})
	if err != nil {
		summary.IncidentErrors++
		logger.LogError(ctx, fmt.Sprintf("WeChat Pay reconciliation incident persistence failed trade_no=%s error=%q", topUp.TradeNo, err.Error()))
		return
	}
	summary.Incidents++
}
