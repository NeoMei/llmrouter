package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"strconv"
	"strings"

	userkit "github.com/NeoMei/UniHub/packages/userkit-go"
	"github.com/QuantumNous/new-api/internal/platformaccount"
	"github.com/QuantumNous/new-api/model"
)

// CreateCentralSessionFromVerifiedSource accepts only Access returned by a
// completed central browser exchange. A separate authenticated runtime lookup
// must resolve the same immutable subject and product binding.
func CreateCentralSessionFromVerifiedSource(ctx context.Context, userID int, source userkit.Access, method string, expiresAt int64, ip, agent string) (*AuthBundle, error) {
	if os.Getenv("UNIHUB_INDEPENDENT_CENTRAL_REALM_ENABLED") != "true" {
		return nil, ErrBrokerSourceUnavailable
	}
	config, err := platformaccount.Current()
	if err != nil || !platformaccount.RuntimeEnabled() || source.Issuer != config.PublicOrigin || source.ProductID != config.ProductID || source.InstanceID != config.InstanceID {
		return nil, ErrBrokerSourceUnavailable
	}
	identity, err := platformaccount.IdentityForLocal(ctx, strconv.Itoa(userID))
	if err != nil {
		return nil, ErrBrokerSourceUnavailable
	}
	if err = model.RecordCentralAuthState(ctx, userID, identity, source); err != nil {
		return nil, err
	}
	if expiresAt <= 0 || expiresAt > source.ExpiresAt {
		expiresAt = source.ExpiresAt
	}
	return CreateVerifiedCentralLoginSession(userID, identity.AuthVersion, method, expiresAt, ip, agent)
}

// The caller must have completed the external OAuth or WebAuthn assertion.
// This function verifies durable provenance and obtains current central
// authority before minting the credential-bound session.
func CreateCentralCredentialLogin(ctx context.Context, userID int, kind, value, method, ip, agent string) (*AuthBundle, userkit.ProductIdentity, error) {
	var empty userkit.ProductIdentity
	if os.Getenv("UNIHUB_INDEPENDENT_CENTRAL_REALM_ENABLED") != "true" || !platformaccount.RuntimeEnabled() || (method != "passkey" && method != "wechat" && method != "telegram" && !strings.HasPrefix(method, "oauth:")) {
		return nil, empty, ErrBrokerSourceUnavailable
	}
	valid, err := model.IsCentralLoginCredential(ctx, userID, kind, value)
	if err != nil || !valid {
		return nil, empty, model.ErrNativeCredentialUnverified
	}
	state, err := model.ReadCentralAuthState(ctx, userID)
	if err != nil {
		return nil, empty, err
	}
	if err = model.VerifyUniHubIdentityBinding(userID, state.Issuer, state.Subject); err != nil {
		return nil, empty, err
	}
	identity, err := platformaccount.AuthenticateLocal(ctx, strconv.Itoa(userID), state.AuthVersion, method)
	if err != nil {
		return nil, empty, ErrBrokerSourceUnavailable
	}
	access, binding := identity.Access, identity.Binding
	if identity.AuthVersion != state.AuthVersion || binding.ID != state.BindingID || access.Issuer != state.Issuer || access.Subject != state.Subject || access.ProductID != state.ProductID || access.InstanceID != state.InstanceID || !(userkit.Principal{LocalUser: userkit.LocalUser{ID: strconv.Itoa(userID)}, Access: access}).HasPermission("product:use") {
		return nil, empty, model.ErrNativeCredentialUnverified
	}
	bundle, err := createLoginSession(userID, state.AuthVersion, "central_credential", ip, agent, access.ExpiresAt, model.CentralAuthRealm)
	if err != nil {
		return nil, empty, err
	}
	digest := sha256.Sum256([]byte(value))
	if err = model.DB.WithContext(ctx).Create(&model.CentralSessionCredential{SID: bundle.Session.SID, UserID: userID, Kind: kind, Digest: hex.EncodeToString(digest[:])}).Error; err != nil {
		_, _ = model.RevokeUserSession(userID, bundle.Session.SID, "credential_binding_failed")
		return nil, empty, err
	}
	parsed, err := ParseAccessToken(bundle.AccessToken)
	if err == nil {
		_, _, err = ValidateLoginSession(parsed)
	}
	if err != nil {
		_, _ = model.RevokeUserSession(userID, bundle.Session.SID, "credential_binding_failed")
		return nil, empty, err
	}
	return bundle, identity, nil
}

func VerifyCentralRealmAuthority(ctx context.Context, identity AuthIdentity) error {
	state, err := model.ReadCentralAuthState(ctx, identity.UserID)
	if err != nil || !state.Active || state.AuthVersion != identity.UserAuthVersion {
		return ErrBrokerSourceUnavailable
	}
	if err := model.VerifyUniHubIdentityBinding(identity.UserID, state.Issuer, state.Subject); err != nil {
		return ErrBrokerSourceUnavailable
	}
	current, err := platformaccount.IdentityForLocal(ctx, strconv.Itoa(identity.UserID))
	if err != nil || current.AuthVersion != identity.UserAuthVersion || current.Binding.ID != state.BindingID || current.Access.Issuer != state.Issuer || current.Access.Subject != state.Subject || current.Access.ProductID != state.ProductID || current.Access.InstanceID != state.InstanceID || !(userkit.Principal{LocalUser: userkit.LocalUser{ID: strconv.Itoa(identity.UserID)}, Access: current.Access}).HasPermission("product:use") {
		return ErrBrokerSourceUnavailable
	}
	return nil
}
