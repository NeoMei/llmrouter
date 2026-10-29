package service

import (
	"context"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNativeRealmSessionRequiresExplicitServerIssuance(t *testing.T) {
	user := setupAuthSessionTestDB(t)
	require.NoError(t, model.RegisterProductFundingModels(model.DB))
	require.NoError(t, model.DB.Model(user).Updates(map[string]any{"funding_realm": model.NativeFundingLocalOnly, "funding_realm_evidence": "reviewed", "native_password": true}).Error)
	old, err := CreateLoginSession(user.Id, "password", "", "")
	require.NoError(t, err)
	oldIdentity, err := ParseAccessToken(old.AccessToken)
	require.NoError(t, err)
	native, err := IsNativeLoginSession(context.Background(), oldIdentity)
	require.NoError(t, err)
	assert.False(t, native)
	bundle, err := CreateNativeLoginSessionAtAuthVersion(user.Id, user.AuthVersion, "password", "", "")
	require.NoError(t, err)
	identity, err := ParseAccessToken(bundle.AccessToken)
	require.NoError(t, err)
	native, err = IsNativeLoginSession(context.Background(), identity)
	require.NoError(t, err)
	assert.True(t, native)
	refreshed, _, err := RefreshLoginSession(bundle.RefreshToken, identity.SessionID, "", "")
	require.NoError(t, err)
	current, err := ParseAccessToken(refreshed.AccessToken)
	require.NoError(t, err)
	native, err = IsNativeLoginSession(context.Background(), current)
	require.NoError(t, err)
	assert.True(t, native)

	// Realm-scoped listing/revocation cannot cross the shared owner.
	sessions, err := ListLoginSessions(user.Id, identity.SessionID, model.NativeFundingLocalOnly)
	require.NoError(t, err)
	require.Len(t, sessions, 1)
	assert.Equal(t, identity.SessionID, sessions[0].SID)
	sessions, err = ListLoginSessions(user.Id, oldIdentity.SessionID, "")
	require.NoError(t, err)
	require.Len(t, sessions, 1)
	assert.Equal(t, oldIdentity.SessionID, sessions[0].SID)
	revoked, err := model.RevokeUserSession(user.Id, identity.SessionID, "central_attempt", "")
	require.NoError(t, err)
	assert.False(t, revoked)
	count, err := model.RevokeOtherUserSessions(user.Id, oldIdentity.SessionID, "central_others", "")
	require.NoError(t, err)
	assert.Zero(t, count)
	_, err = model.GetUserSessionCached(identity.SessionID)
	require.NoError(t, err)
	revoked, err = model.RevokeUserSession(user.Id, oldIdentity.SessionID, "native_attempt", model.NativeFundingLocalOnly)
	require.NoError(t, err)
	assert.False(t, revoked)
	require.NoError(t, model.DB.Model(user).Update("status", common.UserStatusDisabled).Error)
	_, err = IsNativeLoginSession(context.Background(), current)
	require.Error(t, err)
}
