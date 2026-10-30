package service

import (
	"context"
	"errors"

	userkit "github.com/NeoMei/UniHub/packages/userkit-go"
	"github.com/QuantumNous/new-api/internal/platformaccount"
	"github.com/QuantumNous/new-api/model"
)

var ErrBrokerSourceUnavailable = errors.New("central browser source unavailable")

// VerifyBrokerSource rechecks the central session that authorized a broker SID.
// Independent native and ordinary OIDC sessions keep their own authority path.
func VerifyBrokerSource(ctx context.Context, identity AuthIdentity) error {
	_, err := VerifyBrokerSourceWithExpiry(ctx, identity)
	return err
}

// VerifyBrokerSourceWithExpiry also exposes the authenticated source deadline.
// Zero means that the session is not broker-derived.
func VerifyBrokerSourceWithExpiry(ctx context.Context, identity AuthIdentity) (int64, error) {
	session, user, err := ValidateLoginSession(identity)
	if err != nil {
		return 0, err
	}
	if session.LoginMethod != "product_broker" {
		return 0, nil
	}
	if !platformaccount.RuntimeEnabled() {
		return 0, ErrBrokerSourceUnavailable
	}
	config, err := platformaccount.Current()
	if err != nil {
		return 0, ErrBrokerSourceUnavailable
	}
	bound, proof, err := model.OpenBrokerSourceProof(identity.SessionID, identity.UserID, user.UniHubUserID, config)
	if err != nil {
		if errors.Is(err, userkit.ErrDenied) {
			return 0, ErrLoginSessionRevoked
		}
		return 0, ErrBrokerSourceUnavailable
	}
	client, err := platformaccount.RuntimeClient()
	if err != nil {
		return 0, ErrBrokerSourceUnavailable
	}
	if err = client.VerifyProductBrowserSource(ctx, proof, user.UniHubUserID); err != nil {
		// The central endpoint does not currently distinguish source revocation
		// from service authentication, permission, or database failures.
		return 0, ErrBrokerSourceUnavailable
	}
	if _, _, err = ValidateLoginSession(identity); err != nil {
		return 0, err
	}
	current, currentProof, err := model.OpenBrokerSourceProof(identity.SessionID, identity.UserID, user.UniHubUserID, config)
	if err != nil {
		if errors.Is(err, userkit.ErrDenied) {
			return 0, ErrLoginSessionRevoked
		}
		return 0, ErrBrokerSourceUnavailable
	}
	if current != bound || currentProof != proof {
		return 0, ErrBrokerSourceUnavailable
	}
	return current.ExpiresAt, nil
}
