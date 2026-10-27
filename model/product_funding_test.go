package model

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	userkit "github.com/NeoMei/UniHub/packages/userkit-go"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/metering"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// The remote boundary represents the central ledger. The product journal,
// token limits, user counters and SQL transactions remain real in these tests.
func productFundingFixture(t *testing.T) (*ProductFundingStore, *gorm.DB, *runtimeLedgerFixture) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(t.TempDir()+"/product.db"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&User{}, &Token{}))
	require.NoError(t, RegisterProductFundingModels(db))
	require.NoError(t, db.Create(&User{Id: 42, Username: "normal-user", Role: common.RoleCommonUser, Status: common.UserStatusEnabled, Quota: 500}).Error)
	require.NoError(t, db.Create(&Token{Id: 7, UserId: 42, Key: "original-key", Status: common.TokenStatusEnabled, ExpiredTime: -1, RemainQuota: 200}).Error)
	ledger := &runtimeLedgerFixture{calls: map[string]userkit.RuntimeCall{}}
	server := httptest.NewServer(http.HandlerFunc(ledger.serve))
	ledger.origin = server.URL
	t.Cleanup(server.Close)
	client, err := userkit.NewRuntimeClient(userkit.ServiceConfig{BaseURL: server.URL, ProductID: "llmrouter", InstanceID: "default", ServiceToken: strings.Repeat("t", 32)})
	require.NoError(t, err)
	return NewProductFundingStore(db, client), db, ledger
}

type runtimeLedgerFixture struct {
	origin                  string
	balance, held, consumed int
	settlements, imports    int
	loseSettleReply         bool
	loseReserveReply        bool
	rejectReserve           bool
	rejectTopUp             bool
	loseTopUpReply          bool
	loseRefundReply         bool
	refunds                 map[string]int
	topups                  map[string]string
	calls                   map[string]userkit.RuntimeCall
}

