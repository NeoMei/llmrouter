package service

import (
	"context"
	"encoding/json"
	userkit "github.com/NeoMei/UniHub/packages/userkit-go"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

type protocolLedger struct {
	available, consumed int
	calls               map[string]userkit.RuntimeCall
	refunds             map[string]bool
}

func (f *protocolLedger) serve(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/internal/v1/product-runtime")
	var out any
	switch {
	case path == "/calls/reserve":
		var in userkit.RuntimeReserveRequest
		_ = json.NewDecoder(r.Body).Decode(&in)
		call, ok := f.calls[in.CallID]
		if !ok {
			n, _ := strconv.Atoi(in.Amount)
			f.available -= n
			call = userkit.RuntimeCall{RuntimeReserveRequest: in, ProductID: "llmrouter", InstanceID: "default", Held: in.Amount, Status: "RESERVED"}
			f.calls[in.CallID] = call
		}
		out = call
	case strings.HasSuffix(path, "/settle"):
		id := strings.TrimSuffix(strings.TrimPrefix(path, "/calls/"), "/settle")
		call := f.calls[id]
		var in userkit.RuntimeSettlementRequest
		_ = json.NewDecoder(r.Body).Decode(&in)
		if call.Status != "SETTLED" {
			held, _ := strconv.Atoi(call.Held)
			n, _ := strconv.Atoi(in.Amount)
			f.available += held - n
			f.consumed += n
			call.Status = "SETTLED"
			call.Charged = in.Amount
			f.calls[id] = call
		}
		out = call
	case strings.HasSuffix(path, "/topup"):
		id := strings.TrimSuffix(strings.TrimPrefix(path, "/calls/"), "/topup")
		call := f.calls[id]
		var in userkit.RuntimeTopUpRequest
		_ = json.NewDecoder(r.Body).Decode(&in)
		n, _ := strconv.Atoi(in.Amount)
		h, _ := strconv.Atoi(call.Held)
		f.available -= n
		call.Held = strconv.Itoa(h + n)
		f.calls[id] = call
		out = call
	case strings.HasSuffix(path, "/refund"):
		id := strings.TrimSuffix(strings.TrimPrefix(path, "/calls/"), "/refund")
		var in userkit.RuntimeRefundRequest
		_ = json.NewDecoder(r.Body).Decode(&in)
		key := id + in.RequestKey
		n, _ := strconv.Atoi(in.Amount)
		if !f.refunds[key] {
			f.refunds[key] = true
			f.available += n
			f.consumed -= n
		}
		out = userkit.RuntimeAdjustment{ProductID: "llmrouter", InstanceID: "default", AccountID: "payer", Amount: in.Amount, Status: "APPLIED"}
	case strings.Contains(path, "/balance"):
		out = userkit.RuntimeBalance{ProductID: "llmrouter", InstanceID: "default", AccountID: "payer", Asset: userkit.RuntimeAsset, UnitVersion: userkit.RuntimeUnitVersion, Available: strconv.Itoa(f.available)}
	case strings.HasPrefix(path, "/calls/"):
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
	_ = json.NewEncoder(w).Encode(out)
}
func protocolFixture(t *testing.T) (*gin.Context, *relaycommon.RelayInfo, *protocolLedger) {
	t.Helper()
	db, e := gorm.Open(sqlite.Open(t.TempDir()+"/protocol.db"), &gorm.Config{})
	require.NoError(t, e)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.Token{}, &model.Channel{}, &model.Log{}, &model.QuotaData{}, &model.Task{}, &model.Midjourney{}, &model.UserSubscription{}, &model.SubscriptionPlan{}))
	require.NoError(t, model.RegisterProductFundingModels(db))
	require.NoError(t, model.RegisterProductRefundModels(db))
	old, oldLog := model.DB, model.LOG_DB
	model.DB, model.LOG_DB = db, db
	t.Cleanup(func() { model.DB, model.LOG_DB = old, oldLog })
	require.NoError(t, db.Create(&model.User{Id: 42, Username: "protocol", Status: common.UserStatusEnabled}).Error)
	require.NoError(t, db.Create(&model.Token{Id: 7, UserId: 42, Key: "protocol-key", Status: common.TokenStatusEnabled, ExpiredTime: -1, RemainQuota: 1000}).Error)
	require.NoError(t, db.Create(&model.Channel{Id: 8, Name: "original"}).Error)
	require.NoError(t, db.Create(&model.ProductFundingAccount{UserID: 42, AccountID: "payer", Asset: userkit.RuntimeAsset, UnitVersion: userkit.RuntimeUnitVersion, State: "central"}).Error)
	ledger := &protocolLedger{available: 500, calls: map[string]userkit.RuntimeCall{}, refunds: map[string]bool{}}
	server := httptest.NewServer(http.HandlerFunc(ledger.serve))
	t.Cleanup(server.Close)
	t.Setenv("UNIHUB_PRODUCT_RUNTIME_ENABLED", "true")
	t.Setenv("UNIHUB_PRODUCT_RUNTIME_TOKEN", strings.Repeat("t", 32))
	t.Setenv("UNIHUB_ORIGIN", server.URL)
	t.Setenv("UNIHUB_PRODUCT_ID", "llmrouter")
	t.Setenv("UNIHUB_PRODUCT_INSTANCE_ID", "default")
	client, e := userkit.NewRuntimeClient(userkit.ServiceConfig{BaseURL: server.URL, ProductID: "llmrouter", InstanceID: "default", ServiceToken: strings.Repeat("t", 32)})
	require.NoError(t, e)
	require.NoError(t, db.Model(&model.ProductFundingAccount{}).Where("user_id = ?", 42).Update("scope_digest", model.NewProductFundingStore(db, client).ScopeDigest()).Error)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1/realtime", nil)
	info := &relaycommon.RelayInfo{RequestId: "protocol-call", UserId: 42, TokenId: 7, TokenKey: "protocol-key", OriginModelName: "gpt-realtime", UsingGroup: "default", UserGroup: "default", StartTime: time.Now(), ChannelMeta: &relaycommon.ChannelMeta{ChannelId: 8, UpstreamModelName: "gpt-realtime"}, PriceData: types.PriceData{ModelRatio: 1, GroupRatioInfo: types.GroupRatioInfo{GroupRatio: 1}}}
	return c, info, ledger
}
func TestProductRealtimeChunksExtendOneOriginalSessionWithoutLocalDebit(t *testing.T) {
	c, info, ledger := protocolFixture(t)
	require.Nil(t, PreConsumeBilling(c, 50, info))
	usage := &dto.RealtimeUsage{TotalTokens: 10, InputTokens: 10}
	usage.InputTokenDetails.TextTokens = 10
	require.NoError(t, PreWssConsumeQuota(c, info, usage))
	require.Equal(t, 450, ledger.available)
	PostWssConsumeQuota(c, info, "gpt-realtime", usage, "")
	require.Equal(t, 490, ledger.available)
	require.Equal(t, 10, ledger.consumed)
}
func TestProductLegacyTaskRepriceAndFailureRefundUseOriginalCall(t *testing.T) {
	c, info, ledger := protocolFixture(t)
	require.Nil(t, PreConsumeBilling(c, 50, info))
	require.NoError(t, SettleBilling(c, info, 50))
	require.NoError(t, model.DB.Model(&model.Channel{}).Where("id = ?", 8).Update("used_quota", 50).Error)
	task := &model.Task{TaskID: "legacy-task", UserId: 42, ChannelId: 8, Quota: 50, ProductCallID: info.RequestId, PrivateData: model.TaskPrivateData{TokenId: 7, BillingSource: "unihub"}}
	require.NoError(t, model.DB.Create(task).Error)
	RecalculateTaskQuota(context.Background(), task, 80, "provider usage")
	require.Equal(t, 420, ledger.available)
	require.True(t, RefundTaskQuota(context.Background(), task, "provider failed"))
	require.Equal(t, 500, ledger.available)
	var channel model.Channel
	require.NoError(t, model.DB.First(&channel, 8).Error)
	require.Zero(t, channel.UsedQuota)
}

