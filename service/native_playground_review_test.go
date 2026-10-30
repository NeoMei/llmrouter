package service

import (
	"context"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestNativePlaygroundRequiresPersistedNativeSessionAndUsesOriginalWallet(t *testing.T) {
	for _, kind := range []string{"native", "central", "absent", "other-owner", "non-playground"} {
		t.Run(kind, func(t *testing.T) {
			db, c, info := legacyLeaseFixture(t)
			require.NoError(t, db.AutoMigrate(&model.UserSession{}, &model.UniHubIdentityMapping{}))
			require.NoError(t, db.Model(&model.User{}).Where("id = ?", 42).Updates(map[string]any{"funding_realm": model.NativeFundingLocalOnly, "funding_realm_evidence": "reviewed", "native_password": true, "auth_version": 1}).Error)
			bundle, err := CreateNativeLoginSessionAtAuthVersion(42, 1, "password", "", "")
			require.NoError(t, err)
			identity, err := ParseAccessToken(bundle.AccessToken)
			require.NoError(t, err)
			switch kind {
			case "central":
				require.NoError(t, db.Model(&model.UserSession{}).Where("sid = ?", identity.SessionID).Update("auth_realm", "").Error)
			case "other-owner":
				identity.UserID = 43
			}
			if kind != "absent" {
				c.Set("auth_identity", identity)
			}
			info.NativeRealm = true
			info.IsPlayground = kind != "non-playground"
			info.TokenId = 0
			info.TokenKey = ""
			session, apiErr := NewBillingSession(c, info, 20)
			if kind != "native" {
				require.NotNil(t, apiErr)
				require.Nil(t, session)
			} else {
				require.Nil(t, apiErr)
				require.NotNil(t, session)
				require.NoError(t, session.Settle(12))
			}
			var user model.User
			require.NoError(t, db.First(&user, 42).Error)
			want := 500
			if kind == "native" {
				want = 488
			}
			assert.Equal(t, want, user.Quota)
			var token model.Token
			require.NoError(t, db.First(&token, 7).Error)
			assert.Equal(t, 200, token.RemainQuota)
			var leases int64
			require.NoError(t, db.Model(&model.ProductLegacyFundingLease{}).Count(&leases).Error)
			assert.Zero(t, leases)
			native, err := IsNativeLoginSession(context.Background(), identity)
			if kind == "native" {
				require.NoError(t, err)
				assert.True(t, native)
			}
			assert.Equal(t, common.UserStatusEnabled, user.Status)
		})
	}
}
