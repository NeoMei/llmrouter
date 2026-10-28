package platformbilling

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/pkg/metering"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func wanVideoPolicyFixture(t *testing.T) VideoPolicy {
	t.Helper()
	sum := sha256.Sum256([]byte("fixture-key"))
	return VideoPolicy{
		Version:          "video_v1",
		PublicModel:      "wan2.6-t2v",
		WireOperation:    "videos.create",
		ChannelID:        16,
		ChannelType:      constant.ChannelTypeAli,
		UpstreamModel:    "wan2.6-t2v",
		ChannelKeySHA256: hex.EncodeToString(sum[:]),
		Origin:           "https://dashscope.aliyuncs.com",
		APIVersion:       "dashscope-video-v1",
		UsagePolicy:      "ali_wan26_t2v_billable_duration_v1",
		MaxSeconds:       10,
		MaxVideoBytes:    64 << 20,
		Resolutions:      []string{"720P", "1080P"},
		ResourceOrigins:  []string{"https://media.example"},
		RateIDs:          map[string]string{"CNY/6": "wan-rate"},
	}
}

func wanTierPrice() metering.Price {
	return metering.Price{Version: "metered_v2", Operation: "video", Policy: "ali_wan26_t2v_billable_duration_v1", Terms: []metering.Term{
		{Key: "video_720p", Unit: "video_second", Price: metering.Rational{Numerator: "3", Denominator: "5"}},
		{Key: "video_1080p", Unit: "video_second", Price: metering.Rational{Numerator: "1", Denominator: "1"}},
	}}
}

func TestWanVideoPolicyParsesAndReservesSelectedOriginalPriceTier(t *testing.T) {
	p := wanVideoPolicyFixture(t)
	require.True(t, p.valid(p.PublicModel, p.WireOperation))
	for _, tc := range []struct {
		size, resolution, term string
		amount                 string
	}{
		{"1280*720", "720P", "video_720p", "3"},
		{"1920*1080", "1080P", "video_1080p", "5"},
	} {
		body := []byte(`{"model":"wan2.6-t2v","prompt":"hello","seconds":"5","size":"` + tc.size + `"}`)
		req, err := parseVideoOperation(p, "application/json", body)
		require.NoError(t, err, tc.size)
		assert.Equal(t, tc.resolution, req.VideoResolution)
		assert.Equal(t, tc.term, req.MeasurementKey)
		assert.Equal(t, 5, req.Quantity)
		amount, err := meteredReserve(wanTierPrice(), req)
		require.NoError(t, err)
		assert.Equal(t, tc.amount, amount)
		var frozen frozenVideo
		require.NoError(t, common.UnmarshalUnique([]byte(req.MediaSnapshot), &frozen))
		assert.Equal(t, p, frozen.Policy)
		assert.Equal(t, tc.resolution, frozen.Resolution)
	}
}

func TestWanVideoPolicyRejectsUncertifiedRequestShapeOrTier(t *testing.T) {
	p := wanVideoPolicyFixture(t)
	for name, body := range map[string]string{
		"unknown":              `{"model":"wan2.6-t2v","prompt":"hello","seconds":"5","size":"1280*720","metadata":{}}`,
		"input_reference":      `{"model":"wan2.6-t2v","prompt":"hello","seconds":"5","size":"1280*720","input_reference":"https://media.example/frame.png"}`,
		"480p":                 `{"model":"wan2.6-t2v","prompt":"hello","seconds":"5","size":"832*480"}`,
		"resolution_alias":     `{"model":"wan2.6-t2v","prompt":"hello","seconds":"5","size":"720P"}`,
		"over_duration":        `{"model":"wan2.6-t2v","prompt":"hello","seconds":"11","size":"1280*720"}`,
		"conflicting_duration": `{"model":"wan2.6-t2v","prompt":"hello","seconds":"5","duration":6,"size":"1280*720"}`,
		"plural_count":         `{"model":"wan2.6-t2v","prompt":"hello","seconds":"5","size":"1280*720","n":2}`,
		"wrong_model":          `{"model":"wan2.7-t2v","prompt":"hello","seconds":"5","size":"1280*720"}`,
		"empty_prompt":         `{"model":"wan2.6-t2v","prompt":"","seconds":"5","size":"1280*720"}`,
		"non_json":             "not-json",
	} {
		_, err := parseVideoOperation(p, "application/json", []byte(body))
		require.Error(t, err, name)
	}
	bad := p
	bad.Resolutions = []string{"720P", "1080P", "480P"}
	require.False(t, bad.valid(bad.PublicModel, bad.WireOperation))
	bad = p
	bad.ResourceOrigins = []string{"http://media.example"}
	require.False(t, bad.valid(bad.PublicModel, bad.WireOperation))
}

func TestWanVideoTargetBindsExactChannelMemberAndUpstream(t *testing.T) {
	p := wanVideoPolicyFixture(t)
	c := Config{VideoPolicies: map[string]map[string]VideoPolicy{p.WireOperation: {p.PublicModel: p}}}
	a := &Adapter{config: c}
	require.True(t, a.VideoTarget(p.PublicModel, p.ChannelID, p.ChannelType, p.UpstreamModel, "fixture-key", p.Origin))
	require.False(t, a.VideoTarget(p.PublicModel, p.ChannelID, p.ChannelType, "wan2.7-t2v", "fixture-key", p.Origin))
	require.False(t, a.VideoTarget(p.PublicModel, p.ChannelID, p.ChannelType, p.UpstreamModel, "other-key", p.Origin))
	require.False(t, a.VideoTarget(p.PublicModel, p.ChannelID+1, p.ChannelType, p.UpstreamModel, "fixture-key", p.Origin))
	p.AllowLoopbackHTTP = true
	c.VideoPolicies[p.WireOperation][p.PublicModel] = p
	require.True(t, a.VideoTarget(p.PublicModel, p.ChannelID, p.ChannelType, p.UpstreamModel, "fixture-key", "http://127.0.0.1:1234"))
}
