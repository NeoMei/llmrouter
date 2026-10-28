package service

import (
	"context"
	"testing"

	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/metering"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPlatformWanVideoArtifactAllowsSignedQueryOnExactOrigin(t *testing.T) {
	task := &model.Task{
		FundingOwner:   "platform",
		PlatformCallID: "wan-call",
		PrivateData: model.TaskPrivateData{BillingContext: &model.TaskBillingContext{
			NativePrice:    &metering.Price{Policy: "ali_wan26_t2v_billable_duration_v1"},
			PlatformCallID: "wan-call", VideoResolution: "video_720p",
			VideoResourceOrigins: []string{"https://media.example"},
		}},
	}
	seen := ""
	oldMedia := PlatformVideoMedia
	PlatformVideoMedia = func(_ context.Context, sourceURL, callID string, width, height int) (PlatformVideoArtifact, error) {
		seen = sourceURL
		assert.Equal(t, "wan-call", callID)
		assert.Equal(t, 1280, width)
		assert.Equal(t, 720, height)
		return PlatformVideoArtifact{ID: "resource", SHA256: "0123456789012345678901234567890123456789012345678901234567890123", ContentType: "video/mp4", Size: 10, Width: width, Height: height, Seconds: 1}, nil
	}
	t.Cleanup(func() { PlatformVideoMedia = oldMedia })

	signed := "https://media.example/path/video.mp4?Expires=1717171717&OSSAccessKeyId=test&Signature=abc"
	require.NoError(t, preparePlatformWanVideoArtifact(context.Background(), task, signed))
	assert.Equal(t, signed, seen)
	assert.Equal(t, "resource", task.PrivateData.ResultResourceID)

	for name, sourceURL := range map[string]string{
		"uncertified_host":  "https://media.example.evil.com/video.mp4?Signature=abc",
		"certified_host":    "https://other.example/video.mp4?Signature=abc",
		"userinfo":          "https://user:secret@media.example/video.mp4?Signature=abc",
		"fragment":          "https://media.example/video.mp4?Signature=abc#fragment",
		"opaque_scheme":     "https:media.example/video.mp4?Signature=abc",
		"non_https":         "http://media.example/video.mp4?Signature=abc",
		"missing_host":      "https:///video.mp4?Signature=abc",
		"scheme_relative":   "//media.example/video.mp4?Signature=abc",
		"signed_query_only": "https://evil.example?media.example",
	} {
		t.Run(name, func(t *testing.T) {
			before := task.PrivateData.ResultResourceID
			seen = ""
			require.Error(t, preparePlatformWanVideoArtifact(context.Background(), task, sourceURL))
			assert.Empty(t, seen)
			assert.Equal(t, before, task.PrivateData.ResultResourceID)
		})
	}
}