func TestProductMidjourneyUsesReservedOriginalCallAndRefundsAfterFailure(t *testing.T) {
	c, info, ledger := protocolFixture(t)
	info.RequestId = "mj-original"
	info.OriginModelName = "mj_imagine"
	info.UpstreamModelName = "mj_imagine"
	require.NoError(t, PrepareMidjourneyAdmission(c.Request.Context(), info, 50, true))
	require.Equal(t, 450, ledger.available)
	task := &model.Midjourney{UserId: 42, ChannelId: 8, MjId: "supplier-task", Action: "IMAGINE"}
	prepared, e := PrepareMidjourneyTaskBilling(info, task, 50, true)
	require.NoError(t, e)
	require.True(t, prepared)
	require.NoError(t, task.Insert())
	paid, e := SettleMidjourneyTaskBilling(info, task, prepared)
	require.NoError(t, e)
	require.True(t, paid)
	require.Equal(t, "mj-original", task.ProductCallID)
	require.Equal(t, 50, ledger.consumed)
	require.True(t, RefundMidjourneyQuota(context.Background(), task, "failed"))
	require.Equal(t, 500, ledger.available)
}

func TestProductTaskRepriceVersionsPermitRepeatedCorrectionsWithoutDuplicateStats(t *testing.T) {
	c, info, ledger := protocolFixture(t)
	require.Nil(t, PreConsumeBilling(c, 50, info))
	require.NoError(t, SettleBilling(c, info, 50))
	require.NoError(t, model.DB.Model(&model.Channel{}).Where("id = ?", 8).Update("used_quota", 50).Error)
	task := &model.Task{TaskID: "repeated-task", UserId: 42, ChannelId: 8, Quota: 50, ProductCallID: info.RequestId, Group: "default", PrivateData: model.TaskPrivateData{TokenId: 7, BillingSource: "unihub", BillingContext: &model.TaskBillingContext{ModelRatio: 1, GroupRatio: 1, OriginModelName: "gpt-realtime"}}}
	require.NoError(t, model.DB.Create(task).Error)
	for _, quota := range []int{80, 60, 80, 60} {
		RecalculateTaskQuota(context.Background(), task, quota, "corrected")
		require.Equal(t, 500-quota, ledger.available)
	}
	RecalculateTaskQuotaByTokens(context.Background(), task, 10)
	require.Equal(t, 490, ledger.available)
	require.True(t, RefundTaskQuota(context.Background(), task, "failed"))
	require.True(t, RefundTaskQuota(context.Background(), task, "failed"))
	require.Equal(t, 500, ledger.available)
	var channel model.Channel
	var user model.User
	require.NoError(t, model.DB.First(&channel, 8).Error)
	require.NoError(t, model.DB.First(&user, 42).Error)
	require.Zero(t, channel.UsedQuota)
	require.Zero(t, user.UsedQuota)
	require.Equal(t, 1, user.RequestCount)
}
