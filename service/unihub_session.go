package service

import (
	"context"
	userkit "github.com/NeoMei/UniHub/packages/userkit-go"
	"github.com/QuantumNous/new-api/internal/platformaccount"
	"github.com/QuantumNous/new-api/model"
	"strconv"
)

// ResolveUniHubSession refreshes central authority on every request, with no
// allow-cache and no native-role fallback. The bearer never leaves server code.
func ResolveUniHubSession(ctx context.Context, identity AuthIdentity) (userkit.Principal, string, error) {
	session, user, e := ValidateLoginSession(identity)
	if e != nil || session.LoginMethod != "unihub" || user.IdentitySource != model.UniHubIdentitySource {
		return userkit.Principal{}, "", userkit.ErrDenied
	}
	c, e := platformaccount.Current()
	if e != nil {
		return userkit.Principal{}, "", e
	}
	binding, bearer, e := model.OpenUniHubSession(identity.SessionID, identity.UserID, c)
	if e != nil {
		return userkit.Principal{}, "", e
	}
	provider, e := c.Provider()
	if e != nil {
		return userkit.Principal{}, "", e
	}
	kit, e := userkit.NewService(provider, model.UniHubProvisioner{}, nil)
	if e != nil {
		return userkit.Principal{}, "", e
	}
	p, e := kit.Authenticate(ctx, bearer)
	if e != nil {
		return userkit.Principal{}, "", e
	}
	if p.Access.Subject != binding.Subject || p.LocalUser.ID != strconv.Itoa(identity.UserID) {
		return userkit.Principal{}, "", userkit.ErrDenied
	}
	return p, bearer, nil
}
