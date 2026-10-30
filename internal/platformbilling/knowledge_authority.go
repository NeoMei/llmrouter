package platformbilling

import (
	"context"
	"io"
	"mime"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"gorm.io/gorm"
)

// IndexReadAuthority deliberately contains no Claims or dispatch credential.
// Its unexported expiry prevents constructing read authority outside this package.
type IndexReadAuthority struct {
	ActorID         string
	AccountID       string
	OriginProductID string
	InstanceID      string
	AccessVersion   int64
	expiresAt       int64
}
type KnowledgeIndexRoute struct {
	Version         int                 `json:"version"`
	Purpose         string              `json:"purpose"`
	Issuer          string              `json:"issuer"`
	Audience        string              `json:"audience"`
	OriginProductID string              `json:"origin_product_id"`
	InstanceID      string              `json:"instance_id"`
	ActorID         string              `json:"actor_id"`
	AccountID       string              `json:"account_id"`
	AccessVersion   int64               `json:"access_version"`
	Model           string              `json:"model"`
	WireOperation   string              `json:"wire_operation"`
	PolicyDigest    string              `json:"policy_digest"`
	RouteGeneration string              `json:"route_generation"`
	EmbeddingSpace  *dto.EmbeddingSpace `json:"embedding_space,omitempty"`
	IssuedAt        int64               `json:"issued_at"`
	ExpiresAt       int64               `json:"expires_at"`
}
type KnowledgePaidRoute struct {
	Version                int                 `json:"version"`
	Issuer                 string              `json:"issuer"`
	Audience               string              `json:"audience"`
	OriginProductID        string              `json:"origin_product_id"`
	InstanceID             string              `json:"instance_id"`
	ActorID                string              `json:"actor_id"`
	AccountID              string              `json:"account_id"`
	TaskID                 string              `json:"task_id"`
	AuthorizedCredentialID string              `json:"authorized_credential_id"`
	Model                  string              `json:"model"`
	WireOperation          string              `json:"wire_operation"`
	PolicyDigest           string              `json:"policy_digest"`
	RouteGeneration        string              `json:"route_generation"`
	EmbeddingSpace         *dto.EmbeddingSpace `json:"embedding_space,omitempty"`
	IssuedAt               int64               `json:"issued_at"`
	ExpiresAt              int64               `json:"expires_at"`
}

func OriginalKnowledgeActor(token string) bool {
	if token == "" || len(token) > 8192 || strings.ContainsAny(token, " \t\r\n,") {
		return false
	}
	for _, prefix := range []string{"uh_model_", "uh_key_", "upa_", "ubp_"} {
		if strings.HasPrefix(token, prefix) {
			return false
		}
	}
	return true
}

