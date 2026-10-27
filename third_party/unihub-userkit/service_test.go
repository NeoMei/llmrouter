package userkit

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type verifiedProvider struct {
	access Access
	err    error
}

func (p verifiedProvider) Resolve(context.Context, string) (Access, error) { return p.access, p.err }

// This provisioner simulates the product transaction boundary: initializer and
// identity insert commit atomically under its lock, keyed by issuer+subject.
type transactionalProvisioner struct {
	mu          sync.Mutex
	users       map[string]LocalUser
	initialized int
	fail        bool
}

func (p *transactionalProvisioner) Ensure(_ context.Context, a Access) (LocalUser, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	key := a.Issuer + "|" + a.Subject
	if u, ok := p.users[key]; ok {
		return u, nil
	}
	if p.fail {
		return LocalUser{}, errors.New("initializer failed")
	}
	u := LocalUser{ID: "local-1"}
	p.users[key] = u
	p.initialized++
	return u, nil
}

type allowPolicy struct{}

func (allowPolicy) Allow(context.Context, Principal, string, string) error { return nil }
func validAccess() Access {
	return Access{Issuer: "https://hub.test", Subject: "central-1", ProductID: "notes", InstanceID: "default", User: User{ID: "central-1"}, Roles: []string{"member"}, Permissions: []string{"product:use"}, Version: 1, Active: true, ExpiresAt: time.Now().Add(time.Hour).Unix()}
}
func TestRepeatedConcurrentAuthenticationInitializesOnce(t *testing.T) {
	p := &transactionalProvisioner{users: map[string]LocalUser{}}
	s, e := NewService(verifiedProvider{access: validAccess()}, p, nil)
	if e != nil {
		t.Fatal(e)
	}
	var wg sync.WaitGroup
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v, e := s.Authenticate(context.Background(), "verified-bearer")
			if e != nil || v.LocalUser.ID != "local-1" || !v.HasPermission("product:use") {
				t.Errorf("principal %#v %v", v, e)
			}
		}()
	}
	wg.Wait()
	if p.initialized != 1 {
		t.Fatalf("initialized %d", p.initialized)
	}
}
func TestFailureAndCentralDenyNeverYieldUsableIdentity(t *testing.T) {
	p := &transactionalProvisioner{users: map[string]LocalUser{}, fail: true}
	s, _ := NewService(verifiedProvider{access: validAccess()}, p, allowPolicy{})
	v, e := s.Authenticate(context.Background(), "bearer")
	if e == nil || v.LocalUser.ID != "" || len(p.users) != 0 {
		t.Fatalf("failed initialization leaked identity %#v %v", v, e)
	}
	a := validAccess()
	a.Active = false
	s, _ = NewService(verifiedProvider{access: a}, p, allowPolicy{})
	if _, e = s.Authenticate(context.Background(), "bearer"); !errors.Is(e, ErrDenied) {
		t.Fatal(e)
	}
	v = Principal{LocalUser: LocalUser{ID: "local"}, Access: a}
	if v.HasPermission("product:use") || v.RequirePermission(context.Background(), "product:use", "") == nil {
		t.Fatal("central denial overridden")
	}
	s, _ = NewService(verifiedProvider{err: ErrDenied}, p, allowPolicy{})
	if _, e = s.Authenticate(context.Background(), "bearer"); !errors.Is(e, ErrDenied) {
		t.Fatal(e)
	}
}

type denyPolicy struct{}

func (denyPolicy) Allow(context.Context, Principal, string, string) error { return ErrDenied }
func TestProductResourcePolicyCanNarrowButCannotBroadenCentralPermissions(t *testing.T) {
	p := &transactionalProvisioner{users: map[string]LocalUser{}}
	s, _ := NewService(verifiedProvider{access: validAccess()}, p, denyPolicy{})
	principal, e := s.Authenticate(context.Background(), "bearer")
	if e != nil {
		t.Fatal(e)
	}
	if principal.HasPermission("product:use") || principal.RequirePermission(context.Background(), "product:use", "document-1") == nil {
		t.Fatal("product resource deny ignored")
	}
	s, _ = NewService(verifiedProvider{access: validAccess()}, p, allowPolicy{})
	principal, e = s.Authenticate(context.Background(), "bearer")
	if e != nil {
		t.Fatal(e)
	}
	if principal.HasPermission("channel:write") || principal.RequirePermission(context.Background(), "channel:write", "channel-1") == nil {
		t.Fatal("product policy broadened central permission")
	}
}
