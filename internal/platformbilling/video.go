package platformbilling

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/url"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/pkg/metering"
	"gorm.io/gorm"
)

// VideoPolicy is an operator certificate for one closed native async producer.
type VideoPolicy struct {
	Version           string            `json:"version"`
	PublicModel       string            `json:"public_model"`
	WireOperation     string            `json:"wire_operation"`
	ChannelID         int               `json:"channel_id"`
	ChannelType       int               `json:"channel_type"`
	UpstreamModel     string            `json:"upstream_model"`
	ChannelKeySHA256  string            `json:"channel_key_sha256"`
	Origin            string            `json:"origin"`
	APIVersion        string            `json:"api_version"`
	UsagePolicy       string            `json:"usage_policy"`
	MaxSeconds        int               `json:"max_seconds"`
	MaxVideoBytes     int64             `json:"max_video_bytes"`
	Resolutions       []string          `json:"resolutions"`
	ResourceOrigins   []string          `json:"resource_origins"`
	AllowLoopbackHTTP bool              `json:"allow_loopback_http,omitempty"`
	RateIDs           map[string]string `json:"rate_ids"`
}

type frozenVideo struct {
	Policy     VideoPolicy `json:"policy"`
	Resolution string      `json:"resolution"`
}

func (p VideoPolicy) valid(model, wire string) bool {
	hash, err := hex.DecodeString(p.ChannelKeySHA256)
	origin, originErr := url.Parse(p.Origin)
	if err != nil || len(hash) != 32 || (p.Version != "video_v1" && p.Version != "video_v2") || p.PublicModel != model || !protocolString(model) || p.WireOperation != wire || p.ChannelID < 1 || p.UpstreamModel == "" || originErr != nil || origin.Scheme != "https" || origin.Host == "" || origin.Path != "" || origin.RawQuery != "" || origin.User != nil || origin.Fragment != "" || p.MaxSeconds < 1 || p.MaxSeconds > 300 || p.MaxVideoBytes < 1 || p.MaxVideoBytes > 64<<20 || len(p.RateIDs) == 0 {
		return false
	}
	for asset, rate := range p.RateIDs {
		if (asset != "CNY/6" && asset != "CREDIT/6") || rate == "" {
			return false
		}
	}
	if len(p.ResourceOrigins) < 1 || len(p.ResourceOrigins) > 4 {
		return false
	}
	for _, resourceOrigin := range p.ResourceOrigins {
		parsed, e := url.Parse(resourceOrigin)
		host := ""
		if parsed != nil {
			host = parsed.Hostname()
		}
		loopback := p.AllowLoopbackHTTP && parsed != nil && parsed.Scheme == "http" && net.ParseIP(host) != nil && net.ParseIP(host).IsLoopback()
		if e != nil || (!loopback && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
			return false
		}
	}
	commonTarget := wire == "videos.create" && p.ChannelType == constant.ChannelTypeAli && p.UpstreamModel == "wan2.6-t2v" && p.Origin == "https://dashscope.aliyuncs.com" && p.APIVersion == "dashscope-video-v1" && slices.Equal(p.Resolutions, []string{"720P", "1080P"})
	if !commonTarget {
		return false
	}
	if p.Version == "video_v1" && p.UsagePolicy == "ali_wan26_t2v_billable_duration_v1" {
		return true
	} // immutable historical snapshots
	return p.Version == "video_v2" && p.PublicModel == "wan2.6-t2v" && p.UsagePolicy == "ali_wan26_t2v_cny6_v2" && len(p.RateIDs) == 1 && p.RateIDs["CNY/6"] != "" && p.MaxSeconds <= 15
}

func wanVideoResolution(size string) (resolution, measurementKey string, ok bool) {
	switch size {
	case "1280*720":
		return "720P", "video_720p", true
	case "1920*1080":
		return "1080P", "video_1080p", true
	default:
		return "", "", false
	}
}

func parseVideoOperation(p VideoPolicy, contentType string, body []byte) (operationRequest, error) {
	var result operationRequest
	bound := int64(p.MaxVideoBytes)
	if bound > 64<<20 {
		bound = 64 << 20
	}
	if !p.valid(p.PublicModel, p.WireOperation) || contentType != "application/json" || int64(len(body)) > bound || !utf8.Valid(body) {
		return result, ErrInvalid
	}
	var request map[string]any
	if common.UnmarshalUnique(body, &request) != nil || len(request) != 4 {
		return result, ErrInvalid
	}
	for key := range request {
		if !strings.Contains(" model prompt seconds size ", " "+key+" ") {
			return result, ErrInvalid
		}
	}
	model, _ := request["model"].(string)
	prompt, _ := request["prompt"].(string)
	size, _ := request["size"].(string)
	if model != p.PublicModel || prompt == "" || !utf8.ValidString(prompt) {
		return result, ErrInvalid
	}
	resolution, measurementKey, ok := wanVideoResolution(size)
	if !ok || !slices.Contains(p.Resolutions, resolution) {
		return result, ErrInvalid
	}
	secondsValue := request["seconds"]
	if text, ok := secondsValue.(string); ok {
		secondsValue = json.Number(text)
	}
	seconds, err := confirmedCount(secondsValue)
	if err != nil || seconds < 1 || seconds > p.MaxSeconds || (p.Version == "video_v2" && seconds < 2) {
		return result, ErrInvalid
	}
	canonical, err := common.Marshal(request)
	if err != nil {
		return result, ErrInvalid
	}
	frozen, err := common.Marshal(frozenVideo{Policy: p, Resolution: resolution})
	if err != nil {
		return result, ErrInvalid
	}
	result = operationRequest{Model: model, Canonical: canonical, Quantity: seconds, MediaSnapshot: string(frozen), VideoResolution: resolution, MeasurementKey: measurementKey}
	return result, nil
}

