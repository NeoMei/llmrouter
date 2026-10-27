package userkit

import (
	"context"
	"net/url"
)

// ProductIdentity maps a mature product's stable subject to the central identity.
// AuthVersion belongs to the product authenticator; Access is always central.
type ProductIdentity struct {
	Binding        RuntimeBinding `json:"binding"`
	Access         Access         `json:"access"`
	ProductRole    int            `json:"product_role"`
	AuthVersion    int64          `json:"auth_version"`
	ActorToken     string         `json:"actor_token,omitempty"`
	ActorExpiresAt int64          `json:"actor_expires_at,omitempty"`
}
type ProductIdentityRegistration struct {
	LocalUserID   string `json:"local_user_id"`
	OperationID   string `json:"operation_id"`
	Username      string `json:"username"`
	DisplayName   string `json:"display_name"`
	Email         string `json:"email"`
	EmailVerified bool   `json:"email_verified"`
	AuthVersion   int64  `json:"auth_version"`
}
type ProductAuthentication struct {
	LocalUserID      string `json:"local_user_id"`
	AuthenticationID string `json:"authentication_id"`
	Method           string `json:"method"`
	AuthenticatedAt  int64  `json:"authenticated_at"`
	AuthVersion      int64  `json:"auth_version"`
}
type ProductIdentityMutation struct {
	OperationID         string                     `json:"operation_id"`
	ExpectedVersion     int64                      `json:"expected_version"`
	Active              *bool                      `json:"active,omitempty"`
	ProductRole         *int                       `json:"product_role,omitempty"`
	PermissionOverrides map[string]map[string]bool `json:"permission_overrides"`
	NextAuthVersion     *int64                     `json:"next_auth_version,omitempty"`
	Event               string                     `json:"event"`
}
type ProductIdentityImport struct {
	CentralUserID string `json:"central_user_id,omitempty"`
	ProductIdentityRegistration
	ReviewedBatchID     string                     `json:"reviewed_batch_id"`
	SnapshotDigest      string                     `json:"snapshot_digest"`
	ProductRole         int                        `json:"product_role"`
	Active              bool                       `json:"active"`
	PermissionOverrides map[string]map[string]bool `json:"permission_overrides"`
}

// ProductLifecycle only advances the trusted authenticator version. It cannot
// create, enable or authorize an identity, even for a privileged product caller.
type ProductLifecycle struct {
	LocalUserID         string `json:"local_user_id"`
	OperationID         string `json:"operation_id"`
	PreviousAuthVersion int64  `json:"previous_auth_version"`
	NextAuthVersion     int64  `json:"next_auth_version"`
}
type ProductLifecycleReceipt struct {
	AuthVersion int64 `json:"auth_version"`
}

func (c *RuntimeClient) SyncProductLifecycle(ctx context.Context, in ProductLifecycle) (out ProductLifecycleReceipt, e error) {
	e = c.request(ctx, "POST", "/identity/lifecycle", "", in, &out)
	if e == nil && out.AuthVersion != in.NextAuthVersion {
		e = ErrDenied
	}
	return
}

func (c *RuntimeClient) validateIdentity(local string, out ProductIdentity) error {
	if e := c.scope(out.Binding.ProductID, out.Binding.InstanceID); e != nil {
		return e
	}
	if out.Access.Subject != out.Binding.UserID || out.Access.User.ID != out.Binding.UserID || out.Access.ProductID != out.Binding.ProductID || out.Access.InstanceID != out.Binding.InstanceID || out.Access.Issuer == "" || out.Binding.LocalUserID != local || out.Binding.UserID == "" || out.AuthVersion < 1 || (out.ProductRole != 1 && out.ProductRole != 10 && out.ProductRole != 100) {
		return ErrDenied
	}
	return nil
}
func (c *RuntimeClient) RegisterProductIdentity(ctx context.Context, in ProductIdentityRegistration) (out ProductIdentity, e error) {
	e = c.request(ctx, "POST", "/identity/register", "", in, &out)
	if e == nil {
		e = c.validateIdentity(in.LocalUserID, out)
	}
	return
}
func (c *RuntimeClient) AuthenticateProductIdentity(ctx context.Context, in ProductAuthentication) (out ProductIdentity, e error) {
	e = c.request(ctx, "POST", "/identity/authenticate", "", in, &out)
	if e == nil {
		e = c.validateIdentity(in.LocalUserID, out)
	}
	return
}
func (c *RuntimeClient) ProductIdentity(ctx context.Context, localID string) (out ProductIdentity, e error) {
	e = c.request(ctx, "GET", "/identity/"+url.PathEscape(localID), "", nil, &out)
	if e == nil {
		e = c.validateIdentity(localID, out)
	}
	return
}
func (c *RuntimeClient) MutateProductIdentity(ctx context.Context, actor, localID string, in ProductIdentityMutation) (out ProductIdentity, e error) {
	e = c.request(ctx, "PATCH", "/identity/"+url.PathEscape(localID), actor, in, &out)
	if e == nil {
		e = c.validateIdentity(localID, out)
	}
	return
}
func (c *RuntimeClient) ImportProductIdentity(ctx context.Context, in ProductIdentityImport) (out ProductIdentity, e error) {
	e = c.request(ctx, "POST", "/identity/import", "", in, &out)
	if e == nil {
		e = c.validateIdentity(in.LocalUserID, out)
	}
	return
}
