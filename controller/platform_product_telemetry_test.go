package controller

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	userkit "github.com/NeoMei/UniHub/packages/userkit-go"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/internal/platformaccount"
	"github.com/QuantumNous/new-api/internal/platformbilling"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service/authz"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestProductRuntimeUsageAppearsInOriginalLogsAndDashboard(t *testing.T) {
	previousDB, previousLogs := model.DB, model.LOG_DB
	t.Cleanup(func() { model.DB, model.LOG_DB = previousDB, previousLogs })
	db := setupModelListControllerTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.UniHubIdentityMapping{}, &model.ProductFundingAccount{}, &model.ProductRuntimeCall{}, &platformbilling.Call{}, &platformbilling.Attempt{}))
	logsDB, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "logs.db")), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, logsDB.AutoMigrate(&model.Log{}))
	sqlLogs, err := logsDB.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sqlLogs.Close()) })
	model.LOG_DB = logsDB

	previous, err := platformaccount.Current()
	require.NoError(t, err)
	t.Cleanup(func() { platformaccount.Configure(previous) })
	platformaccount.Configure(platformaccount.Config{PublicOrigin: "https://authority.example", ProductID: "llmrouter", InstanceID: "default"})
	t.Setenv("UNIHUB_PRODUCT_RUNTIME_ENABLED", "true")
	t.Setenv("UNIHUB_ORIGIN", "https://authority.example")
	t.Setenv("UNIHUB_PRODUCT_RUNTIME_TOKEN", strings.Repeat("t", 32))
	t.Setenv("UNIHUB_PRODUCT_ID", "llmrouter")
	t.Setenv("UNIHUB_PRODUCT_INSTANCE_ID", "default")
	scope, err := model.ProductRuntimeScopeDigest()
	require.NoError(t, err)
	for id := 1; id <= 3; id++ {
		user := model.User{Id: id, Username: fmt.Sprintf("product-user-%d", id), AffCode: fmt.Sprintf("aff-%d", id), AuthVersion: 1, Status: common.UserStatusEnabled}
		if id == 3 {
			user.FundingRealm, user.FundingRealmEvidence, user.NativePassword = model.NativeFundingLocalOnly, "reviewed", true
		}
		require.NoError(t, db.Create(&user).Error)
		require.NoError(t, db.Create(&model.UniHubIdentityMapping{UserID: id, Issuer: "https://authority.example", Subject: fmt.Sprintf("actor-%d", id), IdentityKey: fmt.Sprintf("identity-%d", id)}).Error)
		if id != 3 {
			require.NoError(t, db.Create(&model.ProductFundingAccount{UserID: id, AccountID: fmt.Sprintf("payer-%d", id), ScopeDigest: scope, State: "central", Asset: userkit.RuntimeAsset, UnitVersion: userkit.RuntimeUnitVersion}).Error)
		}
	}
	now := time.Now().Unix()
	for _, entry := range []struct {
		id, account, deployment string
		user, quota             int
	}{
		{"runtime-1", "payer-1", scope, 1, 30},
		{"runtime-2", "payer-1", scope, 1, 20},
		{"other-user", "payer-2", scope, 2, 40},
		{"wrong-account", "another-payer", scope, 1, 999},
		{"wrong-deployment", "payer-1", "another-deployment", 1, 999},
	} {
		reserve, marshalErr := common.Marshal(userkit.RuntimeReserveRequest{CallID: entry.id, LocalUserID: fmt.Sprint(entry.user), LocalKeyID: "11", AccountID: entry.account, RequestDigest: "contract-" + entry.id, Model: "original-model", Operation: "1", Asset: userkit.RuntimeAsset, UnitVersion: userkit.RuntimeUnitVersion, PriceVersion: userkit.RuntimeUnitVersion, PricingDigest: "price-" + entry.id, Amount: fmt.Sprint(entry.quota)})
		require.NoError(t, marshalErr)
		require.NoError(t, db.Create(&model.ProductRuntimeCall{ID: entry.id, UserID: entry.user, TokenID: 11, AccountID: entry.account, ScopeDigest: entry.deployment, ReserveJSON: string(reserve), ContractDigest: "contract-" + entry.id, PricingDigest: "price-" + entry.id, State: "settled", ReservedQuota: entry.quota, FinalQuota: entry.quota, CreatedAt: now - 10, DispatchedAt: now - 9}).Error)
	}
	for _, log := range []model.Log{
		{UserId: 1, TokenId: 11, RequestId: "runtime-1", Username: "product-user", TokenName: "application", ModelName: "product-a", Type: model.LogTypeConsume, Quota: 30, PromptTokens: 10, CompletionTokens: 4, Group: "default", ChannelId: 7, CreatedAt: now - 3, UpstreamRequestId: "upstream-1", Other: `{"model_ratio":2,"admin_info":{"secret":"hidden-admin"},"audit_info":{"secret":"hidden-audit"},"po":{"secret":"hidden-override"}}`},
		{UserId: 1, TokenId: 11, RequestId: "runtime-2", Username: "product-user", TokenName: "application", ModelName: "product-b", Type: model.LogTypeConsume, Quota: 20, PromptTokens: 20, CompletionTokens: 6, Group: "vip", ChannelId: 8, CreatedAt: now - 2},
		{UserId: 1, TokenId: 99, RequestId: "old-native", Username: "product-user", Type: model.LogTypeConsume, Quota: 999, CreatedAt: now},
		{UserId: 1, TokenId: 99, RequestId: "runtime-1", ModelName: "wrong-token", Type: model.LogTypeConsume, Quota: 999, CreatedAt: now},
		{UserId: 2, TokenId: 11, RequestId: "runtime-1", ModelName: "wrong-owner", Type: model.LogTypeConsume, Quota: 999, CreatedAt: now},
		{UserId: 2, TokenId: 11, RequestId: "other-user", ModelName: "another-actor", Type: model.LogTypeConsume, Quota: 40, CreatedAt: now},
		{UserId: 1, TokenId: 11, RequestId: "wrong-account", ModelName: "account-private", Type: model.LogTypeConsume, Quota: 999, CreatedAt: now},
		{UserId: 1, TokenId: 11, RequestId: "wrong-deployment", ModelName: "deployment-private", Type: model.LogTypeConsume, Quota: 999, CreatedAt: now},
		{UserId: 3, TokenId: 11, RequestId: "shared-native", ModelName: "native-owner-private", Type: model.LogTypeConsume, Quota: 999, CreatedAt: now},
	} {
		require.NoError(t, logsDB.Create(&log).Error)
	}
	for _, call := range []platformbilling.Call{
		{CallID: "legacy-key", ActorID: "actor-1", AccountID: "payer-1", FundingProductID: "llmrouter", InstanceID: "default", Model: "legacy-model", Asset: "CNY/6", Charged: "999999999999999999", Status: "SETTLED", TelemetryOutcome: "success", Intent: `{"usage":{"input":"7","output":"3","cache_read":"0","cache_write":"0","reasoning":"0"}}`, CreatedAt: time.Unix(now-1, 0)},
		{CallID: "runtime-1", ActorID: "actor-1", FundingProductID: "llmrouter", InstanceID: "default", Model: "duplicate-platform-row", Status: "SETTLED", TelemetryOutcome: "success", CreatedAt: time.Unix(now-3, 0)},
	} {
		require.NoError(t, db.Create(&call).Error)
	}

	r := gin.New()
	actorID := 1
	permissions := []string{"product:use"}
	r.Use(func(c *gin.Context) {
		c.Set("id", actorID)
		c.Set("username", "product-user")
		authz.SetProductPrincipal(c, userkit.Principal{LocalUser: userkit.LocalUser{ID: fmt.Sprint(actorID)}, Access: userkit.Access{Issuer: "https://authority.example", Subject: fmt.Sprintf("actor-%d", actorID), ProductID: "llmrouter", InstanceID: "default", Active: true, ExpiresAt: now + 3600, Permissions: permissions}})
	})
	r.GET("/api/log/self", GetUserLogs)
	r.GET("/api/log/self/stat", GetLogsSelfStat)
	r.GET("/api/data/self", GetUserQuotaDates)
	r.GET("/api/data/flow/self", GetUserFlowQuotaDates)
	r.GET("/api/log/", GetAllLogs)
	r.GET("/api/data/", GetAllQuotaDates)
	query := fmt.Sprintf("?start_timestamp=%d&end_timestamp=%d", now-60, now+1)
	request := func(path, extra string, out any) {
		recorder := httptest.NewRecorder()
		r.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path+query+extra, nil))
		require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
		require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), out))
		for _, hidden := range []string{"old-native", "wrong-token", "wrong-owner", "account-private", "deployment-private", "native-owner-private", "hidden-admin", "hidden-audit", "hidden-override", "999999999999999999", "duplicate-platform-row"} {
			assert.NotContains(t, recorder.Body.String(), hidden)
		}
	}
	var logs struct {
		Success bool `json:"success"`
		Data    struct {
			Total int         `json:"total"`
			Items []model.Log `json:"items"`
		} `json:"data"`
	}
	request("/api/log/self", "&p=2&page_size=1", &logs)
	require.True(t, logs.Success)
	assert.Equal(t, 3, logs.Data.Total)
	require.Len(t, logs.Data.Items, 1)
	assert.Equal(t, "runtime-2", logs.Data.Items[0].RequestId)
	assert.Equal(t, 20, logs.Data.Items[0].Quota)
	for _, filter := range []string{"&model_name=product-a", "&token_name=application&group=default", "&request_id=runtime-1", "&upstream_request_id=upstream-1", "&channel=7"} {
		request("/api/log/self", filter, &logs)
		assert.Equal(t, 1, logs.Data.Total, filter)
		require.Len(t, logs.Data.Items, 1)
		assert.Equal(t, "runtime-1", logs.Data.Items[0].RequestId)
		assert.Equal(t, 30, logs.Data.Items[0].Quota)
		var other map[string]any
		require.NoError(t, common.UnmarshalJsonStr(logs.Data.Items[0].Other, &other))
		assert.Equal(t, float64(2), other["model_ratio"])
		assert.Equal(t, userkit.RuntimeAsset, other["central_asset"])
		assert.Equal(t, userkit.RuntimeUnitVersion, other["central_unit_version"])
	}
	var stat struct {
		Success bool       `json:"success"`
		Data    model.Stat `json:"data"`
	}
	request("/api/log/self/stat", "", &stat)
	require.True(t, stat.Success)
	assert.Equal(t, model.Stat{Quota: 50, Rpm: 3, Tpm: 50}, stat.Data)
	var dates struct {
		Success bool              `json:"success"`
		Data    []model.QuotaData `json:"data"`
	}
	request("/api/data/self", "", &dates)
	require.True(t, dates.Success)
	count, quota, tokens := 0, 0, 0
	for _, row := range dates.Data {
		count += row.Count
		quota += row.Quota
		tokens += row.TokenUsed
	}
	assert.Equal(t, 3, count)
	assert.Equal(t, 50, quota)
	assert.Equal(t, 50, tokens)
	var flow struct {
		Success bool                  `json:"success"`
		Data    []model.FlowQuotaData `json:"data"`
	}
	request("/api/data/flow/self", "&model_name=product-a", &flow)
	require.True(t, flow.Success)
	require.Len(t, flow.Data, 1)
	assert.Equal(t, 11, flow.Data[0].TokenID)
	assert.Equal(t, "application", flow.Data[0].TokenName)
	assert.Equal(t, 30, flow.Data[0].Quota)
	assert.Equal(t, 1, flow.Data[0].Count)

	permissions = []string{"product:use", "log:read"}
	request("/api/log/", "&username=actor-2", &logs)
	require.True(t, logs.Success)
	assert.Equal(t, 1, logs.Data.Total)
	require.Len(t, logs.Data.Items, 1)
	assert.Equal(t, "other-user", logs.Data.Items[0].RequestId)
	assert.Equal(t, 40, logs.Data.Items[0].Quota)
	request("/api/data/", "&username=actor-2", &dates)
	require.True(t, dates.Success)
	require.Len(t, dates.Data, 1)
	assert.Equal(t, 40, dates.Data[0].Quota)
	assert.Equal(t, 1, dates.Data[0].Count)

	require.NoError(t, logsDB.Create(&model.Log{UserId: 1, TokenId: 11, RequestId: "runtime-2", ModelName: "product-b", Type: model.LogTypeError, CreatedAt: now}).Error)
	request("/api/log/self", "&type=5", &logs)
	assert.Equal(t, 1, logs.Data.Total)
	require.Len(t, logs.Data.Items, 1)
	assert.Equal(t, model.LogTypeError, logs.Data.Items[0].Type)
	request("/api/log/self/stat", "", &stat)
	assert.Equal(t, model.Stat{Quota: 50, Rpm: 3, Tpm: 50}, stat.Data, "an error attempt must not duplicate the final request charge or count")

	require.NoError(t, db.Model(&model.ProductRuntimeCall{}).Where("id = ?", "runtime-2").Update("reserve_json", `{"call_id":"runtime-2","local_user_id":"another-owner","local_key_id":"11","account_id":"payer-1","asset":"LR_QUOTA/0","unit_version":"lr-quota-v1"}`).Error)
	request("/api/log/self", "&request_id=runtime-2", &logs)
	assert.Zero(t, logs.Data.Total, "inconsistent durable admission cannot authorize a product log")

	// The funding-account row remains an LR_QUOTA cutover receipt. New cash
	// calls carry their own frozen contract while original logs keep LR units.
	t.Setenv("UNIHUB_PRODUCT_FUNDING_MODE", "unified_cash")
	require.NoError(t, db.AutoMigrate(&model.Token{}))
	require.NoError(t, model.RegisterProductFundingModels(db))
	require.NoError(t, db.Create(&model.Token{Id: 11, UserId: 1, Key: "product-key", Status: common.TokenStatusEnabled, ExpiredTime: -1, RemainQuota: 100}).Error)
	central := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var reserve userkit.RuntimeReserveRequest
		if err := common.DecodeJson(r.Body, &reserve); !assert.NoError(t, err) {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		raw, marshalErr := common.Marshal(userkit.RuntimeCall{RuntimeReserveRequest: reserve, ProductID: "llmrouter", InstanceID: "default", ActorID: "actor-1", Status: "RESERVED", Held: reserve.Amount, Charged: "0"})
		if !assert.NoError(t, marshalErr) {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, writeErr := w.Write(raw)
		assert.NoError(t, writeErr)
	}))
	t.Cleanup(central.Close)
	client := central.Client()
	transport := client.Transport.(*http.Transport).Clone()
	transport.TLSClientConfig = transport.TLSClientConfig.Clone()
	transport.TLSClientConfig.ServerName = "example.com"
	transport.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, central.Listener.Addr().String())
	}
	client.Transport = transport
	runtime, err := userkit.NewRuntimeClient(userkit.ServiceConfig{BaseURL: "https://authority.example", ProductID: "llmrouter", InstanceID: "default", ServiceToken: strings.Repeat("t", 32), HTTPClient: client})
	require.NoError(t, err)
	cash, err := model.NewProductFundingStore(db, runtime).Reserve(context.Background(), model.ProductFundingAdmission{CallID: "cash-key", UserID: 1, TokenID: 11, Model: "original-model", Operation: "1", PriceSnapshot: `{"product_price":"frozen"}`, Quota: 7})
	require.NoError(t, err)
	require.NoError(t, db.Model(&cash).Updates(map[string]any{"state": "settled", "final_quota": 7}).Error)
	require.NoError(t, logsDB.Create(&model.Log{UserId: 1, TokenId: 11, RequestId: cash.ID, ModelName: "cash-model", TokenName: "application", Type: model.LogTypeConsume, Quota: 7, PromptTokens: 2, CompletionTokens: 1, Group: "default", CreatedAt: now}).Error)
	request("/api/log/self", "&request_id=cash-key", &logs)
	require.True(t, logs.Success)
	assert.Equal(t, 1, logs.Data.Total)
	require.Len(t, logs.Data.Items, 1)
	assert.Equal(t, 7, logs.Data.Items[0].Quota, "the 103 micro-CNY debit must not replace original LR quota")
	var cashOther map[string]any
	require.NoError(t, common.UnmarshalJsonStr(logs.Data.Items[0].Other, &cashOther))
	assert.Equal(t, "CNY/6", cashOther["central_asset"])
	assert.Equal(t, "lr-cny-v1", cashOther["central_unit_version"])
	request("/api/log/self/stat", "&request_id=cash-key", &stat)
	assert.Equal(t, model.Stat{Quota: 7, Rpm: 1, Tpm: 3}, stat.Data)
	request("/api/data/self", "&request_id=cash-key", &dates)
	require.Len(t, dates.Data, 1)
	assert.Equal(t, 7, dates.Data[0].Quota)
	assert.Equal(t, 1, dates.Data[0].Count)
	// A centrally bought entitlement retains original subscription usage. Its
	// purchase charge is not charged or counted again for each model request.
	require.NoError(t, db.AutoMigrate(&model.SubscriptionPlan{}, &model.UserSubscription{}, &model.SubscriptionPreConsumeRecord{}, &model.ProductSubscriptionPurchase{}))
	plan := model.SubscriptionPlan{Id: 66, Title: "cash-paid entitlement"}
	planRaw, err := common.Marshal(plan)
	require.NoError(t, err)
	purchase, err := model.NewProductFundingStore(db, runtime).Reserve(context.Background(), model.ProductFundingAdmission{CallID: "cash-subscription-purchase", UserID: 1, Model: "subscription:66", Operation: "subscription_purchase", PriceSnapshot: string(planRaw), Quota: 7, ExternalCounters: true, FinancialCommitment: true})
	require.NoError(t, err)
	require.NoError(t, db.Model(&purchase).Updates(map[string]any{"state": "settled", "final_quota": 7}).Error)
	require.NoError(t, db.Create(&model.UserSubscription{Id: 501, UserId: 1, PlanId: 66, Status: "expired", Source: model.PaymentMethodBalance}).Error)
	require.NoError(t, db.Create(&model.ProductSubscriptionPurchase{ID: purchase.ID, UserID: 1, PlanID: 66, AccountID: purchase.AccountID, State: "applied", SubscriptionID: 501, RequiredQuota: 7, PlanSnapshot: string(planRaw)}).Error)
	require.NoError(t, db.Create(&model.SubscriptionPreConsumeRecord{RequestId: "cash-subscription-use", UserId: 1, UserSubscriptionId: 501, PreConsumed: 5, Status: "consumed"}).Error)
	require.NoError(t, logsDB.Create(&model.Log{RequestId: "cash-subscription-use", UserId: 1, TokenId: 11, Type: model.LogTypeConsume, Quota: 3, PromptTokens: 1, CompletionTokens: 1, CreatedAt: now, ModelName: "subscription-model"}).Error)
	request("/api/log/self", "&request_id=cash-subscription-use", &logs)
	require.True(t, logs.Success)
	require.Len(t, logs.Data.Items, 1)
	assert.Equal(t, 3, logs.Data.Items[0].Quota)
	var subscriptionOther map[string]any
	require.NoError(t, common.UnmarshalJsonStr(logs.Data.Items[0].Other, &subscriptionOther))
	assert.Equal(t, "subscription", subscriptionOther["billing_source"])
	assert.Equal(t, purchase.ID, subscriptionOther["purchase_call_id"])
	assert.Equal(t, userkit.RuntimeAsset, subscriptionOther["central_asset"])
	request("/api/log/self/stat", "&request_id=cash-subscription-use", &stat)
	assert.Equal(t, model.Stat{Quota: 3, Rpm: 1, Tpm: 2}, stat.Data)
	require.NoError(t, db.Model(&model.ProductRuntimeCall{}).Where("id = ?", cash.ID).Update("conversion_numerator", "74").Error)
	request("/api/log/self", "&request_id=cash-key", &logs)
	assert.Zero(t, logs.Data.Total, "a changed conversion snapshot must fail closed")

	for i := 1; i <= 2; i++ {
		id := fmt.Sprintf("large-%d", i)
		reserve, marshalErr := common.Marshal(userkit.RuntimeReserveRequest{CallID: id, LocalUserID: "1", LocalKeyID: "11", AccountID: "payer-1", Asset: userkit.RuntimeAsset, UnitVersion: userkit.RuntimeUnitVersion, PriceVersion: userkit.RuntimeUnitVersion, RequestDigest: id, PricingDigest: "price", Amount: "1500000000", Model: "large-total", Operation: "1"})
		require.NoError(t, marshalErr)
		require.NoError(t, db.Create(&model.ProductRuntimeCall{ID: id, UserID: 1, TokenID: 11, AccountID: "payer-1", ScopeDigest: scope, ContractDigest: id, PricingDigest: "price", ReserveJSON: string(reserve), State: "settled", FinalQuota: 1500000000}).Error)
		require.NoError(t, logsDB.Create(&model.Log{UserId: 1, TokenId: 11, RequestId: id, ModelName: "large-total", Type: model.LogTypeConsume, Quota: 1500000000, CreatedAt: now}).Error)
	}
	request("/api/log/self/stat", "&model_name=large-total", &stat)
	assert.Equal(t, int64(3000000000), int64(stat.Data.Quota), "a read total is not limited to a single charge's int32 capacity")
	request("/api/data/self", "&model_name=large-total", &dates)
	require.Len(t, dates.Data, 1)
	assert.Equal(t, int64(3000000000), int64(dates.Data[0].Quota))
	request("/api/data/flow/self", "&model_name=large-total", &flow)
	require.Len(t, flow.Data, 1)
	assert.Equal(t, int64(3000000000), int64(flow.Data[0].Quota))

	actorID = 3
	request("/api/log/self", "&actor_id=actor-1&account_id=payer-1&user_id=1", &logs)
	assert.Zero(t, logs.Data.Total, "a shared native owner with no central runtime calls must not inherit local logs")
	assert.Empty(t, logs.Data.Items)
}
