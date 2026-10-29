package service

import (
	"context"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"time"
)

// CreateNativeLoginSessionAtAuthVersion is called only after a native credential
// has authenticated at this exact local auth version; method is audit metadata.
func CreateNativeLoginSessionAtAuthVersion(userID int, version int64, method, ip, userAgent string) (*AuthBundle, error) {
	user, err := model.NativeRealmUser(context.Background(), userID)
	if err != nil {
		return nil, err
	}
	if version <= 0 || user.AuthVersion != version || user.Status != common.UserStatusEnabled {
		return nil, ErrLoginSessionRevoked
	}
	return createLoginSession(userID, version, method, ip, userAgent, 0, model.NativeFundingLocalOnly)
}

// IsNativeLoginSession reads the persisted server issuance decision, not method
// or mapping. Existing SIDs never become native when their owner is enrolled.
func IsNativeLoginSession(ctx context.Context, identity AuthIdentity) (bool, error) {
	if identity.SessionID == "" {
		return false, nil
	}
	session, err := model.GetUserSessionBySID(identity.SessionID)
	if err != nil {
		return false, err
	}
	if session.AuthRealm == "" {
		return false, nil
	}
	if session.AuthRealm != model.NativeFundingLocalOnly || session.UserID != identity.UserID || session.Version != identity.SessionVersion || session.UserAuthVersion != identity.UserAuthVersion || session.Status != model.UserSessionStatusActive || session.ExpiresAt <= time.Now().Unix() || session.RevokedAt != 0 {
		return false, ErrLoginSessionRevoked
	}
	user, err := model.NativeRealmUser(ctx, identity.UserID)
	if err != nil {
		return false, err
	}
	if user.Status != common.UserStatusEnabled || user.AuthVersion != identity.UserAuthVersion {
		return false, ErrLoginSessionRevoked
	}
	return true, nil
}
