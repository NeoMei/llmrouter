package service

import (
	"context"
	"crypto/hmac"
	"errors"
	"strconv"
	"strings"
	"time"

	userkit "github.com/NeoMei/UniHub/packages/userkit-go"
	"github.com/QuantumNous/new-api/internal/platformaccount"
	"github.com/QuantumNous/new-api/model"
	"gorm.io/gorm"
)

type SessionOption struct {
	SID              string `json:"sid"`
	Realm            string `json:"realm"`
	DisplayName      string `json:"display_name"`
	PublicProjection bool   `json:"-"`
}

// InspectSessionOption validates an individual refresh secret without rotating
// it. A caller may show only results proved by the corresponding cookie.
func InspectSessionOption(ctx context.Context, raw string, privateMaintenance func(int) bool) (*SessionOption, error) {
	sid, secret, ok := splitRefreshToken(raw)
	if !ok {
		return nil, ErrRefreshTokenInvalid
	}
	session, err := model.GetUserSessionBySID(sid)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrRefreshTokenInvalid
		}
		return nil, ErrAuthStoreUnavailable
	}
	if !hmac.Equal([]byte(session.RefreshHash), []byte(hashRefreshSecret(secret))) {
		return nil, ErrRefreshTokenInvalid
	}
	identity := AuthIdentity{UserID: session.UserID, SessionID: session.SID, SessionVersion: session.Version, UserAuthVersion: session.UserAuthVersion}
	_, user, err := ValidateLoginSession(identity)
	if err != nil {
		return nil, err
	}
	if session.AuthRealm == "" {
		if session.LoginMethod == "product_broker" {
			if err := VerifyBrokerSource(ctx, identity); err != nil {
				return nil, err
			}
		}
		if session.LoginMethod == "unihub" {
			if _, _, err := ResolveUniHubSession(ctx, identity); err != nil {
				return nil, ErrBrokerSourceUnavailable
			}
		}
		maintenance := session.LoginMethod != "unihub" && session.LoginMethod != "product_broker" && privateMaintenance != nil && privateMaintenance(identity.UserID)
		if platformaccount.RuntimeEnabled() && !maintenance {
			current, err := platformaccount.IdentityForLocal(ctx, strconv.Itoa(identity.UserID))
			if err != nil || current.AuthVersion != identity.UserAuthVersion || current.Binding.LocalUserID != strconv.Itoa(identity.UserID) || current.Access.Subject == "" || current.Access.Subject != current.Binding.UserID || current.Access.User.ID != current.Binding.UserID || !(userkit.Principal{LocalUser: userkit.LocalUser{ID: strconv.Itoa(identity.UserID)}, Access: current.Access}).HasPermission("product:use") || current.Access.ExpiresAt <= time.Now().Unix() {
				return nil, ErrBrokerSourceUnavailable
			}
			if err := model.VerifyUniHubIdentityBinding(identity.UserID, current.Access.Issuer, current.Access.Subject); err != nil {
				return nil, ErrBrokerSourceUnavailable
			}
		} else if !maintenance && !platformaccount.RuntimeEnabled() && user.IdentitySource == model.UniHubIdentitySource && session.LoginMethod != "unihub" {
			return nil, ErrBrokerSourceUnavailable
		}
		if session.LoginMethod == "product_broker" {
			if err := VerifyBrokerSource(ctx, identity); err != nil {
				return nil, err
			}
		}
		if maintenance && !privateMaintenance(identity.UserID) {
			return nil, ErrBrokerSourceUnavailable
		}
		if _, _, err := ValidateLoginSession(identity); err != nil {
			return nil, err
		}
		currentSession, err := model.GetUserSessionBySID(sid)
		if err != nil {
			return nil, ErrAuthStoreUnavailable
		}
		if !hmac.Equal([]byte(currentSession.RefreshHash), []byte(hashRefreshSecret(secret))) {
			return nil, ErrRefreshTokenInvalid
		}
		return &SessionOption{SID: sid, Realm: "legacy", DisplayName: user.Username, PublicProjection: (session.LoginMethod == "unihub" || session.LoginMethod == "product_broker") && user.IdentitySource == model.UniHubIdentitySource}, nil
	}
	if session.AuthRealm == model.NativeFundingLocalOnly {
		native, err := IsNativeLoginSession(ctx, identity)
		if err != nil {
			return nil, err
		}
		if !native {
			return nil, ErrLoginSessionRevoked
		}
		return &SessionOption{SID: sid, Realm: "native", DisplayName: user.Username}, nil
	}
	if session.AuthRealm != model.CentralAuthRealm {
		return nil, ErrLoginSessionRevoked
	}
	if err := VerifyBrokerSource(ctx, identity); err != nil {
		return nil, err
	}
	if session.LoginMethod == "unihub" {
		if _, _, err := ResolveUniHubSession(ctx, identity); err != nil {
			return nil, ErrBrokerSourceUnavailable
		}
	} else if session.LoginMethod != "product_broker" && session.LoginMethod != "central_credential" {
		return nil, ErrLoginSessionRevoked
	}
	state, err := model.ReadCentralAuthState(ctx, identity.UserID)
	if err != nil {
		return nil, ErrAuthStoreUnavailable
	}
	current, err := platformaccount.IdentityForLocal(ctx, strconv.Itoa(identity.UserID))
	if err != nil {
		return nil, ErrBrokerSourceUnavailable
	}
	if current.AuthVersion != identity.UserAuthVersion || current.Binding.ID != state.BindingID || current.Access.Issuer != state.Issuer || current.Access.Subject != state.Subject || current.Access.ProductID != state.ProductID || current.Access.InstanceID != state.InstanceID || !(userkit.Principal{LocalUser: userkit.LocalUser{ID: strconv.Itoa(identity.UserID)}, Access: current.Access}).HasPermission("product:use") {
		return nil, ErrBrokerSourceUnavailable
	}
	if err := model.VerifyUniHubIdentityBinding(identity.UserID, state.Issuer, state.Subject); err != nil {
		return nil, ErrBrokerSourceUnavailable
	}
	name := strings.TrimSpace(current.Access.User.Name)
	if name == "" {
		name = current.Access.Subject
	}
	if err := VerifyBrokerSource(ctx, identity); err != nil {
		return nil, err
	}
	if _, _, err := ValidateLoginSession(identity); err != nil {
		return nil, err
	}
	if current.Access.ExpiresAt <= time.Now().Unix() {
		return nil, ErrBrokerSourceUnavailable
	}
	return &SessionOption{SID: sid, Realm: "central", DisplayName: name}, nil
}
