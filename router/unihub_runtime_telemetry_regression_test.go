package router

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	userkit "github.com/NeoMei/UniHub/packages/userkit-go"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/internal/platformaccount"
	"github.com/QuantumNous/new-api/internal/platformbilling"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRuntimeTelemetryUsesVerifiedActorAndPreservesNativeHistory(t *testing.T) {
	f := newUniHubRegressionFixture(t)
	t.Setenv("NATIVE_PUBLIC_ACCESS", "true")
	require.NoError(t, f.db.AutoMigrate(&model.ProductAuthLifecycle{}, &model.Log{}, &model.Channel{}, &platformbilling.Call{}, &platformbilling.Attempt{}))
	oldLog := model.LOG_DB
	model.LOG_DB = f.db
	t.Cleanup(func() { model.LOG_DB = oldLog })
	config, err := platformaccount.Current()
	require.NoError(t, err)
	t.Cleanup(func() { platformaccount.Configure(config) })

	managed := model.User{Username: "runtime-member", AffCode: "runtime-member", AuthVersion: 1, Status: common.UserStatusEnabled, Role: common.RoleRootUser}
	native := model.User{Username: "native-user", AffCode: "native-user", AuthVersion: 1, Status: common.UserStatusEnabled, Role: common.RoleRootUser}
	require.NoError(t, f.db.Create(&managed).Error)
	require.NoError(t, f.db.Create(&native).Error)
	_, err = model.BindUniHubIdentity(platformaccount.Identity{Issuer: config.PublicOrigin, User: platformaccount.User{ID: "central-member"}}, managed.Id)
	require.NoError(t, err)
	managedSession, err := service.CreateLoginSession(managed.Id, "password", "", "")
	require.NoError(t, err)
	nativeSession, err := service.CreateLoginSession(native.Id, "password", "", "")
	require.NoError(t, err)

	role := 1
	permissions := []string{"product:use"}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/internal/v1/product-runtime/identity/"+strconv.Itoa(managed.Id) {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		identity := userkit.ProductIdentity{Binding: userkit.RuntimeBinding{ID: "binding", LocalUserID: strconv.Itoa(managed.Id), UserID: "central-member", ProductID: "llmrouter", InstanceID: "default"}, Access: userkit.Access{Issuer: config.PublicOrigin, Subject: "central-member", User: userkit.User{ID: "central-member"}, ProductID: "llmrouter", InstanceID: "default", Active: true, ProductRole: role, Permissions: permissions, ExpiresAt: time.Now().Add(time.Minute).Unix()}, ProductRole: role, AuthVersion: 1}
		data, marshalErr := common.Marshal(identity)
		if marshalErr != nil {
			w.WriteHeader(500)
			return
		}
		_, _ = w.Write(data)
	}))
	t.Cleanup(server.Close)
	t.Setenv("UNIHUB_PRODUCT_RUNTIME_ENABLED", "true")
	t.Setenv("UNIHUB_ORIGIN", server.URL)
	t.Setenv("UNIHUB_PRODUCT_RUNTIME_TOKEN", strings.Repeat("s", 32))
	now := time.Now().UTC().Truncate(time.Second)
	for _, call := range []platformbilling.Call{
		{CallID: "own-central", ActorID: "central-member", AccountID: "own-account", FundingProductID: "llmrouter", InstanceID: "default", Model: "own-model", Status: "SETTLED", Charged: "987654", TelemetryOutcome: "success", CreatedAt: now},
		{CallID: "own-stream", ActorID: "central-member", AccountID: "own-account", FundingProductID: "llmrouter", InstanceID: "default", Model: "own-model", Status: "SETTLED", TelemetryOutcome: "success", TelemetryIsStream: true, CreatedAt: now},
		{CallID: "other-central", ActorID: "other", AccountID: "other-account", FundingProductID: "llmrouter", InstanceID: "default", Model: "other-model", Status: "SETTLED", CreatedAt: now},
		{CallID: "other-instance", ActorID: "central-member", AccountID: "own-account", FundingProductID: "llmrouter", InstanceID: "another", Model: "instance-model", Status: "SETTLED", CreatedAt: now},
	} {
		require.NoError(t, f.db.Create(&call).Error)
	}
	for _, log := range []model.Log{
		{UserId: managed.Id, Username: managed.Username, RequestId: "own-native-history", Type: model.LogTypeSystem, Content: "private-content", Other: `{"secret":"private"}`, Quota: 12345, CreatedAt: now.Unix()},
		{UserId: native.Id, Username: native.Username, RequestId: "other-native-history", Type: model.LogTypeConsume, Quota: 45678, CreatedAt: now.Unix()},
	} {
		require.NoError(t, f.db.Create(&log).Error)
	}
	query := fmt.Sprintf("?start_timestamp=%d&end_timestamp=%d&actor_id=other&account_id=other-account&user_id=%d&username=other", now.Unix()-60, now.Unix()+60, native.Id)
	for _, enabled := range []bool{false, true} {
		t.Run(fmt.Sprintf("runtime_product_enabled_%t", enabled), func(t *testing.T) {
			t.Setenv("UNIHUB_PRODUCT_ENABLED", strconv.FormatBool(enabled))
			current := config
			current.Enabled = enabled
			platformaccount.Configure(current)
			response := f.request("GET", "/api/log/self"+query, managedSession.AccessToken, nil)
			require.Equal(t, 200, response.Code, response.Body.String())
			assert.Contains(t, response.Body.String(), "own-central")
			assert.Contains(t, response.Body.String(), "SETTLED")
			assert.Contains(t, response.Body.String(), "own-native-history")
			var body struct {
				Success bool `json:"success"`
				Data    struct {
					Total int         `json:"total"`
					Items []model.Log `json:"items"`
				} `json:"data"`
			}
			require.NoError(t, common.Unmarshal(response.Body.Bytes(), &body))
			require.True(t, body.Success)
			require.Equal(t, 3, body.Data.Total)
			found := make(map[string]bool)
			for _, entry := range body.Data.Items {
				if entry.RequestId != "own-central" && entry.RequestId != "own-stream" {
					continue
				}
				var details map[string]any
				require.NoError(t, common.UnmarshalJsonStr(entry.Other, &details))
				assert.Equal(t, entry.RequestId, details["call_id"])
				assert.Equal(t, "unihub", details["funding_owner"])
				assert.Equal(t, "SETTLED", details["central_status"])
				assert.Equal(t, "central-member", details["actor_id"])
				assert.Equal(t, entry.RequestId == "own-stream", entry.IsStream)
				found[entry.RequestId] = true
			}
			assert.Equal(t, map[string]bool{"own-central": true, "own-stream": true}, found)
			// Current technical telemetry is limited to a 30-day range. Exceeding
			// that limit returns an API error instead of silently widening scope.
			longRange := fmt.Sprintf("?start_timestamp=%d&end_timestamp=%d", now.Unix()-2592001, now.Unix())
			tooLong := f.request("GET", "/api/log/self"+longRange, managedSession.AccessToken, nil)
			require.Equal(t, 200, tooLong.Code)
			var failure struct {
				Success bool   `json:"success"`
				Message string `json:"message"`
			}
			require.NoError(t, common.Unmarshal(tooLong.Body.Bytes(), &failure))
			assert.False(t, failure.Success)
			assert.NotEmpty(t, failure.Message)
			for _, hidden := range []string{"other-central", "other-instance", "other-native-history", "987654", "12345", "private-content"} {
				assert.NotContains(t, response.Body.String(), hidden)
			}
			assert.Equal(t, 403, f.request("GET", "/api/log/"+query, managedSession.AccessToken, nil).Code, "native root must not grant central log:read")
		})
	}
	role, permissions = 10, []string{"product:use", "log:read"}
	response := f.request("GET", "/api/log/?start_timestamp="+strconv.FormatInt(now.Unix()-60, 10)+"&end_timestamp="+strconv.FormatInt(now.Unix()+60, 10), managedSession.AccessToken, nil)
	require.Equal(t, 200, response.Code, response.Body.String())
	assert.Contains(t, response.Body.String(), "other-central")
	assert.Contains(t, response.Body.String(), "other-native-history")
	assert.NotContains(t, response.Body.String(), "other-instance")
	assert.NotContains(t, response.Body.String(), "45678")
	role, permissions = 1, []string{"product:use"}
	assert.Equal(t, 403, f.request("GET", "/api/log/", managedSession.AccessToken, nil).Code)

	// With central runtime disabled, a native principal retains its own native log and quota.
	t.Setenv("UNIHUB_PRODUCT_RUNTIME_ENABLED", "false")
	response = f.request("GET", "/api/log/self", nativeSession.AccessToken, nil)
	require.Equal(t, 200, response.Code, response.Body.String())
	assert.Contains(t, response.Body.String(), "other-native-history")
	assert.Contains(t, response.Body.String(), "45678")
	assert.NotContains(t, response.Body.String(), "own-central")
}
