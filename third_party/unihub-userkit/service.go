// Package userkit proves current product access and provisions a product-owned identity.
package userkit

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"
)

var ErrDenied = errors.New("product access denied")
var ErrInvalid = errors.New("invalid product identity")

type User struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Email string `json:"email"`
}
type Access struct {
	ProductRole         int                        `json:"product_role"`
	PermissionOverrides map[string]map[string]bool `json:"permission_overrides,omitempty"`
	Issuer              string                     `json:"issuer"`
	Subject             string                     `json:"subject"`
	ProductID           string                     `json:"product_id"`
	InstanceID          string                     `json:"instance_id"`
	User                User                       `json:"user"`
	Roles               []string                   `json:"roles"`
	Permissions         []string                   `json:"permissions"`
	Version             int64                      `json:"version"`
	Active              bool                       `json:"active"`
	ExpiresAt           int64                      `json:"expires_at"`
}
type Provider interface {
	Resolve(context.Context, string) (Access, error)
}
type LocalUser struct{ ID string }

// Provisioner must atomically initialize product resources and ensure a stable
// local identity keyed by Access.Issuer + Access.Subject. Ensure must be safe for
// repeated and concurrent calls; a failed initialization must roll back identity.
// Central roles must never become permanent elevated product roles.
type Provisioner interface {
	Ensure(context.Context, Access) (LocalUser, error)
}
type ResourcePolicy interface {
	Allow(context.Context, Principal, string, string) error
}
type Principal struct {
	LocalUser LocalUser
	Access    Access
	policy    ResourcePolicy
}

func (p Principal) HasPermission(permission string) bool {
	return p.RequirePermission(context.Background(), permission, "") == nil
}
func (p Principal) RequirePermission(ctx context.Context, permission, resource string) error {
	if p.LocalUser.ID == "" || !p.Access.Active || p.Access.ExpiresAt <= time.Now().Unix() || !slices.Contains(p.Access.Permissions, permission) {
		return ErrDenied
	}
	if p.policy != nil {
		return p.policy.Allow(ctx, p, permission, resource)
	}
	return nil
}

type Service struct {
	provider    Provider
	provisioner Provisioner
	policy      ResourcePolicy
}

func NewService(provider Provider, provisioner Provisioner, policy ResourcePolicy) (*Service, error) {
	if provider == nil || provisioner == nil {
		return nil, ErrInvalid
	}
	return &Service{provider, provisioner, policy}, nil
}
func (s *Service) Authenticate(ctx context.Context, bearer string) (Principal, error) {
	a, e := s.provider.Resolve(ctx, bearer)
	if e != nil {
		return Principal{}, e
	}
	if e = validate(a); e != nil {
		return Principal{}, e
	}
	u, e := s.provisioner.Ensure(ctx, a)
	if e != nil {
		return Principal{}, e
	}
	if strings.TrimSpace(u.ID) == "" {
		return Principal{}, ErrInvalid
	}
	return Principal{LocalUser: u, Access: a, policy: s.policy}, nil
}
func validate(a Access) error {
	if !a.Active || a.ExpiresAt <= time.Now().Unix() {
		return ErrDenied
	}
	for _, key := range []string{a.Issuer, a.Subject, a.ProductID, a.InstanceID, a.User.ID} {
		if key == "" || strings.TrimSpace(key) != key {
			return ErrInvalid
		}
	}
	if a.Subject != a.User.ID || a.Version < 1 {
		return ErrInvalid
	}
	return nil
}
