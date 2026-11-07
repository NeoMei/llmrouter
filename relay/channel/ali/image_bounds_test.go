package ali

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/pkg/metering"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestQwenImageActualBoundsRejectBeforeOutputOrSettlement(t *testing.T) {
	for _, tc := range []struct {
		name      string
		usage     string
		maxPixels int64
		maxImages int
	}{
		{"route pixels below provider limit", `{"image_count":1,"width":1024,"height":1024}`, 262144, 4},
		{"route count below provider limit", `{"image_count":2,"width":1024,"height":1024}`, 16777216, 1},
		{"missing actual dimensions", `{"image_count":1,"width":1024}`, 16777216, 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(out)
			c.Request = httptest.NewRequest("POST", "/v1/images/generations", nil)
			request := helper.DomesticImageRequest{Model: "qwen-image-2.0"}
			request.Parameters.Size, request.Parameters.N = "512*512", 1
			c.Set(helper.DomesticImageRequestKey, request)
			observed := 0
			info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{}, ModelCapabilities: dto.ModelCapabilities{MediaLimits: &dto.MediaLimits{MaxPixels: &tc.maxPixels, MaxImages: &tc.maxImages}}, NativeBilling: &relaycommon.NativeBillingContext{Observe: func(context.Context, metering.Usage) error { observed++; return nil }}}
			contents := []map[string]string{{"image": "https://images.example/one.png"}}
			if strings.Contains(tc.usage, `"image_count":2`) {
				contents = append(contents, map[string]string{"image": "https://images.example/two.png"})
			}
			content, err := common.Marshal(contents)
			require.NoError(t, err)
			body := `{"output":{"choices":[{"finish_reason":"stop","message":{"role":"assistant","content":` + string(content) + `}}]},"usage":` + tc.usage + `}`
			apiErr, _ := qwenImageHandler(c, &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, info)
			require.NotNil(t, apiErr)
			require.Equal(t, http.StatusBadGateway, apiErr.StatusCode)
			require.Empty(t, out.Body.String(), "unverified image URLs must not reach the caller")
			require.Zero(t, observed, "unverified outputs must not reach native settlement")
			require.False(t, info.ActualUsageValidated)
		})
	}
}
