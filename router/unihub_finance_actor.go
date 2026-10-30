package router

import (
	"context"
	"errors"
	"regexp"
	"strconv"
	"time"

	userkit "github.com/NeoMei/UniHub/packages/userkit-go"
	"github.com/QuantumNous/new-api/internal/platformaccount"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
)

var errRuntimeFinanceAuthority = errors.New("runtime finance authority unavailable")
var runtimeActorToken = regexp.MustCompile(`^upa_[0-9a-f]{64}$`)

// runtimeFinanceActor derives a short-lived central actor from a current local
// session. It never substitutes a service credential for user authority.
func runtimeFinanceActor(ctx context.Context, identity service.AuthIdentity, principal userkit.Principal, config platformaccount.Config) (userkit.ProductIdentity, error) {
	if !platformaccount.RuntimeEnabled() || identity.UserID <= 0 || principal.LocalUser.ID != strconv.Itoa(identity.UserID) || !principal.HasPermission("product:use") {
		return userkit.ProductIdentity{}, errRuntimeFinanceAuthority
	}
	if principal.Access.Subject == "" || principal.Access.User.ID != principal.Access.Subject || principal.Access.Issuer != config.PublicOrigin || principal.Access.ProductID != config.ProductID || principal.Access.InstanceID != config.InstanceID {
		return userkit.ProductIdentity{}, errRuntimeFinanceAuthority
	}
	if err := model.SyncProductAuthVersions(ctx, identity.UserID); err != nil {
		return userkit.ProductIdentity{}, errRuntimeFinanceAuthority
	}
	session, _, err := service.ValidateLoginSession(identity)
	if err != nil {
		return userkit.ProductIdentity{}, errRuntimeFinanceAuthority
	}
	if session.AuthRealm == model.NativeFundingLocalOnly {
		return userkit.ProductIdentity{}, errRuntimeFinanceAuthority
	}
	if session.LoginMethod == "product_broker" {
		if err := service.VerifyBrokerSource(ctx, identity); err != nil {
			return userkit.ProductIdentity{}, errRuntimeFinanceAuthority
		}
	}
	projected, err := model.CheckUniHubProjection(identity.UserID)
	if err != nil || !projected {
		return userkit.ProductIdentity{}, errRuntimeFinanceAuthority
	}
	if err := model.VerifyUniHubIdentityBinding(identity.UserID, principal.Access.Issuer, principal.Access.Subject); err != nil {
		return userkit.ProductIdentity{}, errRuntimeFinanceAuthority
	}
	current, err := platformaccount.AuthenticateLocal(ctx, strconv.Itoa(identity.UserID), identity.UserAuthVersion, "session")
	now := time.Now().Unix()
	if err != nil || current.AuthVersion != identity.UserAuthVersion || current.Binding.UserID != principal.Access.Subject || current.Access.Subject != principal.Access.Subject || current.Access.User.ID != principal.Access.Subject {
		return userkit.ProductIdentity{}, errRuntimeFinanceAuthority
	}
	if current.Access.Issuer != config.PublicOrigin || current.Access.ProductID != config.ProductID || current.Access.InstanceID != config.InstanceID || !(userkit.Principal{LocalUser: principal.LocalUser, Access: current.Access}).HasPermission("product:use") {
		return userkit.ProductIdentity{}, errRuntimeFinanceAuthority
	}
	if !runtimeActorToken.MatchString(current.ActorToken) || current.ActorExpiresAt <= now || current.ActorExpiresAt > now+130 {
		return userkit.ProductIdentity{}, errRuntimeFinanceAuthority
	}
	if _, _, err := service.ValidateLoginSession(identity); err != nil {
		return userkit.ProductIdentity{}, errRuntimeFinanceAuthority
	}
	if err := model.VerifyUniHubIdentityBinding(identity.UserID, current.Access.Issuer, current.Access.Subject); err != nil {
		return userkit.ProductIdentity{}, errRuntimeFinanceAuthority
	}
	if err := service.VerifyBrokerSource(ctx, identity); err != nil {
		return userkit.ProductIdentity{}, errRuntimeFinanceAuthority
	}
	return current, nil
}