func validWanVideoPrice(price metering.Price) bool {
	if price.Version != "metered_v2" || price.Operation != "video" || !metering.WanCashTierPolicy(price.Policy) || len(price.Terms) != 2 || metering.ValidatePrice(price) != nil {
		return false
	}
	expected := map[string]metering.Rational{
		"video_720p":  {Numerator: "600000", Denominator: "1"},
		"video_1080p": {Numerator: "1000000", Denominator: "1"},
	}
	for _, term := range price.Terms {
		if term.Unit != "video_second" || expected[term.Key] != term.Price || term.Step != nil || term.Minimum != nil {
			return false
		}
	}
	return true
}

func (a *Adapter) VideoPolicy(model string) (VideoPolicy, bool) {
	p, ok := a.config.VideoPolicies["videos.create"][model]
	return p, ok && p.valid(model, "videos.create")
}

func (a *Adapter) VideoTarget(model string, id, kind int, upstream, key, origin string) bool {
	p, ok := a.VideoPolicy(model)
	sum := sha256.Sum256([]byte(key))
	if !ok || p.ChannelID != id || p.ChannelType != kind || p.UpstreamModel != upstream || p.ChannelKeySHA256 != hex.EncodeToString(sum[:]) {
		return false
	}
	trimmed := strings.TrimRight(origin, "/")
	if trimmed == p.Origin {
		return true
	}
	parsed, err := url.Parse(trimmed)
	return err == nil && p.AllowLoopbackHTTP && parsed.Scheme == "http" && net.ParseIP(parsed.Hostname()) != nil && net.ParseIP(parsed.Hostname()).IsLoopback() && parsed.Path == "" && parsed.RawQuery == "" && parsed.Fragment == ""
}

func (s *Session) FrozenVideoPolicy() (frozenVideo, error) {
	var frozen frozenVideo
	if common.UnmarshalUnique([]byte(s.call.MediaSnapshot), &frozen) != nil || !frozen.Policy.valid(s.call.Model, s.WireOperation().ID) {
		return frozenVideo{}, ErrInvalid
	}
	return frozen, nil
}

func (s *Session) ValidateVideoTarget(id, kind int, upstream, key, origin string) error {
	if _, ok := s.adapter.config.VideoPolicies["videos.create"][s.call.Model]; !ok {
		return nil
	}
	frozen, err := s.FrozenVideoPolicy()
	if err != nil {
		return err
	}
	if !s.adapter.VideoTarget(frozen.Policy.PublicModel, id, kind, upstream, key, origin) || frozen.Policy.ChannelID != id {
		return ErrInvalid
	}
	return nil
}

func (a *Adapter) VideoReplay(ctx context.Context, token, task, id string, wire WireOperation, contentType string, body []byte) (bool, error) {
	if !a.Enabled() || !IsModelCredential(token) || !stableID.MatchString(id) || !stableID.MatchString(task) || wire.ID != "videos.create" {
		return false, ErrInvalid
	}
	var old Call
	err := a.db.WithContext(ctx).First(&old, "call_id = ?", id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	if err != nil {
		return true, err
	}
	if _, err = a.Receipt(ctx, token, task, id); err != nil {
		return true, err
	}
	if old.WireOperation != wire.ID || old.WirePathModel != wire.PathModel || old.WireMode != wire.Mode {
		return true, ErrConflict
	}
	var frozen frozenVideo
	if common.UnmarshalUnique([]byte(old.MediaSnapshot), &frozen) != nil {
		return true, ErrConflict
	}
	req, parseErr := parseVideoOperation(frozen.Policy, contentType, body)
	if parseErr != nil || req.Model != old.Model || req.MediaSnapshot != old.MediaSnapshot {
		return true, ErrConflict
	}
	data, _ := common.Marshal(struct {
		Version, Operation, Task, Actor, Account, Origin, Asset, Funding string
		Profile                                                          int
		Body                                                             string
	}{"platform-metered-v2", old.Operation, old.TaskID, old.ActorID, old.AccountID, old.OriginProductID, old.Asset, old.FundingMode, old.BillingProfileVersion, string(req.Canonical)})
	data, _ = common.Marshal(struct {
		Request string
		Media   string
	}{string(data), old.MediaSnapshot})
	data, _ = common.Marshal(struct {
		Version string
		Wire    WireOperation
		Scope   string
	}{"platform-wire-v3", wire, string(data)})
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != old.Digest {
		return true, ErrConflict
	}
	return true, ErrDuplicate
}
