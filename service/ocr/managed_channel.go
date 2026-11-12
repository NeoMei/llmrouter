package ocr

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/internal/platformbilling"
	"github.com/QuantumNous/new-api/model"
)

const paddleOfficialOrigin = "https://paddleocr.aistudio-app.com"
const paddleOfficialJobsURL = paddleOfficialOrigin + "/api/v2/ocr/jobs"
const paddleOfficialResultOrigin = "https://paddleocr-store.bj.bcebos.com"

// The official Paddle OCR service shards result buckets across hosts such as
// paddleocr-store-2.bj.bcebos.com and paddleocr-store-6.bj.bcebos.com for the
// same API. Only that exact sharded host family belongs to the official
// endpoint; any other host, scheme or port stays rejected.
var paddleOfficialResultHost = regexp.MustCompile(`^paddleocr-store(-[0-9]+)?\.bj\.bcebos\.com$`)

// nativeCreditOrNative reports a pricing source that authorizes the official
// result bucket: plain native pricing and its CREDIT-priced twin.
func nativeCreditOrNative(source string) bool {
	return source == "llmrouter_native" || source == platformbilling.NativeCreditPricingSource
}

func officialPaddleResultOrigin(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.Fragment != "" || u.Port() != "" {
		return false
	}
	return u.Scheme == "https" && paddleOfficialResultHost.MatchString(u.Host)
}

// ManagedNativeResourceOrigins binds the official Paddle result bucket to its
// exact API endpoint. Custom endpoints retain their original same-origin policy.
func ManagedNativeResourceOrigins(jobsURL, upstreamModel string) ([]string, error) {
	u, err := url.Parse(jobsURL)
	if err != nil || u.Scheme == "" || u.Host == "" || u.User != nil || u.Fragment != "" {
		return nil, model.ErrOCRConflict
	}
	origins := []string{u.Scheme + "://" + u.Host}
	if jobsURL == paddleOfficialJobsURL && upstreamModel == "PaddleOCR-VL-1.6" {
		origins = append(origins, paddleOfficialResultOrigin)
	}
	return origins, nil
}

// nativePaddleResultOrigin recovers rc21 jobs whose immutable snapshot omitted
// the official BOS result origin. It never rewrites that snapshot or grants the
// exception to selected tariffs, custom endpoints or a different task owner.
func (s *Service) nativePaddleResultOrigin(ctx context.Context, j model.OCRJob, e model.OCRExecution) bool {
	if j.Funding != "platform" || j.PlatformCallID == "" || j.ProviderJobID == "" || j.SubmissionState != "submitted" || e.ProfileID != 0 || e.JobsURL != paddleOfficialJobsURL || e.Model != "PaddleOCR-VL-1.6" || !slices.Contains(e.ResourceOrigins, paddleOfficialOrigin) || e.KeySHA256 == "" {
		return false
	}
	var call platformbilling.Call
	if s.db.WithContext(ctx).Where("call_id = ?", j.PlatformCallID).First(&call).Error != nil || !nativeCreditOrNative(call.PricingSource) || call.Status != "UNKNOWN" || call.CredentialKind != "api_key" || call.CredentialID == "" || call.CredentialVersion < 1 || call.OriginProductID != "superwagie" || call.WireOperation != OCROperation || call.Model != e.RequestedModel || call.ProviderTaskID != j.ProviderJobID {
		return false
	}
	var quote platformbilling.NativeQuote
	if common.UnmarshalJsonStr(call.NativeQuoteJSON, &quote) != nil || (quote.PricingVersion != "lr-native-v1" && quote.PricingVersion != platformbilling.NativeCreditPricingVersion) || quote.PricingDigest == "" || quote.ChannelID != strconv.Itoa(j.ChannelID) || quote.BillingModel != j.Model || quote.WireOperation != OCROperation {
		return false
	}
	var binding model.OCRAccountBinding
	// A single-key channel records no separate owner: the member that dispatched
	// the job is its owner, and that is exactly what Ref carries. Requiring an
	// explicit Owner rejected every such job.
	if common.UnmarshalJsonStr(j.AccountBinding, &binding) != nil || binding.Ref.ChannelID != j.ChannelID || binding.Ref.MemberID == "" || binding.Ref.MemberID != j.MemberID {
		return false
	}
	channel, err := s.channel(ctx, j.ChannelID, 0)
	if err != nil {
		return false
	}
	singleKey, available := ManagedNativeChannelKey(channel)
	if !available {
		return false
	}
	key, err := model.ResolveTaskAccountKey(ctx, s.db, channel, binding.Ref)
	if err != nil || key != singleKey {
		return false
	}
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:]) == e.KeySHA256
}

// ManagedNativeChannelKey preserves the native account selector for a direct
// PaddleOCR channel containing exactly one enabled credential. Pool metadata
// does not establish cardinality; a second member is never an implicit fallback.
func ManagedNativeChannelKey(channel *model.Channel) (string, bool) {
	if channel == nil || channel.Type != constant.ChannelTypePaddleOCR || channel.Status != common.ChannelStatusEnabled {
		return "", false
	}
	keys := channel.GetKeys()
	if len(keys) != 1 || strings.TrimSpace(keys[0]) == "" {
		return "", false
	}
	if status, exists := channel.ChannelInfo.MultiKeyStatusList[0]; exists && status != common.ChannelStatusEnabled {
		return "", false
	}
	return keys[0], true
}

func platformAdmissionKey(admission AuthorizedAdmission, channel *model.Channel) (string, bool) {
	if admission.NativePricing {
		return ManagedNativeChannelKey(channel)
	}
	// Selected-scope platform tariffs retain their certified single-key policy.
	return channel.Key, !channel.ChannelInfo.IsMultiKey
}
