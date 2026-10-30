package platformbilling

import (
	"context"
	"net/http"
	"reflect"
	"time"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

type pinnedKnowledgeContextKey struct{}
type pinnedKnowledgeAdmission struct {
	actor      string // request-memory only; never serialized, logged or sent upstream
	credential string
	grant      knowledgeReadGrant
	binding    ManagedBinding
}

func (a *Adapter) AdmitPinnedKnowledge(ctx context.Context, actor, credential, task, id, pin string, wire WireOperation, ct string, body []byte) (*Session, error) {
	if !opaqueKnowledgeID(pin) {
		return nil, ErrInvalid
	}
	var request struct {
		Model string `json:"model"`
	}
	if common.UnmarshalUnique(body, &request) != nil || !ApprovedKnowledgePair(request.Model, wire.ID) {
		return nil, ErrInvalid
	}
	grant, err := a.verifyKnowledgeDispatch(ctx, actor, credential, task, request.Model, wire.ID)
	if err != nil {
		return nil, err
	}
	var binding ManagedBinding
	err = a.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var e error
		binding, e = a.ResolveManagedBinding(tx, grant.OriginProductID, request.Model, wire.ID, pin)
		return e
	})
	if err != nil {
		return nil, err
	}
	admission := &pinnedKnowledgeAdmission{actor: actor, credential: credential, grant: grant, binding: binding}
	return a.admitWireOperation(context.WithValue(ctx, pinnedKnowledgeContextKey{}, admission), credential, task, id, wire, ct, body)
}

// PinnedKnowledgeProduct exposes only the origin already verified by center
// and sealed into this admitted call. Browser client/query hints are irrelevant.
func (s *Session) PinnedKnowledgeProduct() string {
	if !s.PinnedKnowledge() {
		return ""
	}
	return s.call.OriginProductID
}
func (s *Session) PinnedKnowledge() bool { return s != nil && s.knowledgeAdmission != nil }
func (a *Adapter) preparePinnedCallTx(tx *gorm.DB, row *Call, admission *pinnedKnowledgeAdmission) error {
	current, err := a.ResolveManagedBinding(tx, row.OriginProductID, row.Model, row.WireOperation, admission.binding.PolicyDigest)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(current, admission.binding) {
		return ErrConflict
	}
	if admission.grant.ActorExpiresAt <= time.Now().Unix() || !admission.grant.ExpiresAt.After(time.Now()) {
		return &remoteError{status: 403}
	}
	if err = a.knowledgeResults.checkWriter(tx); err != nil {
		return err
	}
	return a.sealKnowledgeSnapshot(row)
}

// ClaimPinnedKnowledgeSend is called only at the real outbound request point.
// The confirmed commit, not first-byte transport timing, is the authorization
// linearization. No center HTTP, SQL base handle or transport occurs in TxB.
func (s *Session) ClaimPinnedKnowledgeSend(ctx context.Context, channel int, upstream, key string, req *http.Request) error {
	if !s.PinnedKnowledge() {
		return ErrInvalid
	}
	if s.err != nil || s.uncertain || s.attempt.ID != 0 || s.confirmed != nil {
		return ErrPending
	}
	var err error
	if s.AliEmbeddingV4() {
		err = s.ValidateAliEmbeddingOutbound(channel, upstream, key, req)
	} else if s.AliRerank() {
		err = s.ValidateAliRerankOutbound(channel, upstream, key, req)
	} else {
		return ErrInvalid
	}
	if err != nil {
		return err
	}
	p := s.knowledgeAdmission
	grant, err := s.adapter.verifyKnowledgeDispatch(ctx, p.actor, p.credential, s.call.TaskID, s.call.Model, s.call.WireOperation)
	if err != nil {
		return err
	}
	if grant.ID != s.call.CredentialID || grant.ActorID != s.call.ActorID || grant.AccountID != s.call.AccountID || grant.OriginProductID != s.call.OriginProductID || grant.InstanceID != s.call.InstanceID || grant.Asset != p.grant.Asset || grant.FundingMode != p.grant.FundingMode || grant.BillingProfileVersion != s.call.BillingProfileVersion {
		return &remoteError{status: 403}
	}
	attempt := Attempt{CallID: s.call.CallID, ChannelID: channel, Status: "DISPATCHED"}
	claimStarted := false
	err = s.adapter.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		b, e := s.adapter.ResolveManagedBinding(tx, s.call.OriginProductID, s.call.Model, s.call.WireOperation, p.binding.PolicyDigest)
		if e != nil {
			return e
		}
		if !reflect.DeepEqual(b, p.binding) || b.ChannelID != channel {
			return ErrConflict
		}
		if grant.ActorExpiresAt <= time.Now().Unix() || !grant.ExpiresAt.After(time.Now()) || ctx.Err() != nil {
			return &remoteError{status: 403}
		}
		var row Call
		if e = tx.First(&row, "call_id = ?", s.call.CallID).Error; e != nil {
			return e
		}
		if row.Status != "RESERVED" || !sameKnowledgeCallBinding(row, s.call) {
			return ErrConflict
		}
		var count int64
		if e = tx.Model(&Attempt{}).Where("call_id = ?", s.call.CallID).Count(&count).Error; e != nil {
			return e
		}
		if count != 0 {
			return ErrDuplicate
		}
		claimStarted = true
		claimed := tx.Model(&Call{}).Where("call_id = ? AND status = ?", s.call.CallID, "RESERVED").Update("status", "UNKNOWN")
		if claimed.Error != nil {
			return claimed.Error
		}
		if claimed.RowsAffected != 1 {
			return ErrPending
		}
		return tx.Create(&attempt).Error
	})
	if err != nil {
		if claimStarted {
			s.safeFailure = false
			s.uncertain = true
			s.err = ErrPending
			// Commit outcome may be ambiguous. Fence locally even if the first commit
			// did not land; this performs no resend and cannot release the hold.
			_ = s.adapter.db.WithContext(context.WithoutCancel(ctx)).Model(&Call{}).Where("call_id = ? AND status = ?", s.call.CallID, "RESERVED").Update("status", "UNKNOWN").Error
		}
		return err
	}
	s.attempt = attempt
	s.safeFailure = false
	s.uncertain = true
	return nil
}
