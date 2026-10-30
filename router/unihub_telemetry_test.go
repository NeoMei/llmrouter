package router

import (
	"fmt"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/internal/platformbilling"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A shared routing principal must never make another actor's calls visible, or
// make the authenticated actor's real platform traffic disappear from old APIs.
func TestUniHubTechnicalUsageReadsLogicalCallsWithoutLocalBilling(t *testing.T) {
	f := newUniHubRegressionFixture(t)
	require.NoError(t, f.db.AutoMigrate(&platformbilling.Call{}, &platformbilling.Attempt{}, &model.Log{}, &model.QuotaData{}))
	bundle, _ := decodeUniHubBundle(t, f.request("POST", "/api/unihub/session", "ordinary", nil))
	now := time.Now().UTC().Truncate(time.Second)
	for _, row := range []platformbilling.Call{
		{CallID: "own-success", ActorID: "ordinary", AccountID: "shared-account", CredentialID: "key-a", OriginProductID: "superwagie", FundingProductID: "llmrouter", TaskID: "task-a", Model: "test-model", Asset: "CNY/6", Charged: "1234", Status: "SETTLED", Intent: `{"usage":{"input":"70","output":"35","cache_read":"20","cache_write":"10","reasoning":"15"}}`, CreatedAt: now, UpdatedAt: now},
		{CallID: "own-unknown", ActorID: "ordinary", AccountID: "personal-account", CredentialID: "key-b", OriginProductID: "llmrouter", FundingProductID: "llmrouter", Model: "test-model", Status: "UNKNOWN", CreatedAt: now, UpdatedAt: now},
		{CallID: "own-failed", ActorID: "ordinary", AccountID: "shared-account", CredentialID: "key-a", OriginProductID: "superwagie", FundingProductID: "llmrouter", Model: "test-model", Status: "RELEASED", CreatedAt: now, UpdatedAt: now},
		{CallID: "another-actor", ActorID: "other", AccountID: "shared-account", CredentialID: "private-key", OriginProductID: "superwagie", FundingProductID: "llmrouter", Model: "private-model", Status: "SETTLED", Intent: `{"usage":{"input":"900","output":"100","cache_read":"0","cache_write":"0","reasoning":"0"}}`, CreatedAt: now, UpdatedAt: now},
	} {
		require.NoError(t, f.db.Create(&row).Error)
	}
	// Retries stay separate attempts, while request totals count logical calls.
	require.NoError(t, f.db.Create(&[]platformbilling.Attempt{{CallID: "own-success", ChannelID: 11, Status: "REJECTED"}, {CallID: "own-success", ChannelID: 12, Status: "USAGE_CONFIRMED"}}).Error)
	query := fmt.Sprintf("?start_timestamp=%d&end_timestamp=%d", now.Unix()-60, now.Unix()+60)
	w := f.request("GET", "/api/data/self"+query, bundle.Data.AccessToken, nil)
	require.Equal(t, 200, w.Code, w.Body.String())
	var dates struct {
		Success bool              `json:"success"`
		Data    []model.QuotaData `json:"data"`
	}
	require.NoError(t, common.Unmarshal(w.Body.Bytes(), &dates))
	require.True(t, dates.Success, w.Body.String())
	require.Len(t, dates.Data, 1, w.Body.String())
	assert.Equal(t, 3, dates.Data[0].Count)
	assert.Equal(t, 150, dates.Data[0].TokenUsed)
	assert.Zero(t, dates.Data[0].Quota)

	w = f.request("GET", "/api/log/self"+query+"&actor_id=other&account_id=shared-account", bundle.Data.AccessToken, nil)
	require.Equal(t, 200, w.Code, w.Body.String())
	var logs struct {
		Success bool `json:"success"`
		Data    struct {
			Total int         `json:"total"`
			Items []model.Log `json:"items"`
		} `json:"data"`
	}
	require.NoError(t, common.Unmarshal(w.Body.Bytes(), &logs))
	require.True(t, logs.Success, w.Body.String())
	assert.Equal(t, 3, logs.Data.Total)
	require.Len(t, logs.Data.Items, 3)
	assert.NotContains(t, w.Body.String(), "private-key")
	assert.NotContains(t, w.Body.String(), "private-model")
	for _, log := range logs.Data.Items {
		assert.Zero(t, log.Quota)
		assert.Zero(t, log.TokenId)
		var other map[string]any
		require.NoError(t, common.UnmarshalJsonStr(log.Other, &other))
		assert.Equal(t, "unihub", other["funding_owner"])
		assert.Equal(t, "ordinary", other["actor_id"])
		if log.RequestId == "own-success" {
			assert.Equal(t, 100, log.PromptTokens)
			assert.Equal(t, 50, log.CompletionTokens)
			assert.Equal(t, "CNY/6", other["central_asset"])
			assert.NotContains(t, other, "central_charged")
			assert.Equal(t, float64(2), other["attempt_count"])
		}
	}
	var user model.User
	require.NoError(t, f.db.First(&user, bundle.Data.User.Id).Error)
	assert.Zero(t, user.Quota)
	assert.Zero(t, user.UsedQuota)
	assert.Zero(t, user.RequestCount)
	for _, path := range []string{"/api/data/flow/self", "/api/log/self/stat"} {
		w = f.request("GET", path+query, bundle.Data.AccessToken, nil)
		require.Equal(t, 200, w.Code, w.Body.String())
		assert.Contains(t, w.Body.String(), `"success":true`)
		assert.NotContains(t, w.Body.String(), "private-model")
		assert.NotContains(t, w.Body.String(), "central_charged")
	}
	w = f.request("GET", "/api/log/self/stat"+query, bundle.Data.AccessToken, nil)
	assert.Contains(t, w.Body.String(), `"rpm":3`)
	assert.Contains(t, w.Body.String(), `"tpm":150`)
	assert.Contains(t, w.Body.String(), `"quota":0`)
	admin, _ := decodeUniHubBundle(t, f.request("POST", "/api/unihub/session", "product-viewer", nil))
	for _, path := range []string{"/api/log/", "/api/data/", "/api/data/flow", "/api/data/users", "/api/log/stat"} {
		w = f.request("GET", path+query, admin.Data.AccessToken, nil)
		require.Equal(t, 200, w.Code, w.Body.String())
		assert.Contains(t, w.Body.String(), `"success":true`)
		assert.NotContains(t, w.Body.String(), "central_charged")
		assert.NotContains(t, w.Body.String(), "1234")
	}
	w = f.request("GET", "/api/log/"+query, admin.Data.AccessToken, nil)
	assert.Contains(t, w.Body.String(), "private-model")
	assert.Contains(t, w.Body.String(), `"total":4`)
	w = f.request("GET", "/api/data/users"+query, admin.Data.AccessToken, nil)
	assert.Contains(t, w.Body.String(), `"username":"ordinary"`)
	assert.Contains(t, w.Body.String(), `"username":"other"`)
}

func TestProductLogReaderExcludesNativeHistoryWhileNativeAdminReads(t *testing.T) {
	f := newUniHubRegressionFixture(t)
	t.Setenv("NATIVE_PUBLIC_ACCESS", "true")
	require.NoError(t, f.db.AutoMigrate(&platformbilling.Call{}, &platformbilling.Attempt{}, &model.Log{}, &model.QuotaData{}, &model.Channel{}))
	require.NoError(t, model.RegisterProductFundingModels(f.db))
	previousLogDB := model.LOG_DB
	model.LOG_DB = f.db
	t.Cleanup(func() { model.LOG_DB = previousLogDB })
	reader, _ := decodeUniHubBundle(t, f.request("POST", "/api/unihub/session", "product-viewer", nil))
	native := model.User{Username: "native-payer", Role: common.RoleRootUser, Status: common.UserStatusEnabled, AuthVersion: 1, Quota: 555000, FundingRealm: model.NativeFundingLocalOnly, FundingRealmEvidence: "reviewed", NativePassword: true}
	require.NoError(t, f.db.Create(&native).Error)
	now := time.Now().Unix()
	log := model.Log{UserId: native.Id, Username: native.Username, CreatedAt: now, Type: model.LogTypeConsume, ModelName: "legacy-private-price", Quota: 123456, PromptTokens: 25, CompletionTokens: 15, Group: "default", RequestId: "native-consumption"}
	require.NoError(t, f.db.Create(&log).Error)
	data := model.QuotaData{UserID: native.Id, Username: native.Username, ModelName: log.ModelName, CreatedAt: now, UseGroup: "default", Count: 2, TokenUsed: 40, Quota: 123456}
	require.NoError(t, f.db.Create(&data).Error)
	query := fmt.Sprintf("?start_timestamp=%d&end_timestamp=%d", now-60, now+60)
	for _, path := range []string{"/api/data/", "/api/data/users", "/api/data/flow", "/api/log/"} {
		w := f.request("GET", path+query, reader.Data.AccessToken, nil)
		require.Equal(t, 200, w.Code, w.Body.String())
		assert.NotContains(t, w.Body.String(), "123456", path)
		assert.NotContains(t, w.Body.String(), "legacy-private-price", path)
		assert.NotContains(t, w.Body.String(), "native-consumption", path)
		assert.NotContains(t, w.Body.String(), "native-payer", path)
	}
	var storedLog model.Log
	var storedData model.QuotaData
	require.NoError(t, f.db.First(&storedLog, log.Id).Error)
	require.NoError(t, f.db.First(&storedData, data.Id).Error)
	assert.Equal(t, 123456, storedLog.Quota)
	assert.Equal(t, 123456, storedData.Quota)
	bundle, err := service.CreateNativeLoginSessionAtAuthVersion(native.Id, 1, "password", "", "")
	require.NoError(t, err)
	for _, path := range []string{"/api/data/", "/api/data/users", "/api/data/flow", "/api/log/"} {
		w := f.request("GET", path+query, bundle.AccessToken, nil)
		require.Equal(t, 200, w.Code, w.Body.String())
		assert.Contains(t, w.Body.String(), `"quota":123456`, path)
	}
}
