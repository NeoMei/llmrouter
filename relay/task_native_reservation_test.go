package relay

import (
	"github.com/QuantumNous/new-api/pkg/metering"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/gin-gonic/gin"
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
