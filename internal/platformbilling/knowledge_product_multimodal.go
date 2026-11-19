package platformbilling

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"

	"github.com/QuantumNous/new-api/common"
)

// AdmitProductMultimodal freezes the certified limits and request fingerprint
// before preparing media. Input text, images and temporary media URLs live only
// for the request. The replay fence therefore needs no external media fetch.
func (a *Adapter) AdmitProductMultimodal(ctx context.Context, credential, task, id string, wire WireOperation, ct string, body []byte, prepare func(string) KnowledgePreparation) (*Session, error) {
	if a == nil || !a.Enabled() || !HasProductAuthorization(ctx) || ct != "application/json" ||
		wire.ID != "multimodal.embeddings.create" || wire.PathModel != "" || wire.Mode != "" && wire.Mode != "knowledge-native-v1" || prepare == nil {
		return nil, ErrInvalid
	}
	var input struct {
		Model string `json:"model"`
	}
	if common.UnmarshalUnique(body, &input) != nil {
		return nil, ErrInvalid
	}
	policy, err := a.ManagedKnowledgePolicy(input.Model, wire.ID)
	if err != nil {
		return nil, err
	}
	request, err := parseKnowledgeMultimodal(a.config, policy, body, wire.Mode == "knowledge-native-v1")
	if err != nil {
		return nil, err
	}
	grant, err := a.managedGrant(ctx, credential, request.Model, wire.ID, "")
	if err != nil {
		return nil, err
	}
	if a.validateManagedCredential(ctx, credential, task, request.Model, grant, false) != nil || !grant.ProductCredit() {
		return nil, ErrInvalid
	}
	var frozen frozenKnowledge
	if common.UnmarshalUnique([]byte(request.MediaSnapshot), &frozen) != nil {
		return nil, ErrInvalid
	}
	effective := frozen.Body
	frozen.Body = nil
	snapshot, err := common.Marshal(frozen)
	if err != nil {
		return nil, ErrInvalid
	}
	request.MediaSnapshot = string(snapshot)
	mac := hmac.New(sha256.New, []byte(a.config.ServiceToken))
	_, _ = mac.Write([]byte("product-multimodal-input-v1\x00"))
	_, _ = mac.Write(request.Canonical)
	session, err := a.admitNative(ctx, credential, task, id, wire, request.Model, mac.Sum(nil), &request)
	if err != nil {
		return nil, err
	}
	if session.call.CredentialID != grant.ID || !session.call.matchesCreditScope(grant) {
		return session, ErrConflict
	}
	prepareForOrigin := prepare(grant.OriginProductID)
	if prepareForOrigin == nil {
		return session, ErrInvalid
	}
	session.canonical, _, err = prepareForOrigin(ctx, effective, policy)
	return session, err
}

func (s *Session) productMultimodal() bool {
	return s.ProductCredit() && s.call.WireOperation == "multimodal.embeddings.create"
}
