package router

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/metering"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNativeBillingSelfRequiresAuthenticationAndHidesOtherUsers(t *testing.T) {
	f := newPoolHTTPFixture(t)
	require.NoError(t, f.db.AutoMigrate(&model.NativeBilling{}))
	contract, err := common.Marshal(model.NativeAdmission{Price: metering.Price{Version: "metered_v2", Operation: "speech", Policy: "unicode_scalar_v1", Terms: []metering.Term{{Key: "characters", Unit: "character", Price: metering.Rational{Numerator: "1", Denominator: "1000"}}}}})
	require.NoError(t, err)
	evidence, err := common.Marshal(metering.Usage{Version: "metered_v2", Operation: "speech", State: "confirmed", Source: "secret-source", ProviderRequestID: "secret-provider-id", Measurements: []metering.Measurement{{Key: "characters", Unit: "character", Quantity: metering.Quantity{Value: "0"}}}})
	require.NoError(t, err)
	require.NoError(t, f.db.Create(&[]model.NativeBilling{
		{ID: "own-pending", CallID: "pending", UserID: f.users["common"], State: "measurement_pending", ReservedQuota: 12, Contract: string(contract), AdminReason: "secret-admin"},
		{ID: "own-zero", CallID: "zero", UserID: f.users["common"], State: "settled", ReservedQuota: 12, FinalQuota: 0, Contract: string(contract), Evidence: string(evidence)},
		{ID: "other-user", CallID: "other", UserID: f.users["root"], State: "settled", ReservedQuota: 99},
	}).Error)
	registerNativeBillingRoutes(f.engine.Group("/api"))
	anonymous := httptest.NewRecorder()
	f.engine.ServeHTTP(anonymous, httptest.NewRequest(http.MethodGet, "/api/native-billing/self", nil))
	assert.Equal(t, http.StatusUnauthorized, anonymous.Code)
	request := httptest.NewRequest(http.MethodGet, "/api/native-billing/self", nil)
	request.Header.Set("Authorization", "Bearer "+f.tokens["common"])
	response := httptest.NewRecorder()
	f.engine.ServeHTTP(response, request)
	require.Equal(t, http.StatusOK, response.Code)
	var body struct {
		Data []struct {
			ID           string                 `json:"id"`
			Reserved     string                 `json:"reserved"`
			Final        *string                `json:"final"`
			Measurements []metering.Measurement `json:"measurements"`
		} `json:"data"`
	}
	require.NoError(t, common.Unmarshal(response.Body.Bytes(), &body))
	require.Len(t, body.Data, 2)
	byID := map[string]int{}
	for i, row := range body.Data {
		byID[row.ID] = i
	}
	pending := body.Data[byID["own-pending"]]
	assert.Equal(t, "12", pending.Reserved)
	assert.Nil(t, pending.Final)
	zero := body.Data[byID["own-zero"]]
	require.NotNil(t, zero.Final)
	assert.Equal(t, "0", *zero.Final)
	require.Len(t, zero.Measurements, 1)
	assert.Equal(t, "0", zero.Measurements[0].Quantity.Value)
	assert.NotContains(t, response.Body.String(), "other-user")
	assert.NotContains(t, response.Body.String(), "secret-")
}
