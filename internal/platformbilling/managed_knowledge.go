package platformbilling

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

// Public inference uses the API key's own authority and billing identity. It
// does not mint a task credential or grant access to private knowledge results.
func (a *Adapter) verifyManagedKnowledge(ctx context.Context, credential, name, wire string) (Claims, error) {
	var grant Claims
	if a == nil || !a.Enabled() || !strings.HasPrefix(credential, "uh_key_") || !IsModelCredential(credential) || !ApprovedKnowledgePair(name, wire) {
		return grant, &remoteError{status: http.StatusForbidden}
	}
	err := readKnowledgeAuthority(ctx, a.client, http.MethodPost, "/internal/v1/model-credentials/verify", map[string]string{"token": credential, "model": name, "wire_operation": wire, "instance_id": a.config.InstanceID}, &grant, "")
	if err != nil {
		return grant, err
	}
	if a.validateModelCredential(credential, "", name, grant) != nil || !grant.ExpiresAt.After(time.Now()) || !allowsWire(grant, name, wire) {
		return Claims{}, &remoteError{status: http.StatusForbidden}
	}
	return grant, nil
}

// ManagedKnowledgeBinding reads the current server-owned route and active pin.
// Callers must establish credential authority separately.
func (a *Adapter) ManagedKnowledgeBinding(ctx context.Context, product, name, wire string) (ManagedBinding, error) {
	if a == nil || a.db == nil || !a.Enabled() {
		return ManagedBinding{}, ErrPending
	}
	var binding ManagedBinding
	err := a.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var err error
		binding, err = a.ResolveManagedBinding(tx, product, name, wire, "")
		return err
	})
	return binding, err
}

// AdmitManagedKnowledge resolves the active certified route on the server.
// The original key remains the reserve/settle credential, preserving its caps.
func (a *Adapter) AdmitManagedKnowledge(ctx context.Context, credential, task, id string, wire WireOperation, ct string, body []byte) (*Session, error) {
	if !stableID.MatchString(task) || len(task) > 128 || !stableID.MatchString(id) || len(id) > 128 || ct != "application/json" {
		return nil, ErrInvalid
	}
	var request struct {
		Model string `json:"model"`
	}
	if common.UnmarshalUnique(body, &request) != nil || !ApprovedKnowledgePair(request.Model, wire.ID) {
		return nil, ErrInvalid
	}
	grant, err := a.verifyManagedKnowledge(ctx, credential, request.Model, wire.ID)
	if err != nil {
		return nil, err
	}
	binding, err := a.ManagedKnowledgeBinding(ctx, grant.OriginProductID, request.Model, wire.ID)
	if err != nil {
		return nil, err
	}
	admission := &pinnedKnowledgeAdmission{managedKey: true, credential: credential, grant: knowledgeReadGrant{Claims: grant}, binding: binding}
	return a.admitWireOperation(context.WithValue(ctx, pinnedKnowledgeContextKey{}, admission), credential, task, id, wire, ct, body)
}
