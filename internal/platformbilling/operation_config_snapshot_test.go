package platformbilling

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOperationBodyLimitRetainsConstructedConfiguration(t *testing.T) {
	limits := map[string]map[string]OperationLimits{"image_generation": {"test": {MaxBodyBytes: 256}}}
	a, err := New(nil, Config{MaxBodyBytes: 64, OperationLimits: limits})
	require.NoError(t, err)
	require.EqualValues(t, 256, a.OperationBodyLimit("image_generation"))
	limits["image_generation"]["test"] = OperationLimits{MaxBodyBytes: 1024}
	assert.EqualValues(t, 256, a.OperationBodyLimit("image_generation"), "mutating the caller's limit must not expand the existing adapter's admission boundary")
	limits["image_generation"] = map[string]OperationLimits{"other": {MaxBodyBytes: 2048}}
	assert.EqualValues(t, 256, a.OperationBodyLimit("image_generation"))
}

func TestOperationReservationRetainsConstructedRatePin(t *testing.T) {
	for _, level := range []string{"model", "asset", "operation"} {
		t.Run(level, func(t *testing.T) {
			fixture, db, _ := meteredFixture(t, "speech")
			pins := map[string]map[string]map[string]string{"speech": {"CREDIT/6": {"test": "native"}}}
			cfg := fixture.config
			cfg.OperationRateIDs = pins
			a, err := New(db, cfg)
			require.NoError(t, err)
			switch level {
			case "model":
				pins["speech"]["CREDIT/6"]["test"] = "foreign-rate"
			case "asset":
				pins["speech"]["CREDIT/6"] = map[string]string{"test": "foreign-rate"}
			case "operation":
				pins["speech"] = map[string]map[string]string{"CREDIT/6": {"test": "foreign-rate"}}
			}
			_, err = a.AdmitOperation(context.Background(), "uh_model_token", "task", "call", "speech", "application/json", []byte(`{"model":"test","input":"hello","voice":"alloy"}`))
			require.NoError(t, err, "an external map edit must not replace the constructed adapter's reservation tariff")
			var reserved Call
			require.NoError(t, db.First(&reserved, "call_id = ?", "call").Error)
			assert.Equal(t, "RESERVED", reserved.Status)
			assert.Equal(t, "native", reserved.RateID)
		})
	}
}
