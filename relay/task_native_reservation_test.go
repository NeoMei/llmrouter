package relay

import (
	"github.com/QuantumNous/new-api/pkg/metering"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"net/http/httptest"
	"testing"
)

func TestNativeVideoReservationRejectsPluralOutputs(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Set("task_request", &relaycommon.TaskSubmitReq{Duration: 5, Metadata: map[string]interface{}{"sampleCount": float64(2)}})
	_, err := NativeVideoReservation(c, "veo", metering.Price{Policy: "sora_completed_seconds_v1", Terms: []metering.Term{{Key: "video", Unit: "video_second", Price: metering.Rational{Numerator: "1", Denominator: "1"}}}})
	require.Error(t, err)
}

func TestNativeWanVideoReservationSelectsResolutionTier(t *testing.T) {
	gin.SetMode(gin.TestMode)
	price := metering.Price{Version: "metered_v2", Operation: "video", Policy: "ali_wan26_t2v_billable_duration_v1", Terms: []metering.Term{
		{Key: "video_720p", Unit: "video_second", Price: metering.Rational{Numerator: "3", Denominator: "5"}},
		{Key: "video_1080p", Unit: "video_second", Price: metering.Rational{Numerator: "1", Denominator: "1"}},
	}}
	for _, tc := range []struct{ size, key string }{{"1280*720", "video_720p"}, {"1920*1080", "video_1080p"}} {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Set("task_request", relaycommon.TaskSubmitReq{Duration: 5, Size: tc.size})
		resolution, err := NativeVideoResolution(c)
		require.NoError(t, err)
		assert.Equal(t, tc.key, resolution)
		usage, err := NativeVideoReservation(c, "wan2.6-t2v", price)
		require.NoError(t, err)
		usage.State = "confirmed"
		require.NoError(t, metering.ValidateUsage(price, usage))
		require.Len(t, usage.Measurements, 1)
		assert.Equal(t, tc.key, usage.Measurements[0].Key)
		assert.Equal(t, metering.Quantity{Value: "5"}, usage.Measurements[0].Quantity)
	}
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Set("task_request", relaycommon.TaskSubmitReq{Duration: 5, Size: "832*480"})
	_, err := NativeVideoResolution(c)
	require.Error(t, err)
}
