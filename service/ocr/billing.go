// Package ocr provides internal admission and billing for one native OCR request.
// HTTP authorization and supplier network protocols are separate callers.
package ocr

import (
	"context"
	"errors"
	"github.com/QuantumNous/new-api/pkg/metering"
	base "github.com/QuantumNous/new-api/service"
	"math"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/billing_setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"gorm.io/gorm"
)

// AuthorizedAdmission must be built after normal user/token/model/group/channel
// authorization. No raw media, token secret or provider credential is accepted.
type AuthorizedAdmission struct {
	PlatformCallID                                  string
	PlatformPrice                                   *metering.Price
	ValidateTarget                                  func(int, int, string, string, string) error
	Dispatch                                        func(context.Context) error
	BindTask                                        func(context.Context, string) error
	BindTaskTx                                      func(context.Context, *gorm.DB, string) error
	IdempotencyKey                                  string
	UserID, TokenID, ChannelID                      int
	Model, UserGroup, UsingGroup, BillingPreference string
}

type PriceError struct {
	Reason string
	Clamp  *common.QuotaClamp
}

func (e *PriceError) Error() string { return e.Reason }

// Audit contains only JSON-safe saturation metadata; never raw NaN/Inf floats.
func (e *PriceError) Audit() map[string]any {
	if e.Clamp == nil {
		return map[string]any{"reason": e.Reason}
	}
	return map[string]any{"reason": e.Reason, "admin_info": map[string]any{"quota_saturation": map[string]any{"op": e.Clamp.Op, "kind": e.Clamp.Kind, "clamped": e.Clamp.Clamped}}}
}

func ResolveAdmission(in AuthorizedAdmission) (model.OCRAdmission, error) {
	a := model.OCRAdmission{IdempotencyKey: in.IdempotencyKey, UserID: in.UserID, TokenID: in.TokenID, ChannelID: in.ChannelID, Model: in.Model, Group: in.UsingGroup, Preference: in.BillingPreference, QuotaPerUnit: common.QuotaPerUnit}
	if in.PlatformCallID != "" {
		a.PlatformCallID = in.PlatformCallID
		a.PlatformPrice = in.PlatformPrice
		a.Price = 0
		a.GroupRatio = 0
		return a, nil
	}
	if native, ok := billing_setting.ResolveModelNativePrice(in.Model, "ocr"); ok {
		a.NativePrice = &native
		a.GroupRatio = ratio_setting.GetGroupRatio(in.UsingGroup)
		if ratio, ok := ratio_setting.GetGroupGroupRatio(in.UserGroup, in.UsingGroup); ok {
			a.GroupRatio = ratio
		}
		return a, nil
	}
	if billing_setting.GetBillingMode(in.Model) == billing_setting.BillingModeTieredExpr {
		return a, &PriceError{Reason: "OCR requires native request pricing; token expressions are incompatible"}
	}
	// Deliberately require an exact existing native-price entry. Token ratios and
	// default prices must never manufacture a price for this asynchronous contract.
	price, ok := ratio_setting.GetModelPriceCopy()[in.Model]
	if !ok {
		return a, &PriceError{Reason: "OCR native request price is not configured"}
	}
	ratio, ok := ratio_setting.GetGroupGroupRatio(in.UserGroup, in.UsingGroup)
	if !ok {
		ratio = ratio_setting.GetGroupRatio(in.UsingGroup)
	}
	a.Price = price
	a.GroupRatio = ratio
	for _, factor := range []float64{price, ratio, a.QuotaPerUnit} {
		if math.IsNaN(factor) || math.IsInf(factor, 0) || factor < 0 {
			return a, &PriceError{Reason: "OCR pricing contains an invalid factor"}
		}
	}
	quota, clamp := common.QuotaFromFloatChecked(price * ratio * a.QuotaPerUnit)
	if clamp != nil {
		return a, &PriceError{Reason: "OCR native request price exceeds supported quota", Clamp: clamp}
	}
	if quota == 0 && price > 0 && ratio > 0 {
		return a, &PriceError{Reason: "OCR native request price is below one quota unit"}
	}
	return a, nil
}