// Exactly these two private fixed paths are available, with captured original
// bearer; client.do is intentionally not used (it installs a service bearer).
func (a *Adapter) ReadKnowledgeIndexAuthority(ctx context.Context, original, account string) (IndexReadAuthority, error) {
	denied := &remoteError{status: 403}
	if a == nil || !a.Enabled() || a.client == nil || (!OriginalKnowledgeActor(original) || original == a.client.token) || !stableID.MatchString(account) {
		return IndexReadAuthority{}, denied
	}
	issuer := strings.TrimSuffix(a.config.Origin, "/")
	if issuer == "" {
		return IndexReadAuthority{}, ErrPending
	}
	read := func(path string, out any) error {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.client.origin+path, nil)
		if err != nil {
			return ErrPending
		}
		req.Header.Set("Authorization", "Bearer "+original)
		req.Header.Set("Accept", "application/json")
		h := *a.client.http
		h.Jar = nil
		h.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		resp, err := h.Do(req)
		if err != nil {
			return ErrPending
		}
		defer resp.Body.Close()
		if resp.StatusCode == 400 || resp.StatusCode == 401 || resp.StatusCode == 403 {
			return denied
		}
		media, _, e := mime.ParseMediaType(resp.Header.Get("Content-Type"))
		if resp.StatusCode != 200 || e != nil || media != "application/json" {
			return ErrPending
		}
		raw, err := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
		if err != nil || len(raw) > 1<<20 || common.UnmarshalUnique(raw, out) != nil {
			return ErrPending
		}
		return nil
	}
	var accounts []struct {
		ID string `json:"id"`
	}
	if err := read("/v1/accounts", &accounts); err != nil {
		return IndexReadAuthority{}, err
	}
	count := 0
	for _, candidate := range accounts {
		if candidate.ID == account {
			count++
		}
	}
	if count != 1 {
		return IndexReadAuthority{}, denied
	}
	var access struct {
		Issuer     string `json:"issuer"`
		Subject    string `json:"subject"`
		ProductID  string `json:"product_id"`
		InstanceID string `json:"instance_id"`
		User       struct {
			ID string `json:"id"`
		} `json:"user"`
		Permissions []string `json:"permissions"`
		Version     *int64   `json:"version"`
		Active      bool     `json:"active"`
		ExpiresAt   *int64   `json:"expires_at"`
	}
	if err := read("/v1/me/product-access", &access); err != nil {
		return IndexReadAuthority{}, err
	}
	if access.Version == nil || access.ExpiresAt == nil {
		return IndexReadAuthority{}, ErrPending
	}
	if !access.Active || !stableID.MatchString(access.User.ID) || access.Subject != access.User.ID || access.Issuer != issuer || (access.ProductID != "superwagie" && access.ProductID != "llmrouter") || access.InstanceID != a.config.InstanceID || *access.Version < 0 || *access.ExpiresAt <= time.Now().Unix() || !slices.Contains(access.Permissions, "product:use") {
		return IndexReadAuthority{}, denied
	}
	return IndexReadAuthority{ActorID: access.User.ID, AccountID: account, OriginProductID: access.ProductID, InstanceID: access.InstanceID, AccessVersion: *access.Version, expiresAt: *access.ExpiresAt}, nil
}
func (a *Adapter) ReadCurrentKnowledgeRoute(ctx context.Context, original, account, name, wire, pin string) (KnowledgeIndexRoute, error) {
	if !opaqueKnowledgeID(pin) || !ApprovedKnowledgePair(name, wire) {
		return KnowledgeIndexRoute{}, ErrInvalid
	}
	authority, err := a.ReadKnowledgeIndexAuthority(ctx, original, account)
	if err != nil {
		return KnowledgeIndexRoute{}, err
	}
	var binding ManagedBinding
	err = a.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var e error
		binding, e = a.ResolveManagedBinding(tx, authority.OriginProductID, name, wire, pin)
		return e
	})
	if err != nil {
		return KnowledgeIndexRoute{}, err
	}
	now := time.Now().Unix()
	expiry := min(authority.expiresAt, now+30)
	if expiry <= now || ctx.Err() != nil {
		return KnowledgeIndexRoute{}, &remoteError{status: 403}
	}
	return KnowledgeIndexRoute{Version: 1, Purpose: "index_read", Issuer: "llmrouter-knowledge-v1", Audience: authority.OriginProductID, OriginProductID: authority.OriginProductID, InstanceID: authority.InstanceID, ActorID: authority.ActorID, AccountID: authority.AccountID, AccessVersion: authority.AccessVersion, Model: name, WireOperation: wire, PolicyDigest: binding.PolicyDigest, RouteGeneration: binding.RouteGeneration, EmbeddingSpace: binding.EmbeddingSpace, IssuedAt: now, ExpiresAt: expiry}, nil
}
func (a *Adapter) verifyKnowledgeDispatch(ctx context.Context, actor, credential, task, name, wire string) (knowledgeReadGrant, error) {
	var grant knowledgeReadGrant
	if a == nil || !a.Enabled() || !OriginalKnowledgeActor(actor) || !strings.HasPrefix(credential, "uh_model_") || !IsModelCredential(credential) || !ApprovedKnowledgePair(name, wire) {
		return grant, &remoteError{status: 403}
	}
	err := readKnowledgeAuthority(ctx, a.client, http.MethodPost, "/internal/v1/model-credentials/verify", map[string]string{"token": credential, "model": name, "wire_operation": wire, "instance_id": a.config.InstanceID, "purpose": "knowledge_dispatch"}, &grant, actor)
	if err != nil {
		return grant, err
	}
	if a.validateModelCredential(credential, task, name, grant.Claims) != nil || grant.CredentialKind != "task" || grant.InstanceID != a.config.InstanceID || grant.ActorExpiresAt <= time.Now().Unix() || !allowsWire(grant.Claims, name, wire) {
		return grant, &remoteError{status: 403}
	}
	return grant, nil
}
func (a *Adapter) PaidKnowledgeRoute(ctx context.Context, actor, credential, task, name, wire string) (KnowledgePaidRoute, error) {
	grant, err := a.verifyKnowledgeDispatch(ctx, actor, credential, task, name, wire)
	if err != nil {
		return KnowledgePaidRoute{}, err
	}
	var b ManagedBinding
	err = a.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var e error
		b, e = a.ResolveManagedBinding(tx, grant.OriginProductID, name, wire, "")
		return e
	})
	if err != nil {
		return KnowledgePaidRoute{}, err
	}
	now := time.Now().Unix()
	expiry := min(now+60, grant.ActorExpiresAt, grant.ExpiresAt.Unix())
	if expiry <= now || ctx.Err() != nil {
		return KnowledgePaidRoute{}, &remoteError{status: 403}
	}
	return KnowledgePaidRoute{Version: 1, Issuer: "llmrouter-knowledge-v1", Audience: grant.OriginProductID, OriginProductID: grant.OriginProductID, InstanceID: b.InstanceID, ActorID: grant.ActorID, AccountID: grant.AccountID, TaskID: task, AuthorizedCredentialID: grant.ID, Model: name, WireOperation: wire, PolicyDigest: b.PolicyDigest, RouteGeneration: b.RouteGeneration, EmbeddingSpace: b.EmbeddingSpace, IssuedAt: now, ExpiresAt: expiry}, nil
}