func (f *runtimeLedgerFixture) serve(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/internal/v1/product-runtime")
	var out any
	switch {
	case strings.HasSuffix(path, "/refund"):
		id := strings.TrimSuffix(strings.TrimPrefix(path, "/calls/"), "/refund")
		var in userkit.RuntimeRefundRequest
		_ = common.DecodeJson(r.Body, &in)
		if f.refunds == nil {
			f.refunds = map[string]int{}
		}
		key := id + ":" + in.RequestKey
		amount := fixtureQuota(in.Amount)
		if old, ok := f.refunds[key]; ok {
			if old != amount {
				w.WriteHeader(409)
				return
			}
		} else {
			f.balance += amount
			f.consumed -= amount
			f.refunds[key] = amount
		}
		if f.loseRefundReply {
			f.loseRefundReply = false
			w.WriteHeader(503)
			return
		}
		out = userkit.RuntimeAdjustment{ID: key, ProductID: "llmrouter", InstanceID: "default", AccountID: "payer", Amount: in.Amount, Status: "APPLIED"}
	case path == "/migrations":
		var in userkit.RuntimeImportRequest
		_ = common.DecodeJson(r.Body, &in)
		if f.imports == 0 {
			f.balance = 500
			f.imports++
		}
		out = userkit.RuntimeAdjustment{ID: "import-42", ProductID: "llmrouter", InstanceID: "default", AccountID: "payer", Amount: in.Amount, Status: "APPLIED"}
	case strings.HasSuffix(path, "/balance"):
		out = userkit.RuntimeBalance{ProductID: "llmrouter", InstanceID: "default", AccountID: "payer", Asset: userkit.RuntimeAsset, UnitVersion: userkit.RuntimeUnitVersion, Available: strconv.Itoa(f.balance), Held: strconv.Itoa(f.held), Consumed: strconv.Itoa(f.consumed)}
	case path == "/calls/reserve":
		if f.rejectReserve {
			w.WriteHeader(402)
			return
		}
		var in userkit.RuntimeReserveRequest
		_ = common.DecodeJson(r.Body, &in)
		call, found := f.calls[in.CallID]
		if !found {
			amount := fixtureQuota(in.Amount)
			f.balance -= amount
			f.held += amount
			call = userkit.RuntimeCall{RuntimeReserveRequest: in, ProductID: "llmrouter", InstanceID: "default", ActorID: "central-user", Held: in.Amount, Charged: "0", Status: "RESERVED"}
			f.calls[in.CallID] = call
		}
		out = call
		if f.loseReserveReply {
			f.loseReserveReply = false
			w.WriteHeader(503)
			return
		}
	case strings.HasSuffix(path, "/topup"):
		if f.rejectTopUp {
			w.WriteHeader(402)
			return
		}
		id := strings.TrimSuffix(strings.TrimPrefix(path, "/calls/"), "/topup")
		var in userkit.RuntimeTopUpRequest
		_ = common.DecodeJson(r.Body, &in)
		if f.topups == nil {
			f.topups = map[string]string{}
		}
		key := id + ":" + in.RequestKey
		call := f.calls[id]
		if previous, ok := f.topups[key]; ok {
			if previous != in.Amount {
				w.WriteHeader(409)
				return
			}
		} else {
			amount := fixtureQuota(in.Amount)
			f.balance -= amount
			f.held += amount
			call.Held = strconv.Itoa(fixtureQuota(call.Held) + amount)
			f.calls[id] = call
			f.topups[key] = in.Amount
		}
		if f.loseTopUpReply {
			f.loseTopUpReply = false
			w.WriteHeader(503)
			return
		}
		out = call
	case strings.HasSuffix(path, "/release"):
		id := strings.TrimSuffix(strings.TrimPrefix(path, "/calls/"), "/release")
		call := f.calls[id]
		if call.Status != "RELEASED" {
			held := fixtureQuota(call.Held)
			f.held -= held
			f.balance += held
			call.Status = "RELEASED"
			f.calls[id] = call
		}
		out = call
	case strings.HasSuffix(path, "/settle"):
		id := strings.TrimSuffix(strings.TrimPrefix(path, "/calls/"), "/settle")
		var in userkit.RuntimeSettlementRequest
		_ = common.DecodeJson(r.Body, &in)
		call := f.calls[id]
		if call.Status != "SETTLED" {
			amount := fixtureQuota(in.Amount)
			hold := fixtureQuota(call.Held)
			f.held -= hold
			f.balance += hold - amount
			f.consumed += amount
			f.settlements++
			call.Status = "SETTLED"
			call.Charged = in.Amount
			f.calls[id] = call
		}
		if f.loseSettleReply {
			f.loseSettleReply = false
			w.WriteHeader(503)
			return
		}
		out = call
	case strings.HasPrefix(path, "/calls/") && r.Method == "GET":
		var ok bool
		out, ok = f.calls[strings.TrimPrefix(path, "/calls/")]
		if !ok {
			w.WriteHeader(404)
			return
		}
	default:
		w.WriteHeader(404)
		return
	}
	data, _ := common.Marshal(out)
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(data)
}

func TestProductFundingPreservesOriginalKeyAndSettlesOneCentralCharge(t *testing.T) {
	s, db, ledger := productFundingFixture(t)
	ctx := context.Background()
	require.NoError(t, s.Import(ctx, 42, "payer", "approved-batch"))
	call, err := s.Reserve(ctx, ProductFundingAdmission{CallID: "call-1", UserID: 42, TokenID: 7, Model: "original-model", Operation: "chat", PriceSnapshot: `{"model_ratio":1}`, Quota: 50})
	require.NoError(t, err)
	require.Equal(t, "payer", call.AccountID)
	var token Token
	require.NoError(t, db.First(&token, 7).Error)
	require.Equal(t, 150, token.RemainQuota)
	require.NoError(t, s.Settle(ctx, "call-1", 30, `{"input_tokens":10,"output_tokens":20}`))
	require.NoError(t, s.Settle(ctx, "call-1", 30, `{"input_tokens":10,"output_tokens":20}`))
	require.Equal(t, 1, ledger.settlements)
	require.Equal(t, 470, ledger.balance)
	require.NoError(t, db.First(&token, 7).Error)
	require.Equal(t, 170, token.RemainQuota)
	require.Equal(t, 30, token.UsedQuota)
	require.Equal(t, "original-key", token.Key)
	var user User
	require.NoError(t, db.First(&user, 42).Error)
	require.Zero(t, user.Quota)
	require.Equal(t, 30, user.UsedQuota)
	require.Equal(t, 1, user.RequestCount)
	require.Error(t, s.Settle(ctx, "call-1", 31, `{"input_tokens":10,"output_tokens":20}`))
}

