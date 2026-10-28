package service

import (
	"context"
	"errors"
	"net"
	"net/url"
	"slices"

	"github.com/QuantumNous/new-api/model"
)

type PlatformVideoArtifact struct {
	ID, SHA256, ContentType string
	Size                    int64
	Width, Height           int
	Seconds                 float64
}

// PlatformVideoMedia is installed by the router from the certified relay-media
// runtime. Polling itself retains no supplier credential or object-store key.
var PlatformVideoMedia func(context.Context, string, string, int, int) (PlatformVideoArtifact, error)

func preparePlatformWanVideoArtifact(ctx context.Context, task *model.Task, sourceURL string) error {
	bc := task.PrivateData.BillingContext
	if task.FundingOwner != "platform" || bc == nil || bc.NativePrice == nil || bc.NativePrice.Policy != "ali_wan26_t2v_billable_duration_v1" || bc.PlatformCallID == "" {
		return nil
	}
	width, height, ok := wanVideoDimensions(bc.VideoResolution)
	if !ok {
		return errors.New("platform Wan video resolution missing")
	}
	parsed, err := url.Parse(sourceURL)
	host := ""
	if parsed != nil {
		host = parsed.Hostname()
	}
	loopback := bc.VideoResourceAllowLoopback && parsed != nil && parsed.Scheme == "http" && net.ParseIP(host) != nil && net.ParseIP(host).IsLoopback()
	if err != nil || parsed.Opaque != "" || (!loopback && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" || !slices.Contains(bc.VideoResourceOrigins, parsed.Scheme+"://"+parsed.Host) {
		return errors.New("platform Wan video resource origin uncertified")
	}
	if PlatformVideoMedia == nil {
		return errors.New("platform Wan video media runtime unavailable")
	}
	artifact, err := PlatformVideoMedia(ctx, sourceURL, bc.PlatformCallID, width, height)
	if err != nil {
		return err
	}
	if artifact.ID == "" || len(artifact.SHA256) != 64 || artifact.ContentType != "video/mp4" || artifact.Size < 1 || artifact.Width != width || artifact.Height != height || artifact.Seconds <= 0 {
		return errors.New("platform Wan video artifact identity invalid")
	}
	task.PrivateData.ResultResourceID = artifact.ID
	task.PrivateData.ResultSHA256 = artifact.SHA256
	task.PrivateData.ResultSize = artifact.Size
	task.PrivateData.ResultContentType = artifact.ContentType
	return nil
}

func wanVideoDimensions(resolution string) (int, int, bool) {
	switch resolution {
	case "video_720p":
		return 1280, 720, true
	case "video_1080p":
		return 1920, 1080, true
	default:
		return 0, 0, false
	}
}
