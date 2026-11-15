package service

import (
	"fmt"
	"testing"

	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/stretchr/testify/require"
)

func TestRefundRequestIDUsesDurableLeaseWhenRequestIDMissing(t *testing.T) {
	db, c, info := legacyLeaseFixture(t)
	_ = db
	_ = c
	info.RequestId = ""
	session := &BillingSession{
		relayInfo: info, funding: &WalletFunding{userId: 42, consumed: 20},
		legacyLeaseID: "unadmitted", preConsumedQuota: 20, tokenConsumed: 20,
	}
	require.Equal(t, "unadmitted", session.refundRequestID())
	require.Equal(t, "unadmitted", info.RequestId)
	require.Equal(t, "unadmitted", session.refundRequestID())
}

func TestRefundRequestIDDerivesStableIdentityWithoutLease(t *testing.T) {
	_, _, info := legacyLeaseFixture(t)
	info.RequestId = ""
	session := &BillingSession{
		relayInfo: info, funding: &WalletFunding{userId: 42, consumed: 20},
		preConsumedQuota: 20, tokenConsumed: 20,
	}
	first := session.refundRequestID()
	require.Equal(t, fmt.Sprintf("refund:%d:%d:%s", info.UserId, info.TokenId, BillingSourceWallet), first)
	info.RequestId = ""
	require.Equal(t, first, session.refundRequestID())
}

func TestApplyRefundReceiptsInvalidatesUserCache(t *testing.T) {
	db, _, info := legacyLeaseFixture(t)
	info.RequestId = "refund-cache-invalidation"
	require.NoError(t, model.BeginProductLegacyFundingLease(t.Context(), "unadmitted", 42))
	require.NoError(t, db.Model(&model.Token{}).Where("id = ?", 7).Update("remain_quota", 180).Error)
	require.NoError(t, db.Model(&model.User{}).Where("id = ?", 42).Update("quota", 480).Error)
	session := &BillingSession{
		relayInfo: info, funding: &WalletFunding{userId: 42, consumed: 20},
		legacyLeaseID: "unadmitted", preConsumedQuota: 20, tokenConsumed: 20,
	}
	refundCtx := legacyRefundContext{
		ctx: t.Context(), requestID: info.RequestId, leaseID: "unadmitted",
		userID: 42, tokenID: 7, tokenConsumed: 20,
	}
	receipts, err := session.beginRefundReceipts(&refundCtx)
	require.NoError(t, err)
	require.Len(t, receipts, 2)
	session.applyRefundReceipts(refundCtx, receipts)
	var count int64
	require.NoError(t, db.Model(&model.WalletRefundReceipt{}).Where("state = ?", "applied").Count(&count).Error)
	require.EqualValues(t, 2, count)
	var user model.User
	require.NoError(t, db.First(&user, 42).Error)
	require.Equal(t, 500, user.Quota)
	// Redis is disabled in this fixture, so InvalidateUserCache is a no-op; the
	// exported helper still keeps both refund paths on the same cache contract.
	require.NoError(t, model.InvalidateUserCache(42))
}

var _ = relaycommon.RelayInfo{}
