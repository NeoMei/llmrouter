package service

import (
	"context"
	"crypto/sha256"
	"fmt"
	"math"
	"path/filepath"
	"strings"
	"testing"
	"time"

	userkit "github.com/NeoMei/UniHub/packages/userkit-go"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/internal/platformaccount"
	"github.com/QuantumNous/new-api/internal/platformbilling"
	"github.com/QuantumNous/new-api/model"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func setupProductTelemetryReadTest(t *testing.T) (*gorm.DB, string) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "telemetry.db")), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	oldDB, oldLogs := model.DB, model.LOG_DB
	previous, err := platformaccount.Current()
	require.NoError(t, err)
	t.Cleanup(func() { model.DB, model.LOG_DB = oldDB, oldLogs; platformaccount.Configure(previous) })
	model.DB, model.LOG_DB = db, db
	platformaccount.Configure(platformaccount.Config{PublicOrigin: "https://authority.example", ProductID: "llmrouter", InstanceID: "default"})
	t.Setenv("UNIHUB_PRODUCT_RUNTIME_ENABLED", "true")
	t.Setenv("UNIHUB_ORIGIN", "https://authority.example")
	t.Setenv("UNIHUB_PRODUCT_RUNTIME_TOKEN", strings.Repeat("t", 32))
	t.Setenv("UNIHUB_PRODUCT_ID", "llmrouter")
	t.Setenv("UNIHUB_PRODUCT_INSTANCE_ID", "default")
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.Token{}, &model.Log{}, &model.UniHubIdentityMapping{}, &model.ProductFundingAccount{}, &model.ProductRuntimeCall{}, &platformbilling.Call{}, &platformbilling.Attempt{}, &model.SubscriptionPlan{}, &model.UserSubscription{}, &model.SubscriptionPreConsumeRecord{}, &model.ProductSubscriptionPurchase{}))
	require.NoError(t, model.RegisterProductRefundModels(db))
	digest, err := model.ProductRuntimeScopeDigest()
	require.NoError(t, err)
	for _, id := range []int{42, 43} {
		require.NoError(t, db.Create(&model.User{Id: id, Username: fmt.Sprint(id), AffCode: fmt.Sprint(id)}).Error)
		require.NoError(t, db.Create(&model.UniHubIdentityMapping{UserID: id, Issuer: "https://authority.example", Subject: fmt.Sprintf("actor-%d", id), IdentityKey: fmt.Sprint(id)}).Error)
		require.NoError(t, db.Create(&model.ProductFundingAccount{UserID: id, AccountID: fmt.Sprintf("payer-%d", id), ScopeDigest: digest, State: "central", Asset: userkit.RuntimeAsset, UnitVersion: userkit.RuntimeUnitVersion}).Error)
	}
	require.NoError(t, db.Create(&model.Token{Id: 7, UserId: 42, Key: "product", FundingAccountID: "payer-42"}).Error)
	require.NoError(t, db.Create(&model.Token{Id: 8, UserId: 42, Key: "native", AuthRealm: model.NativeFundingLocalOnly}).Error)
	return db, digest
}

func createTelemetryRuntimeCall(t *testing.T, db *gorm.DB, call model.ProductRuntimeCall, modelName, operation string) model.ProductRuntimeCall {
	t.Helper()
	call.ContractDigest = "contract-" + call.ID
	if call.PricingDigest == "" {
		call.PricingDigest = "price-" + call.ID
	}
	reserve, err := common.Marshal(userkit.RuntimeReserveRequest{CallID: call.ID, ParentCallID: call.ParentCallID, LocalUserID: fmt.Sprint(call.UserID), LocalKeyID: fmt.Sprint(call.TokenID), AccountID: call.AccountID, RequestDigest: call.ContractDigest, PricingDigest: call.PricingDigest, Model: modelName, Operation: operation, Asset: userkit.RuntimeAsset, UnitVersion: userkit.RuntimeUnitVersion, PriceVersion: userkit.RuntimeUnitVersion, Amount: fmt.Sprint(call.FinalQuota)})
	require.NoError(t, err)
	call.ReserveJSON = string(reserve)
	require.NoError(t, db.Create(&call).Error)
	return call
}

