package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUnenrolledNativeQuotaKeepsHistoryWithoutGrantingDispatch(t *testing.T) {
	setupDashboardAuthMiddlewareTest(t)
	model.InitDatabaseColumns()
	t.Setenv("UNIHUB_PRODUCT_RUNTIME_ENABLED", "false")
	require.NoError(t, model.DB.AutoMigrate(&model.Token{}))
	require.NoError(t, model.RegisterProductFundingModels(model.DB))
	require.NoError(t, model.RegisterNativeTokenQuotaModels(model.DB))
	owner := createMiddlewarePATUser(t, "quota-history-owner", "unused")
	require.NoError(t, model.DB.Model(owner).Updates(map[string]any{"funding_realm": model.NativeFundingLocalOnly, "funding_realm_evidence": "reviewed", "native_password": true}).Error)
	cases := []struct {
		id          int
		key         string
		status      int
		expires     int64
		deleted     bool
		historyCode int
		receiptCode int
	}{
		{1, "quotaenabled", common.TokenStatusEnabled, -1, false, 204, 204},
		{2, "quotaexhausted", common.TokenStatusExhausted, -1, false, 204, 204},
		{4, "quotaexpired", common.TokenStatusExpired, time.Now().Unix() - 60, false, 204, 401},
		{5, "quotadisabled", common.TokenStatusDisabled, -1, false, 401, 401},
		{3, "quotadeletedthree", common.TokenStatusEnabled, -1, true, 401, 401},
		{13, "quotadeletedthirteen", common.TokenStatusEnabled, -1, true, 401, 401},
	}
	for _, item := range cases {
		token := model.Token{Id: item.id, Key: item.key, UserId: owner.Id, AuthRealm: model.NativeFundingLocalOnly, Status: item.status, ExpiredTime: item.expires, RemainQuota: 100}
		require.NoError(t, model.DB.Create(&token).Error)
		if item.deleted {
			require.NoError(t, model.DB.Delete(&token).Error)
		}
	}
	dispatches := 0
	r := gin.New()
	r.POST("/relay", TokenAuth(), func(c *gin.Context) { dispatches++; c.Status(204) })
	r.GET("/history", TokenAuthReadOnly(), func(c *gin.Context) { c.Status(204) })
	r.GET("/receipt", TokenAuthOwnedJobRead(), func(c *gin.Context) { c.Status(204) })
	for _, item := range cases {
		t.Run(item.key, func(t *testing.T) {
			for _, route := range []struct {
				method, path string
				code         int
			}{{http.MethodPost, "/relay", 0}, {http.MethodGet, "/history", item.historyCode}, {http.MethodGet, "/receipt", item.receiptCode}} {
				request := httptest.NewRequest(route.method, route.path, nil)
				request.Header.Set("Authorization", "Bearer sk-"+item.key)
				response := httptest.NewRecorder()
				r.ServeHTTP(response, request)
				if route.path == "/relay" {
					assert.GreaterOrEqual(t, response.Code, http.StatusBadRequest, response.Body.String())
				} else {
					assert.Equal(t, route.code, response.Code, route.path+": "+response.Body.String())
				}
			}
		})
	}
	assert.Zero(t, dispatches, "read-only compatibility must never reach a dispatch handler")
}