func TestProductFundingLostSettlementReplyRecoversWithoutSecondDebit(t *testing.T) {
	s, db, ledger := productFundingFixture(t)
	ctx := context.Background()
	require.NoError(t, s.Import(ctx, 42, "payer", "approved-batch"))
	_, err := s.Reserve(ctx, ProductFundingAdmission{CallID: "call-2", UserID: 42, TokenID: 7, Model: "m", Operation: "embedding", PriceSnapshot: `{"price":1}`, Quota: 40})
	require.NoError(t, err)
	ledger.loseSettleReply = true
	require.Error(t, s.Settle(ctx, "call-2", 20, `{"tokens":20}`))
	resumed := NewProductFundingStore(db, s.client)
	require.NoError(t, resumed.Recover(ctx))
	require.Equal(t, 1, ledger.settlements)
	require.Equal(t, 480, ledger.balance)
	var token Token
	require.NoError(t, db.First(&token, 7).Error)
	require.Equal(t, 180, token.RemainQuota)
}

func TestProductFundingImportKeepsFrozenSnapshotAndDoesNotMintKeyLimits(t *testing.T) {
	s, db, ledger := productFundingFixture(t)
	ctx := context.Background()
	require.NoError(t, s.Import(ctx, 42, "payer", "approved-batch"))
	require.NoError(t, s.Import(ctx, 42, "payer", "approved-batch"))
	require.Equal(t, 1, ledger.imports)
	var account ProductFundingAccount
	require.NoError(t, db.First(&account, 42).Error)
	require.Equal(t, 500, account.SnapshotQuota)
	require.NotEmpty(t, account.SnapshotDigest)
	require.Equal(t, "central", account.State)
	require.Error(t, s.Import(ctx, 42, "other-payer", "different-batch"))
	var token Token
	require.NoError(t, db.First(&token, 7).Error)
	require.Equal(t, 200, token.RemainQuota)
}

func TestProductFundingNativeMeteringRetainsOriginalCalculationAndAudit(t *testing.T) {
	s, db, ledger := productFundingFixture(t)
	ctx := context.Background()
	require.NoError(t, db.AutoMigrate(&NativeBilling{}, &OCRJob{}, &OCROperation{}, &OCRAuditEvent{}, &Channel{}, &UserSubscription{}, &NativeUsageReceipt{}))
	require.NoError(t, db.Create(&Channel{Id: 8, Name: "original-channel"}).Error)
	require.NoError(t, s.Import(ctx, 42, "payer", "approved-batch"))
	t.Setenv("UNIHUB_PRODUCT_RUNTIME_ENABLED", "true")
	t.Setenv("UNIHUB_PRODUCT_RUNTIME_TOKEN", strings.Repeat("t", 32))
	t.Setenv("UNIHUB_ORIGIN", ledger.origin)
	t.Setenv("UNIHUB_PRODUCT_ID", "llmrouter")
	t.Setenv("UNIHUB_PRODUCT_INSTANCE_ID", "default")
	oldDB := DB
	DB = db
	t.Cleanup(func() { DB = oldDB })
	native := NewNativeBillingStore(db, nil)
	price := metering.Price{Version: "metered_v2", Operation: "speech", Policy: "provider_reported_v1", Terms: []metering.Term{{Key: "characters", Unit: "character", Price: metering.Rational{Numerator: "1", Denominator: "10"}}}}
	a := NativeAdmission{CallID: "native-central", UserID: 42, TokenID: 7, ChannelID: 8, Model: "speech-model", Group: "default", Preference: "wallet_only", Price: price, GroupRatio: "1", QuotaPerUnit: "1", Reservation: nativeUsage("400")}
	n, err := native.Begin(ctx, a)
	require.NoError(t, err)
	require.NoError(t, native.MarkDispatched(ctx, n.ID, n.AttemptID))
	require.NoError(t, native.RecordUsage(ctx, n.ID, nativeUsage("200")))
	n, err = native.Finish(ctx, n.ID, "measured")
	require.NoError(t, err)
	require.Equal(t, "settled", n.State)
	_, err = native.Finish(ctx, n.ID, "measured")
	require.NoError(t, err)
	require.Equal(t, 20, ledger.consumed)
	var user User
	require.NoError(t, db.First(&user, 42).Error)
	require.Zero(t, user.Quota)
	require.Equal(t, 20, user.UsedQuota)
	require.Equal(t, 1, user.RequestCount)
	var audit OCRAuditEvent
	require.NoError(t, db.Where("job_id = ?", n.ID).First(&audit).Error)
	require.Equal(t, "success", audit.Kind)
	var token Token
	require.NoError(t, db.First(&token, 7).Error)
	require.Equal(t, 180, token.RemainQuota)
}