func TestProductTelemetryLongRangesKeepInclusiveBoundariesOnce(t *testing.T) {
	db, digest := setupProductTelemetryReadTest(t)
	start := int64(1700000000)
	for _, days := range []int64{31, 95} {
		t.Run(fmt.Sprint(days), func(t *testing.T) {
			require.NoError(t, db.Where("1 = 1").Delete(&platformbilling.Call{}).Error)
			require.NoError(t, db.Where("1 = 1").Delete(&model.Log{}).Error)
			require.NoError(t, db.Where("1 = 1").Delete(&model.ProductRuntimeCall{}).Error)
			end := start + days*86400 - 1
			for i, timestamp := range []int64{start, start + 2592000, start + 2592001, end, end + 1} {
				require.NoError(t, db.Create(&platformbilling.Call{CallID: fmt.Sprint(i), ActorID: "actor-42", InstanceID: "default", FundingProductID: "llmrouter", Model: "m", Status: "SETTLED", TelemetryOutcome: "success", CreatedAt: time.Unix(timestamp, 0)}).Error)
			}
			for i, timestamp := range []int64{start, end} {
				id := fmt.Sprintf("product-%d", i)
				createTelemetryRuntimeCall(t, db, model.ProductRuntimeCall{ID: id, UserID: 42, TokenID: 7, AccountID: "payer-42", ScopeDigest: digest, State: "settled", FinalQuota: 5}, "m", "1")
				require.NoError(t, db.Create(&model.Log{RequestId: id, UserId: 42, TokenId: 7, CreatedAt: timestamp, Type: model.LogTypeConsume, Quota: 5, ModelName: "m"}).Error)
			}
			result, err := ReadPlatformTelemetry(context.Background(), platformbilling.TechnicalScope{ActorID: "actor-42", InstanceID: "default"}, 42, "member", PlatformTelemetryFilter{Start: start, End: end})
			require.NoError(t, err)
			assert.Len(t, result.Logs, 6)
			seen := map[string]bool{}
			for _, log := range result.Logs {
				assert.False(t, seen[log.RequestId])
				seen[log.RequestId] = true
			}
			assert.Equal(t, 10, result.Stat.Quota)
			count, quota := 0, 0
			for _, row := range result.QuotaData {
				count += row.Count
				quota += row.Quota
			}
			assert.Equal(t, 6, count)
			assert.Equal(t, 10, quota)
		})
	}
	for _, filter := range []PlatformTelemetryFilter{{Start: -1, End: start}, {Start: start, End: start - 1}, {Start: 1, End: math.MaxInt64}, {Start: 1, End: math.MaxInt64 - 1}} {
		_, err := ReadPlatformTelemetry(context.Background(), platformbilling.TechnicalScope{ActorID: "actor-42", InstanceID: "default"}, 42, "member", filter)
		require.Error(t, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := ReadPlatformTelemetry(ctx, platformbilling.TechnicalScope{ActorID: "actor-42", InstanceID: "default"}, 42, "member", PlatformTelemetryFilter{Start: start, End: start + 95*86400})
	require.ErrorIs(t, err, context.Canceled)
}

func TestProductTelemetryIncludesAppliedAdjustmentsWithoutExtraRequests(t *testing.T) {
	db, digest := setupProductTelemetryReadTest(t)
	now := time.Now().Unix()
	parent := createTelemetryRuntimeCall(t, db, model.ProductRuntimeCall{ID: "parent", UserID: 42, TokenID: 7, AccountID: "payer-42", ScopeDigest: digest, State: "settled", FinalQuota: 50}, "m", "task")
	require.NoError(t, db.Create(&model.Log{RequestId: parent.ID, UserId: 42, TokenId: 7, CreatedAt: now - 5, Type: model.LogTypeConsume, Quota: 50, PromptTokens: 10, CompletionTokens: 5, ModelName: "m"}).Error)
	child := createTelemetryRuntimeCall(t, db, model.ProductRuntimeCall{ID: "adj_plus", ParentCallID: parent.ID, UserID: 42, TokenID: 7, AccountID: parent.AccountID, ScopeDigest: digest, State: "settled", FinalQuota: 30, PricingDigest: parent.PricingDigest, ExternalCounters: true, FinancialCommitment: true}, "m", "task")
	require.NoError(t, db.Create(&model.ProductCallChargeSegment{CallID: child.ID, ParentCallID: parent.ID}).Error)
	for _, entry := range []struct {
		id, state string
		delta     int
	}{{"plus", "applied", 30}, {"minus", "applied", -20}, {"pending", "pending", 10}, {"forged", "applied", 9}} {
		kind, quota := model.LogTypeConsume, entry.delta
		if quota < 0 {
			kind, quota = model.LogTypeRefund, -quota
		}
		log := model.Log{RequestId: entry.id, UserId: 42, TokenId: 7, CreatedAt: now - 2, Type: kind, Quota: quota, ModelName: "m", Other: `{"product_call_id":"parent"}`}
		raw, err := common.Marshal(log)
		require.NoError(t, err)
		op := model.ProductCallAdjustment{ID: entry.id, ParentCallID: parent.ID, State: entry.state, Delta: entry.delta, BaseQuota: 50, ActualQuota: 50 + entry.delta, LogJSON: string(raw)}
		if entry.id == "plus" {
			op.ChildCallID = child.ID
		}
		require.NoError(t, db.Create(&op).Error)
		if entry.id == "forged" {
			log.Quota = 99
		}
		require.NoError(t, db.Create(&log).Error)
	}
	require.NoError(t, db.Create(&model.ProductCallRefund{ID: "refund-minus", CallID: child.ID, RequestKey: "minus", Amount: 20, State: "applied"}).Error)
	require.NoError(t, db.Create(&model.Log{RequestId: "native-with-hint", UserId: 42, TokenId: 7, CreatedAt: now, Type: model.LogTypeConsume, Quota: 999, Other: `{"product_call_id":"parent"}`}).Error)
	result, err := ReadPlatformTelemetry(context.Background(), platformbilling.TechnicalScope{ActorID: "actor-42", InstanceID: "default"}, 42, "member", PlatformTelemetryFilter{Start: now - 60, End: now + 1})
	require.NoError(t, err)
	assert.Len(t, result.Logs, 3)
	assert.Equal(t, model.Stat{Quota: 60, Rpm: 1, Tpm: 15}, result.Stat)
	require.Len(t, result.QuotaData, 1)
	assert.Equal(t, 60, result.QuotaData[0].Quota)
	assert.Equal(t, 1, result.QuotaData[0].Count)
	for _, log := range result.Logs {
		assert.NotContains(t, []string{"pending", "forged", "native-with-hint"}, log.RequestId)
	}
	var changed userkit.RuntimeReserveRequest
	require.NoError(t, common.UnmarshalJsonStr(child.ReserveJSON, &changed))
	changed.PricingDigest = "another-price"
	raw, err := common.Marshal(changed)
	require.NoError(t, err)
	require.NoError(t, db.Model(&model.ProductRuntimeCall{}).Where("id = ?", child.ID).Updates(map[string]any{"pricing_digest": changed.PricingDigest, "reserve_json": string(raw)}).Error)
	result, err = ReadPlatformTelemetry(context.Background(), platformbilling.TechnicalScope{ActorID: "actor-42", InstanceID: "default"}, 42, "member", PlatformTelemetryFilter{Start: now - 60, End: now + 1, RequestID: "plus"})
	require.NoError(t, err)
	assert.Empty(t, result.Logs, "a child cannot select a different price while retaining its parent's identity")
}

func TestProductTelemetryIncludesCentrallyPurchasedSubscriptionUsage(t *testing.T) {
	db, digest := setupProductTelemetryReadTest(t)
	now := time.Now().Unix()
	plan := model.SubscriptionPlan{Id: 1, Title: "central-plan"}
	raw, err := common.Marshal(plan)
	require.NoError(t, err)
	price := fmt.Sprintf("%x", sha256.Sum256(raw))
	call := createTelemetryRuntimeCall(t, db, model.ProductRuntimeCall{ID: "purchase", UserID: 42, AccountID: "payer-42", ScopeDigest: digest, State: "settled", FinalQuota: 20, PricingDigest: price, ExternalCounters: true, FinancialCommitment: true}, "subscription:1", "subscription_purchase")
	require.NoError(t, db.Create(&model.UserSubscription{Id: 101, UserId: 42, PlanId: 1, Status: "expired", Source: model.PaymentMethodBalance}).Error)
	require.NoError(t, db.Create(&model.UserSubscription{Id: 102, UserId: 42, PlanId: 1, Status: "active"}).Error)
	require.NoError(t, db.Create(&model.ProductSubscriptionPurchase{ID: call.ID, UserID: 42, PlanID: 1, AccountID: call.AccountID, State: "applied", SubscriptionID: 101, RequiredQuota: 20, PlanSnapshot: string(raw)}).Error)
	for _, entry := range []struct {
		id, status string
		sub, token int
	}{{"subscription-use", "consumed", 101, 7}, {"native-subscription", "consumed", 102, 8}, {"old-subscription-product-key", "consumed", 102, 7}, {"refunded-success", "refunded", 101, 7}, {"native-key", "consumed", 101, 8}} {
		require.NoError(t, db.Create(&model.SubscriptionPreConsumeRecord{RequestId: entry.id, UserId: 42, UserSubscriptionId: entry.sub, PreConsumed: 10, Status: entry.status}).Error)
		require.NoError(t, db.Create(&model.Log{RequestId: entry.id, UserId: 42, TokenId: entry.token, CreatedAt: now, Type: model.LogTypeConsume, Quota: 7, PromptTokens: 2, CompletionTokens: 3, ModelName: "m", Other: `{"subscription_id":101}`}).Error)
	}
	result, err := ReadPlatformTelemetry(context.Background(), platformbilling.TechnicalScope{ActorID: "actor-42", InstanceID: "default"}, 42, "member", PlatformTelemetryFilter{Start: now - 60, End: now + 1})
	require.NoError(t, err)
	require.Len(t, result.Logs, 1)
	assert.Equal(t, "subscription-use", result.Logs[0].RequestId)
	assert.Equal(t, model.Stat{Quota: 7, Rpm: 1, Tpm: 5}, result.Stat)
	var other map[string]any
	require.NoError(t, common.UnmarshalJsonStr(result.Logs[0].Other, &other))
	assert.Equal(t, "subscription", other["billing_source"])
	assert.Equal(t, "purchase", other["purchase_call_id"])
	for _, tc := range []struct {
		name            string
		record          any
		id              any
		column          string
		value, original any
	}{
		{"pending_purchase", &model.ProductSubscriptionPurchase{}, call.ID, "state", "pending", "applied"},
		{"other_payer", &model.ProductSubscriptionPurchase{}, call.ID, "account_id", "another", call.AccountID},
		{"wrong_price", &model.ProductSubscriptionPurchase{}, call.ID, "required_quota", 21, 20},
		{"wrong_plan", &model.UserSubscription{}, 101, "plan_id", 99, 1},
		{"native_entitlement", &model.UserSubscription{}, 101, "source", "admin", model.PaymentMethodBalance},
		{"other_deployment", &model.ProductRuntimeCall{}, call.ID, "scope_digest", "another", digest},
		{"unsettled_purchase", &model.ProductRuntimeCall{}, call.ID, "state", "reserved", "settled"},
		{"other_key_owner", &model.Token{}, 7, "user_id", 43, 42},
		{"other_key_payer", &model.Token{}, 7, "funding_account_id", "another", "payer-42"},
		{"native_owner", &model.User{}, 42, "funding_realm", model.NativeFundingLocalOnly, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.NoError(t, db.Model(tc.record).Where("id = ?", tc.id).Update(tc.column, tc.value).Error)
			t.Cleanup(func() {
				require.NoError(t, db.Model(tc.record).Where("id = ?", tc.id).Update(tc.column, tc.original).Error)
			})
			result, err := ReadPlatformTelemetry(context.Background(), platformbilling.TechnicalScope{ActorID: "actor-42", InstanceID: "default"}, 42, "member", PlatformTelemetryFilter{Start: now - 60, End: now + 1})
			require.NoError(t, err)
			assert.Empty(t, result.Logs)
		})
	}
}

func TestProductSubscriptionTelemetrySurvivesIdempotencyCleanup(t *testing.T) {
	db, digest := setupProductTelemetryReadTest(t)
	now := time.Now().Unix()
	old := now - 8*24*3600
	plan := model.SubscriptionPlan{Id: 1, Title: "central-plan"}
	raw, err := common.Marshal(plan)
	require.NoError(t, err)
	call := createTelemetryRuntimeCall(t, db, model.ProductRuntimeCall{ID: "retained-purchase", UserID: 42, AccountID: "payer-42", ScopeDigest: digest, State: "settled", FinalQuota: 20, PricingDigest: fmt.Sprintf("%x", sha256.Sum256(raw)), ExternalCounters: true, FinancialCommitment: true}, "subscription:1", "subscription_purchase")
	for _, sub := range []model.UserSubscription{
		{Id: 101, UserId: 42, PlanId: 1, Status: "expired", Source: model.PaymentMethodBalance},
		{Id: 102, UserId: 42, PlanId: 1, Status: "active", Source: "admin"},
		{Id: 103, UserId: 42, PlanId: 1, Status: "active", Source: model.PaymentMethodBalance},
		{Id: 104, UserId: 42, PlanId: 1, Status: "expired", Source: model.PaymentMethodBalance},
	} {
		require.NoError(t, db.Create(&sub).Error)
	}
	for _, purchase := range []model.ProductSubscriptionPurchase{
		{ID: call.ID, UserID: 42, PlanID: 1, AccountID: call.AccountID, State: "applied", SubscriptionID: 101, RequiredQuota: 20, PlanSnapshot: string(raw)},
		{ID: "unpaid-purchase", UserID: 42, PlanID: 1, AccountID: call.AccountID, State: "pending", SubscriptionID: 103, RequiredQuota: 20, PlanSnapshot: string(raw)},
		{ID: "missing-financial-proof", UserID: 42, PlanID: 1, AccountID: call.AccountID, State: "applied", SubscriptionID: 104, RequiredQuota: 20, PlanSnapshot: string(raw)},
	} {
		require.NoError(t, db.Create(&purchase).Error)
	}
	for _, entry := range []struct {
		id      string
		sub     int
		updated int64
	}{{"central-old", 101, old}, {"native-old", 102, old}, {"pending-old", 103, old}, {"native-recent", 102, now}, {"untrusted-applied-old", 104, old}} {
		require.NoError(t, db.Create(&model.SubscriptionPreConsumeRecord{RequestId: entry.id, UserId: 42, UserSubscriptionId: entry.sub, PreConsumed: 10, Status: "consumed"}).Error)
		require.NoError(t, db.Model(&model.SubscriptionPreConsumeRecord{}).Where("request_id = ?", entry.id).UpdateColumn("updated_at", entry.updated).Error)
		require.NoError(t, db.Create(&model.Log{RequestId: entry.id, UserId: 42, TokenId: 7, CreatedAt: entry.updated, Type: model.LogTypeConsume, Quota: 7, ModelName: "m"}).Error)
	}
	before, err := ReadPlatformTelemetry(context.Background(), platformbilling.TechnicalScope{ActorID: "actor-42", InstanceID: "default"}, 42, "member", PlatformTelemetryFilter{Start: now - 30*24*3600, End: now})
	require.NoError(t, err)
	require.Len(t, before.Logs, 1)
	assert.Equal(t, "central-old", before.Logs[0].RequestId)
	removed, err := model.CleanupSubscriptionPreConsumeRecords(7 * 24 * 3600)
	require.NoError(t, err)
	assert.EqualValues(t, 2, removed, "old native/unpaid idempotency records still expire")
	var records []model.SubscriptionPreConsumeRecord
	require.NoError(t, db.Find(&records).Error)
	ids := make([]string, 0, len(records))
	for _, record := range records {
		ids = append(ids, record.RequestId)
	}
	assert.ElementsMatch(t, []string{"central-old", "native-recent", "untrusted-applied-old"}, ids)
	after, err := ReadPlatformTelemetry(context.Background(), platformbilling.TechnicalScope{ActorID: "actor-42", InstanceID: "default"}, 42, "member", PlatformTelemetryFilter{Start: now - 30*24*3600, End: now})
	require.NoError(t, err)
	require.Len(t, after.Logs, 1, "routine cleanup must retain expired central entitlement history")
	assert.Equal(t, "central-old", after.Logs[0].RequestId)
	assert.Equal(t, 7, after.Stat.Quota)
	require.Len(t, after.QuotaData, 1)
	assert.Equal(t, 1, after.QuotaData[0].Count)
}
