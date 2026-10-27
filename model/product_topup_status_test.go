package model

import (
	"context"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestOriginalPaymentHistoryDistinguishesConfirmedPaymentFromDeliveredCredit(t *testing.T) {
	paymentParityFixture(t)
	paid := &TopUp{UserId: 42, TradeNo: "paid-order", PaymentProvider: "manual", Status: "success"}
	local := &TopUp{UserId: 42, TradeNo: "legacy", PaymentProvider: "manual", Status: "success"}
	id := productDigest("payment:" + productDigest("manual:paid-order"))
	require.NoError(t, DB.Create(&ProductCreditAward{ID: id, UserID: 42, State: "pending"}).Error)
	require.NoError(t, PopulateProductTopUpCreditStatuses(context.Background(), []*TopUp{paid, local}))
	require.Equal(t, "success", paid.Status)
	require.Equal(t, "pending", paid.CreditStatus)
	require.Empty(t, local.CreditStatus)
	require.NoError(t, DB.Model(&ProductCreditAward{}).Where("id = ?", id).Update("state", "applied").Error)
	require.NoError(t, PopulateProductTopUpCreditStatuses(context.Background(), []*TopUp{paid}))
	require.Equal(t, "applied", paid.CreditStatus)
}