func TestProductFundingOriginalBalanceReadUsesCentralLedgerAndRejectsLocalWrites(t *testing.T) {
	s, db, ledger := productFundingFixture(t)
	ctx := context.Background()
	require.NoError(t, s.Import(ctx, 42, "payer", "approved-batch"))
	t.Setenv("UNIHUB_PRODUCT_RUNTIME_ENABLED", "true")
	t.Setenv("UNIHUB_PRODUCT_RUNTIME_TOKEN", strings.Repeat("t", 32))
	t.Setenv("UNIHUB_ORIGIN", ledger.origin)
	oldDB := DB
	DB = db
	t.Cleanup(func() { DB = oldDB })
	quota, err := GetUserQuota(42, true)
	require.NoError(t, err)
	require.Equal(t, 500, quota)
	require.Error(t, IncreaseUserQuota(42, 10, true))
	require.Error(t, DecreaseUserQuota(42, 10, true))
	t.Setenv("UNIHUB_PRODUCT_RUNTIME_ENABLED", "false")
	_, err = GetUserQuota(42, true)
	require.Error(t, err)
}

func TestProductFundingNativeSubscriptionUsesPurchasedEntitlementWithoutWalletCharge(t *testing.T) {
	s, db, ledger := productFundingFixture(t)
	ctx := context.Background()
	require.NoError(t, db.AutoMigrate(&NativeBilling{}, &OCRJob{}, &OCROperation{}, &OCRAuditEvent{}, &Channel{}, &UserSubscription{}, &SubscriptionPlan{}, &NativeUsageReceipt{}))
	require.NoError(t, db.Create(&Channel{Id: 8, Name: "speech"}).Error)
	require.NoError(t, db.Create(&SubscriptionPlan{Id: 1, Title: "original-prepaid-entitlement"}).Error)
	require.NoError(t, db.Create(&UserSubscription{Id: 1, UserId: 42, PlanId: 1, Status: "active", AmountTotal: 100, StartTime: time.Now().Unix(), EndTime: time.Now().Add(time.Hour).Unix()}).Error)
	require.NoError(t, s.Import(ctx, 42, "payer", "reviewed"))
	t.Setenv("UNIHUB_PRODUCT_RUNTIME_ENABLED", "true")
	t.Setenv("UNIHUB_PRODUCT_RUNTIME_TOKEN", strings.Repeat("t", 32))
	t.Setenv("UNIHUB_ORIGIN", ledger.origin)
	oldDB := DB
	DB = db
	t.Cleanup(func() { DB = oldDB })
	native := NewNativeBillingStore(db, nil)
	price := metering.Price{Version: "metered_v2", Operation: "speech", Policy: "provider_reported_v1", Terms: []metering.Term{{Key: "characters", Unit: "character", Price: metering.Rational{Numerator: "1", Denominator: "10"}}}}
	n, err := native.Begin(ctx, NativeAdmission{CallID: "paid-subscription", UserID: 42, TokenID: 7, ChannelID: 8, Model: "speech", Group: "default", Preference: "subscription_only", Price: price, GroupRatio: "1", QuotaPerUnit: "1", Reservation: nativeUsage("400")})
	require.NoError(t, err)
	require.NoError(t, native.RecordUsage(ctx, n.ID, nativeUsage("200")))
	_, err = native.Finish(ctx, n.ID, "measured")
	require.NoError(t, err)
	require.Equal(t, 500, ledger.balance)
	require.Zero(t, ledger.consumed)
	var sub UserSubscription
	require.NoError(t, db.First(&sub, 1).Error)
	require.EqualValues(t, 20, sub.AmountUsed)
}