type Billing struct{ *model.OCRBillingStore }

// NewBilling preserves configured Redis authority. An unavailable configured
// client is an initialization error, never an implicit DB-only fallback.
func NewBilling(db *gorm.DB) (*Billing, error) {
	if db == nil {
		return nil, errors.New("OCR main database unavailable")
	}
	if common.RedisEnabled && common.RDB == nil {
		return nil, errors.New("OCR configured Redis client unavailable")
	}
	if !common.RedisEnabled {
		return &Billing{model.NewOCRBillingStore(db, nil)}, nil
	}
	return &Billing{model.NewOCRBillingStore(db, common.RDB)}, nil
}

func (b *Billing) CreateIntent(ctx context.Context, in AuthorizedAdmission) (model.OCRJob, error) {
	a, err := ResolveAdmission(in)
	if err != nil {
		var priceErr *PriceError
		if errors.As(err, &priceErr) {
			if auditErr := b.RecordAdmissionRejection(ctx, in.UserID, in.TokenID, in.Model, priceErr.Audit()); auditErr != nil {
				return model.OCRJob{}, auditErr
			}
		}
		return model.OCRJob{}, err
	}
	return b.OCRBillingStore.CreateIntent(ctx, a)
}

// OwnedStatus is safe for an authenticated polling surface. Component receipts,
// provider job identifiers and submission claim capabilities remain internal.
type OwnedStatus struct {
	ID, Model, BillingState, SubmissionState, Reason string
	Quota                                            int
}

func (b *Billing) OwnedStatus(ctx context.Context, id string, userID, tokenID int) (OwnedStatus, error) {
	j, err := b.Inspect(ctx, id)
	if err != nil {
		return OwnedStatus{}, err
	}
	if j.Job.UserID != userID || j.Job.TokenID != tokenID {
		return OwnedStatus{}, model.ErrOCRConflict
	}
	return OwnedStatus{ID: j.Job.ID, Model: j.Job.Model, BillingState: j.Job.BillingState, SubmissionState: j.Job.SubmissionState, Reason: j.Job.Reason, Quota: j.Job.Quota}, nil
}

// Finish retains the OCR lifecycle and delegates only financial authority. The
// model persisted the terminal intent first; retries replay one UniHub command.
func (b *Billing) Finish(ctx context.Context, id, outcome string) (model.OCRJob, error) {
	j, err := b.OCRBillingStore.Finish(ctx, id, outcome)
	if err != nil || j.Funding != "platform" {
		return j, err
	}
	if j.BillingState == "settled" || j.BillingState == "refunded" {
		return j, nil
	}
	if j.ProviderJobID != "" {
		if base.PlatformTaskBind == nil {
			return j, errors.New("platform binding recovery unavailable")
		}
		if err = base.PlatformTaskBind(ctx, j.PlatformCallID, j.ProviderJobID); err != nil {
			return j, err
		}
	}
	if outcome != "success" {
		refused := j.SubmissionState == "rejected" && !j.RetryRefusal && j.Decision == "failure" && j.ProviderJobID == ""
		if (!refused && (j.ProviderState != "failed" || j.ProviderJobID == "")) || base.PlatformTaskRelease == nil {
			return j, errors.New("platform OCR no-output release pending")
		}
		if err = base.PlatformTaskRelease(ctx, j.PlatformCallID, j.ProviderJobID); err != nil {
			return j, err
		}
		return b.CompletePlatformOCR(ctx, id)
	}
	if base.PlatformTaskObserver == nil {
		return j, errors.New("platform OCR observer unavailable")
	}
	u := metering.Usage{Version: "metered_v2", Operation: "ocr", State: "incomplete", Source: "success_request", ProviderRequestID: j.ProviderJobID}
	if outcome == "success" {
		u.State = "confirmed"
		u.Measurements = []metering.Measurement{{Key: "requests", Unit: "request", Quantity: metering.Quantity{Value: "1"}}}
	}
	if err = base.PlatformTaskObserver(ctx, j.PlatformCallID, j.ProviderJobID, u); err != nil {
		return j, err
	}

	return b.CompletePlatformOCR(ctx, id)
}
