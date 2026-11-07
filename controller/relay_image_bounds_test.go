package controller

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/types"
	"github.com/stretchr/testify/require"
)

func TestImageCandidateCannotReusePriorProviderOutputProof(t *testing.T) {
	db := setupRelayRouteRuntimeDB(t)
	seedRelayRouteTargets(t, db, 128000)
	c := relayRouteResolutionContext(t)
	c.Request = httptest.NewRequest("POST", "/v1/images/generations", strings.NewReader(`{"model":"image","prompt":"circle","size":"1024x1024","n":1}`))
	c.Request.Header.Set("Content-Type", "application/json")
	prior := helper.DomesticImageRequest{Model: "qwen-image-2.0"}
	prior.Parameters.Size, prior.Parameters.N = "1024*1024", 1
	c.Set(helper.DomesticImageRequestKey, prior)
	spy := &relayRouteBillingSpy{}
	info := &relaycommon.RelayInfo{
		RelayMode: relayconstant.RelayModeImagesGenerations, RelayFormat: types.RelayFormatOpenAIImage,
		Request:         &dto.ImageRequest{Model: "image", Prompt: "circle", Size: "1024x1024", N: common.GetPointer(uint(1))},
		OriginModelName: "image", UserId: 99, UserGroup: "default", UsingGroup: "default", Billing: spy,
		RouteCandidates: []relaycommon.RouteCandidate{{
			RouteID: 1, TargetID: 201, ChannelID: 101, UpstreamModel: "upstream-a", BillingModel: "gpt-4o-mini", SelectedGroup: "default",
			Capabilities: dto.ModelCapabilities{Operations: []string{"images.generations.create"}, InputModalities: []string{"text"}, OutputModalities: []string{"image"}, MediaLimits: &dto.MediaLimits{MaxPixels: common.GetPointer(int64(16777216))}},
		}},
	}
	err := prepareRouteCandidateForAttempt(c, info, 0, 1, &types.TokenCountMeta{})
	require.NotNil(t, err)
	require.Equal(t, http.StatusBadRequest, err.StatusCode)
	require.Contains(t, err.Error(), "require verified media metadata")
	require.Empty(t, spy.calls, "a prior supplier's image proof must not authorize a new candidate's reserve")
	require.Zero(t, info.UpstreamRequestBodySize)
}