func fixtureQuota(raw string) int { n, _ := strconv.Atoi(raw); return n }

func TestProductFundingRejectedReserveRestoresOriginalKeyLimit(t *testing.T) {
	s, db, ledger := productFundingFixture(t)
	ctx := context.Background()
	require.NoError(t, s.Import(ctx, 42, "payer", "batch"))
	ledger.rejectReserve = true
	_, err := s.Reserve(ctx, ProductFundingAdmission{CallID: "reject", UserID: 42, TokenID: 7, Model: "m", Operation: "chat", PriceSnapshot: `{"price":1}`, Quota: 50})
	require.Error(t, err)
	var token Token
	require.NoError(t, db.First(&token, 7).Error)
	require.Equal(t, 200, token.RemainQuota)
	require.Zero(t, token.UsedQuota)
}
func TestProductFundingLostReserveReplyRecoversUndispatchedHold(t *testing.T) {
	s, db, ledger := productFundingFixture(t)
	ctx := context.Background()
	require.NoError(t, s.Import(ctx, 42, "payer", "batch"))
	ledger.loseReserveReply = true
	_, err := s.Reserve(ctx, ProductFundingAdmission{CallID: "lost", UserID: 42, TokenID: 7, Model: "m", Operation: "chat", PriceSnapshot: `{"price":1}`, Quota: 50})
	require.Error(t, err)
	require.NoError(t, db.Model(&ProductRuntimeCall{}).Where("id = ?", "lost").Update("dispatch_lease_until", time.Now().Unix()-1).Error)
	require.NoError(t, s.Recover(ctx))
	var token Token
	require.NoError(t, db.First(&token, 7).Error)
	require.Equal(t, 200, token.RemainQuota)
	require.Zero(t, ledger.held)
	require.Equal(t, 500, ledger.balance)
}
func TestProductFundingTopUpCannotReserveMoneyBeyondOriginalKeyLimit(t *testing.T) {
	s, db, ledger := productFundingFixture(t)
	ctx := context.Background()
	require.NoError(t, s.Import(ctx, 42, "payer", "batch"))
	_, err := s.Reserve(ctx, ProductFundingAdmission{CallID: "limited", UserID: 42, TokenID: 7, Model: "m", Operation: "chat", PriceSnapshot: `{"price":1}`, Quota: 50})
	require.NoError(t, err)
	require.Error(t, s.TopUp(ctx, "limited", 250))
	require.Equal(t, 50, ledger.held)
	require.Equal(t, 450, ledger.balance)
	var token Token
	require.NoError(t, db.First(&token, 7).Error)
	require.Equal(t, 150, token.RemainQuota)
}
func TestProductFundingImportWaitsForLegacyLeaseWithoutFreezingIt(t *testing.T) {
	s, db, ledger := productFundingFixture(t)
	ctx := context.Background()
	require.NoError(t, RegisterProductFundingModels(db))
	old := DB
	DB = db
	t.Cleanup(func() { DB = old })
	require.NoError(t, BeginProductLegacyFundingLease(ctx, "old-live-call", 42))
	require.ErrorIs(t, s.Import(ctx, 42, "payer", "batch"), ErrProductFundingPending)
	var user User
	require.NoError(t, db.First(&user, 42).Error)
	require.Equal(t, 500, user.Quota)
	require.Zero(t, ledger.imports)
	require.NoError(t, FinishProductLegacyFundingLease(ctx, "old-live-call", "settled"))
	require.NoError(t, s.Import(ctx, 42, "payer", "batch"))
	require.Error(t, BeginProductLegacyFundingLease(ctx, "too-late", 42))
}

