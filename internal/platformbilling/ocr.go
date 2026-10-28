package platformbilling

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"gorm.io/gorm"
	"net/url"
	"strings"
)

// OCRPolicy binds the native producer, API version, exact target and request tariff.
type OCRPolicy struct {
	Version          string            `json:"version"`
	PublicModel      string            `json:"public_model"`
	WireOperation    string            `json:"wire_operation"`
	ChannelID        int               `json:"channel_id"`
	ChannelType      int               `json:"channel_type"`
	ChannelKeySHA256 string            `json:"channel_key_sha256"`
	UpstreamModel    string            `json:"upstream_model"`
	Origin           string            `json:"origin"`
	APIversion       string            `json:"api_version"`
	UsagePolicy      string            `json:"usage_policy"`
	MaxInputTokens   int               `json:"max_input_tokens"`
	MaxOutputTokens  int               `json:"max_output_tokens"`
	MaxImageBytes    int               `json:"max_image_bytes"`
	MaxImagePixels   int64             `json:"max_image_pixels"`
	BlankPolicy      string            `json:"blank_policy"`
	ResourceOrigins  []string          `json:"resource_origins"`
	RateIDs          map[string]string `json:"rate_ids"`
}
type frozenOCR struct {
	Policy OCRPolicy `json:"ocr_policy"`
}

func (p OCRPolicy) valid(model, wire string) bool {
	h, e := hex.DecodeString(p.ChannelKeySHA256)
	u, ue := url.Parse(p.Origin)
	usagePolicy := p.UsagePolicy == "success_request" || p.UsagePolicy == "provider_tokens_v1"
	if e != nil || len(h) != 32 || p.Version != "ocr_v1" || p.PublicModel != model || !protocolString(model) || p.WireOperation != wire || p.ChannelID < 1 || !protocolString(p.UpstreamModel) || ue != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || !usagePolicy || len(p.RateIDs) == 0 {
		return false
	}
	for asset, id := range p.RateIDs {
		if (asset != "CNY/6" && asset != "CREDIT/6") || id == "" {
			return false
		}
	}
	if wire == "dashscope.ocr.recognitions.create" {
		legacyModel := p.UsagePolicy == "success_request" && (p.UpstreamModel == "qwen-vl-ocr" || p.UpstreamModel == "qwen-vl-ocr-2025-11-20")
		providerTokenModel := p.UsagePolicy == "provider_tokens_v1" && (p.UpstreamModel == "qwen-vl-ocr" || p.UpstreamModel == "qwen3.5-ocr") && p.MaxInputTokens > 0 && p.MaxInputTokens <= 86400 && p.MaxOutputTokens > 0 && p.MaxOutputTokens <= 32768
		return p.ChannelType == constant.ChannelTypeAli && p.APIversion == "dashscope-advanced-recognition-v1" && (legacyModel || providerTokenModel) && p.Origin == "https://dashscope.aliyuncs.com" && p.MaxImageBytes > 0 && p.MaxImageBytes <= 10<<20 && p.MaxImagePixels > 0 && p.MaxImagePixels <= 16*1024*1024 && (p.BlankPolicy == "reject_empty_v1" || p.BlankPolicy == "allow_empty_complete_v1")
	}
	if len(p.ResourceOrigins) < 1 || len(p.ResourceOrigins) > 4 {
		return false
	}
	for _, origin := range p.ResourceOrigins {
		v, e := url.Parse(origin)
		if e != nil || v.Scheme != "https" || v.Host == "" || v.User != nil || v.Path != "" || v.RawQuery != "" || v.Fragment != "" {
			return false
		}
	}

	return wire == "ocr.jobs.create" && p.ChannelType == constant.ChannelTypePaddleOCR && p.APIversion == "paddle-v2"
}
func (a *Adapter) OCRTarget(model, wire string, id, kind int, upstream, key, origin string) bool {
	p, ok := a.config.OCRPolicies[wire][model]
	h := sha256.Sum256([]byte(key))
	return ok && p.valid(model, wire) && p.ChannelID == id && p.ChannelType == kind && p.UpstreamModel == upstream && p.ChannelKeySHA256 == hex.EncodeToString(h[:]) && strings.TrimRight(origin, "/") == p.Origin
}
func (s *Session) OCRPolicy() (OCRPolicy, error) {
	var f frozenOCR
	if common.UnmarshalUnique([]byte(s.call.MediaSnapshot), &f) != nil || !f.Policy.valid(s.call.Model, s.WireOperation().ID) {
		return OCRPolicy{}, ErrInvalid
	}
	return f.Policy, nil
}
func (s *Session) ValidateOCRTarget(id, kind int, upstream, key, origin string) error {
	p, e := s.OCRPolicy()
	h := sha256.Sum256([]byte(key))
	if e != nil || p.ChannelID != id || p.ChannelType != kind || p.UpstreamModel != upstream || p.ChannelKeySHA256 != hex.EncodeToString(h[:]) || strings.TrimRight(origin, "/") != p.Origin {
		return ErrInvalid
	}
	return nil
}

// OCRReplay resolves existing call identity using its original certificate before
// touching mutable execution policy or supplier state. UNKNOWN never resubmits.
func (a *Adapter) OCRReplay(ctx context.Context, token, task, id string, wire WireOperation, ct string, body []byte) (bool, error) {
	if !a.Enabled() || !IsModelCredential(token) || !stableID.MatchString(id) || !stableID.MatchString(task) {
		return false, ErrInvalid
	}
	var old Call
	e := a.db.WithContext(ctx).First(&old, "call_id = ?", id).Error
	if errors.Is(e, gorm.ErrRecordNotFound) {
		return false, nil
	}
	if e != nil {
		return true, e
	}
	if _, e = a.Receipt(ctx, token, task, id); e != nil {
		return true, e
	}
	if old.WireOperation != wire.ID || old.WireMode != wire.Mode || old.WirePathModel != wire.PathModel {
		return true, ErrConflict
	}
	c := a.config
	c.MaxBodyBytes = 64 << 20
	c.OperationLimits = nil
	var f frozenOCR
	if common.UnmarshalUnique([]byte(old.MediaSnapshot), &f) != nil {
		return true, ErrConflict
	}
	c.OCRPolicies = map[string]map[string]OCRPolicy{wire.ID: {old.Model: f.Policy}}
	var req operationRequest
	if wire.ID == "dashscope.ocr.recognitions.create" {
		req, e = parseQwenOCR(c, ct, body)
	} else {
		req, e = parseOperation(c, "ocr", ct, body)
	}
	if e != nil || req.Model != old.Model {
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

// OCRReadCertificate is only an explicit allowlist for an already-owned job.
// Current tariff availability is deliberately not consulted for historical reads.
func (a *Adapter) OCRReadCertificate(model string) (OCRPolicy, bool) {
	p, ok := a.config.OCRPolicies["ocr.jobs.create"][model]
	return p, ok && p.valid(model, "ocr.jobs.create")
}
