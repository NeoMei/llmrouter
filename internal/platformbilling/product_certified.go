package platformbilling

import (
	"context"
	"encoding/json"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/metering"
)

// ProductCertifiedTarget is the bounded operator-certified producer selected
// by the exact native request parser. Its immutable policy is saved separately.
type ProductCertifiedTarget struct {
	ChannelID      int
	UpstreamModel  string
	MeasurementKey string
	Quantity       int
}

func HasProductAuthorization(ctx context.Context) bool {
	return productAuthorizationFrom(ctx).proof != ""
}

func ProductReceiptAlias(ctx context.Context, row Call) string {
	if row.CreditScope == ProductCreditScope {
		alias := productAuthorizationFrom(ctx).receiptAlias
		identity := Claims{InstanceID: row.InstanceID, OriginProductID: row.OriginProductID, AccountID: row.AccountID, ActorID: row.ActorID, ID: row.CredentialID}
		if callID, err := identity.productCreditCallID(alias); err == nil && callID == row.CallID {
			return alias
		}
	}
	return ""
}

// AdmitProductCertifiedOperation reuses the original certified media parser
// and preserves its canonical request bytes and supplier certificate across
// retries. Only the sale tariff and wallet owner come from the product lane.
func (a *Adapter) AdmitProductCertifiedOperation(ctx context.Context, credential, task, id string, wire WireOperation, contentType string, body []byte) (*Session, ProductCertifiedTarget, error) {
	var target ProductCertifiedTarget
	if !HasProductAuthorization(ctx) || int64(len(body)) > a.OperationBodyLimit(wireBillingOperation(wire.ID)) {
		return nil, target, ErrInvalid
	}
	var req operationRequest
	var err error
	switch wire.ID {
	case "videos.create":
		var fields struct {
			Model   string      `json:"model"`
			Seconds json.Number `json:"seconds"`
		}
		if common.UnmarshalUnique(body, &fields) != nil {
			return nil, target, ErrInvalid
		}
		// The old certified parser also accepts a string for duration. The
		// product wire uses the actual documented integer request shape.
		var exact map[string]any
		if common.UnmarshalUnique(body, &exact) != nil {
			return nil, target, ErrInvalid
		}
		if _, ok := exact["seconds"].(json.Number); !ok {
			return nil, target, ErrInvalid
		}
		p, ok := a.VideoPolicy(fields.Model)
		if !ok || p.Version != "video_v2" {
			return nil, target, ErrInvalid
		}
		req, err = parseVideoOperation(p, contentType, body)
		target.ChannelID, target.UpstreamModel = p.ChannelID, p.UpstreamModel
	case "images.generations.create", "images.edits.create", "gemini.images.generate":
		name, e := a.ImageRequestModel(wire, contentType, body)
		if e != nil {
			return nil, target, e
		}
		p, ok := a.ImagePolicy(name, wire.ID)
		if !ok {
			return nil, target, ErrInvalid
		}
		req, err = parseImageOperation(p, wire, contentType, body)
		target.ChannelID, target.UpstreamModel = p.ChannelID, p.UpstreamModel
	case "dashscope.audio.transcriptions.create":
		name, e := audioRequestModel(wire.ID, contentType, body, a.config)
		if e != nil {
			return nil, target, e
		}
		p, ok := a.config.AudioPolicies[wire.ID][name]
		if !ok {
			return nil, target, ErrInvalid
		}
		req, err = parseAudioOperation(a.config, p, contentType, body)
		target.ChannelID, target.UpstreamModel = p.ChannelID, p.UpstreamModel
	default:
		return nil, target, ErrInvalid
	}
	if err != nil || req.Quantity < 1 || target.ChannelID < 1 {
		return nil, target, ErrInvalid
	}
	target.MeasurementKey, target.Quantity = req.MeasurementKey, req.Quantity
	s, err := a.admitNative(ctx, credential, task, id, wire, req.Model, req.Canonical, &req)
	return s, target, err
}

func (s *Session) observeProductNativeMetering(ctx context.Context, u metering.Usage) error {
	if u.State != "confirmed" {
		return nil
	}
	if s.adapter.productCreditMetering == nil {
		return ErrPending
	}
	actual, err := s.adapter.productCreditMetering(s.call.NativePriceSnapshot, u)
	if err != nil {
		return err
	}
	// A poller can repeat an already durably confirmed result. A conflicting
	// observation is fenced against delivery and cannot rewrite settlement.
	if s.call.Evidence != "" {
		raw, err := common.Marshal(u)
		if err != nil {
			return ErrInvalid
		}
		if s.call.Evidence != string(raw) {
			_ = s.adapter.db.WithContext(ctx).Model(&Call{}).Where("call_id = ?", s.call.CallID).Update("evidence_conflict", true).Error
			return ErrConflict
		}
		if s.call.Status == "SETTLED" {
			return nil
		}
		if s.call.Status == "SETTLE_PENDING" {
			return s.adapter.deliverNative(ctx, s.call)
		}
	}
	return s.settleNative(ctx, actual, &Usage{Metered: &u}, nil)
}

func IsProductCertifiedOperation(wire string) bool {
	switch wire {
	case "videos.create", "images.generations.create", "images.edits.create", "gemini.images.generate", "dashscope.audio.transcriptions.create":
		return true
	default:
		return false
	}
}

// ProductCertifiedModelTarget reports the configured producer, without
// granting use of its channel or exposing any supplier credentials.
func (a *Adapter) ProductCertifiedModelTarget(name, wire string) (int, string) {
	if a == nil {
		return 0, ""
	}
	switch wire {
	case "videos.create":
		if policy, ok := a.VideoPolicy(name); ok && policy.Version == "video_v2" {
			return policy.ChannelID, policy.UpstreamModel
		}
	case "images.generations.create", "images.edits.create", "gemini.images.generate":
		if policy, ok := a.ImagePolicy(name, wire); ok {
			return policy.ChannelID, policy.UpstreamModel
		}
	case "dashscope.audio.transcriptions.create":
		if policy, ok := a.AudioPolicy(name, wire); ok {
			return policy.ChannelID, policy.UpstreamModel
		}
	}
	return 0, ""
}