func TestProductFundingLostTopUpReplyReplaysDurableDeltaOnce(t *testing.T) {
	s, db, ledger := productFundingFixture(t)
	ctx := context.Background()
	require.NoError(t, s.Import(ctx, 42, "payer", "batch"))
	_, err := s.Reserve(ctx, ProductFundingAdmission{CallID: "topup-lost", UserID: 42, TokenID: 7, Model: "m", Operation: "chat", PriceSnapshot: `{"price":1}`, Quota: 50})
	require.NoError(t, err)
	ledger.loseTopUpReply = true
	require.Error(t, s.TopUp(ctx, "topup-lost", 80))
	require.Equal(t, 80, ledger.held)
	var token Token
	require.NoError(t, db.First(&token, 7).Error)
	require.Equal(t, 120, token.RemainQuota)
	require.NoError(t, s.Recover(ctx))
	require.Equal(t, 80, ledger.held)
	require.Equal(t, 1, len(ledger.topups))
	require.NoError(t, s.Release(ctx, "topup-lost", "not dispatched"))
	require.NoError(t, db.First(&token, 7).Error)
	require.Equal(t, 200, token.RemainQuota)
	require.Equal(t, 500, ledger.balance)
}
func TestProductFundingTopUpRejectedCanRetryAfterCapacityReturns(t *testing.T) {
	s, db, ledger := productFundingFixture(t)
	ctx := context.Background()
	require.NoError(t, s.Import(ctx, 42, "payer", "batch"))
	_, err := s.Reserve(ctx, ProductFundingAdmission{CallID: "topup-rejected", UserID: 42, TokenID: 7, Model: "m", Operation: "chat", PriceSnapshot: `{"price":1}`, Quota: 50})
	require.NoError(t, err)
	ledger.rejectTopUp = true
	require.Error(t, s.TopUp(ctx, "topup-rejected", 80))
	var token Token
	require.NoError(t, db.First(&token, 7).Error)
	require.Equal(t, 150, token.RemainQuota)
	require.Equal(t, 50, ledger.held)
	ledger.rejectTopUp = false
	require.NoError(t, s.TopUp(ctx, "topup-rejected", 80))
	require.Equal(t, 80, ledger.held)
	require.NoError(t, db.First(&token, 7).Error)
	require.Equal(t, 120, token.RemainQuota)
}
func TestProductFundingDispatchedHoldIsNeverReclaimedByExpiry(t *testing.T) {
	s, db, ledger := productFundingFixture(t)
	ctx := context.Background()
	require.NoError(t, s.Import(ctx, 42, "payer", "batch"))
	_, err := s.Reserve(ctx, ProductFundingAdmission{CallID: "dispatched", UserID: 42, TokenID: 7, Model: "m", Operation: "chat", PriceSnapshot: `{"price":1}`, Quota: 50})
	require.NoError(t, err)
	require.NoError(t, s.MarkDispatched(ctx, "dispatched"))
	require.NoError(t, db.Model(&ProductRuntimeCall{}).Where("id = ?", "dispatched").Update("dispatch_lease_until", time.Now().Unix()-1).Error)
	require.NoError(t, s.Recover(ctx))
	require.Equal(t, 50, ledger.held)
	var call ProductRuntimeCall
	require.NoError(t, db.First(&call, "id = ?", "dispatched").Error)
	require.Equal(t, "reserved", call.State)
}

func TestProductFundingImportRejectsUndrainedNativeAndOCRJournals(t *testing.T) {
	s, db, ledger := productFundingFixture(t)
	ctx := context.Background()
	require.NoError(t, db.AutoMigrate(&NativeBilling{}, &OCRJob{}))
	require.NoError(t, db.Create(&NativeBilling{ID: "legacy-native", UserID: 42, CallID: "legacy-call", State: "reserved"}).Error)
	require.ErrorIs(t, s.Import(ctx, 42, "payer", "batch"), ErrProductFundingPending)
	require.Zero(t, ledger.imports)
	require.NoError(t, db.Model(&NativeBilling{}).Where("id = ?", "legacy-native").Update("state", "settled").Error)
	require.NoError(t, db.Create(&OCRJob{ID: "legacy-ocr", UserID: 42, IdempotencyKey: "legacy-job", BillingState: "reserved", Funding: "wallet"}).Error)
	require.ErrorIs(t, s.Import(ctx, 42, "payer", "batch"), ErrProductFundingPending)
	require.Zero(t, ledger.imports)
	require.NoError(t, db.Model(&OCRJob{}).Where("id = ?", "legacy-ocr").Update("billing_state", "refunded").Error)
	require.NoError(t, s.Import(ctx, 42, "payer", "batch"))
	require.Equal(t, 1, ledger.imports)
}
