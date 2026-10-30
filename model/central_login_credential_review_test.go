package model

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestCentralCredentialBindingRequiresLiveCentralSessionAndCannotOverwriteNative(t *testing.T) {
	_, db, _ := productFundingFixture(t)
	old := DB
	DB = db
	t.Cleanup(func() { DB = old })
	require.NoError(t, db.AutoMigrate(&CentralAuthState{}, &CentralLoginCredential{}, &NativeLoginCredential{}, &UserSession{}))
	require.NoError(t, db.Model(&User{}).Where("id = ?", 42).Updates(map[string]any{"funding_realm": NativeFundingLocalOnly, "funding_realm_evidence": "reviewed", "native_password": true, "auth_version": 1}).Error)
	require.NoError(t, db.Create(&CentralAuthState{UserID: 42, AuthVersion: 7, Active: true, BindingID: "binding"}).Error)
	now := time.Now().Unix()
	require.NoError(t, db.Create(&UserSession{SID: "central-sid", UserID: 42, AuthRealm: CentralAuthRealm, UserAuthVersion: 7, Version: 1, Status: UserSessionStatusActive, RefreshHash: "fixture", LoginMethod: "product_broker", ExpiresAt: now + 600}).Error)
	bind := func(sid, value string) error {
		return db.Transaction(func(tx *gorm.DB) error {
			return SetCentralLoginCredentialTx(tx, 42, sid, "oidc_id", value, func(tx *gorm.DB) error { return tx.Model(&User{}).Where("id = ?", 42).Update("oidc_id", value).Error })
		})
	}
	require.Error(t, bind("unknown-sid", "central-subject"))
	require.NoError(t, bind("central-sid", "central-subject"))
	err := db.Transaction(func(tx *gorm.DB) error {
		return SetNativeLoginCredentialTx(tx, 42, "oidc_id", "native-subject", NativeFundingLocalOnly)
	})
	require.Error(t, err, "native binding must not overwrite a central credential slot")
	central, err := IsCentralLoginCredential(context.Background(), 42, "oidc_id", "central-subject")
	require.NoError(t, err)
	require.True(t, central)
	require.NoError(t, bind("central-sid", "central-rotated"))
	_, err = IsCentralLoginCredential(context.Background(), 42, "oidc_id", "central-subject")
	require.Error(t, err, "rotating a central binding must invalidate the old credential")
	central, err = IsCentralLoginCredential(context.Background(), 42, "oidc_id", "central-rotated")
	require.NoError(t, err)
	require.True(t, central)
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		return DeleteCentralLoginCredentialTx(tx, 42, "central-sid", "oidc_id", func(tx *gorm.DB) error { return tx.Model(&User{}).Where("id = ?", 42).Update("oidc_id", "").Error })
	}))
	_, err = IsCentralLoginCredential(context.Background(), 42, "oidc_id", "central-rotated")
	require.Error(t, err)
	require.NoError(t, bind("central-sid", "central-rotated"))
	central, err = IsCentralLoginCredential(context.Background(), 42, "oidc_id", "different")
	require.Error(t, err)
	require.False(t, central)
	require.NoError(t, db.Model(&CentralAuthState{}).Where("user_id = ?", 42).Update("active", false).Error)
	_, err = IsCentralLoginCredential(context.Background(), 42, "oidc_id", "central-rotated")
	require.Error(t, err)
}

func TestCentralCredentialCannotClaimUnknownOrNativeSlot(t *testing.T) {
	_, db, _ := productFundingFixture(t)
	old := DB
	DB = db
	t.Cleanup(func() { DB = old })
	require.NoError(t, db.AutoMigrate(&CentralAuthState{}, &CentralLoginCredential{}, &NativeLoginCredential{}, &UserSession{}))
	require.NoError(t, db.Model(&User{}).Where("id = ?", 42).Updates(map[string]any{"funding_realm": NativeFundingLocalOnly, "funding_realm_evidence": "reviewed", "native_password": true, "auth_version": 1, "oidc_id": "unknown-old"}).Error)
	require.NoError(t, db.Create(&CentralAuthState{UserID: 42, AuthVersion: 7, Active: true, BindingID: "binding"}).Error)
	require.NoError(t, db.Create(&UserSession{SID: "central-sid", UserID: 42, AuthRealm: CentralAuthRealm, UserAuthVersion: 7, Version: 1, Status: UserSessionStatusActive, RefreshHash: "fixture", LoginMethod: "product_broker", ExpiresAt: time.Now().Unix() + 600}).Error)
	bind := func() error {
		return db.Transaction(func(tx *gorm.DB) error {
			return SetCentralLoginCredentialTx(tx, 42, "central-sid", "oidc_id", "central-new", func(tx *gorm.DB) error {
				return tx.Model(&User{}).Where("id = ?", 42).Update("oidc_id", "central-new").Error
			})
		})
	}
	require.Error(t, bind())
	var user User
	require.NoError(t, db.First(&user, 42).Error)
	require.Equal(t, "unknown-old", user.OidcId)
	require.NoError(t, db.Model(&User{}).Where("id = ?", 42).Update("oidc_id", "").Error)
	require.NoError(t, db.Create(&NativeLoginCredential{UserID: 42, Kind: "oidc_id", Digest: "native-proof"}).Error)
	require.Error(t, bind())
}

func TestCentralPasskeyProofBindsPublicKeyAndCanBeDeleted(t *testing.T) {
	_, db, _ := productFundingFixture(t)
	old := DB
	DB = db
	t.Cleanup(func() { DB = old })
	require.NoError(t, db.AutoMigrate(&CentralAuthState{}, &CentralLoginCredential{}, &NativeLoginCredential{}, &UserSession{}, &PasskeyCredential{}))
	require.NoError(t, db.Create(&CentralAuthState{UserID: 42, AuthVersion: 7, Active: true, BindingID: "binding"}).Error)
	require.NoError(t, db.Create(&UserSession{SID: "central-sid", UserID: 42, AuthRealm: CentralAuthRealm, UserAuthVersion: 7, Version: 1, Status: UserSessionStatusActive, RefreshHash: "fixture", LoginMethod: "product_broker", ExpiresAt: time.Now().Unix() + 600}).Error)
	passkey := PasskeyCredential{UserID: 42, CredentialID: "credential", PublicKey: "original-public-key"}
	value := passkey.CredentialID + "\x00" + passkey.PublicKey
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		return SetCentralLoginCredentialTx(tx, 42, "central-sid", "passkey", value, func(tx *gorm.DB) error { return tx.Create(&passkey).Error })
	}))
	valid, err := IsCentralLoginCredential(context.Background(), 42, "passkey", value)
	require.NoError(t, err)
	require.True(t, valid)
	require.NoError(t, db.Model(&PasskeyCredential{}).Where("id = ?", passkey.ID).Update("public_key", "replaced-public-key").Error)
	_, err = IsCentralLoginCredential(context.Background(), 42, "passkey", value)
	require.Error(t, err)
	require.NoError(t, db.Model(&PasskeyCredential{}).Where("id = ?", passkey.ID).Update("public_key", passkey.PublicKey).Error)
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		return DeleteCentralLoginCredentialTx(tx, 42, "central-sid", "passkey", func(tx *gorm.DB) error {
			return tx.Unscoped().Where("user_id = ?", 42).Delete(&PasskeyCredential{}).Error
		})
	}))
	_, err = IsCentralLoginCredential(context.Background(), 42, "passkey", value)
	require.Error(t, err)
}

func TestCentralPasskeyDeleteRequiresVerifiedIndependentSessionCredential(t *testing.T) {
	_, db, _ := productFundingFixture(t)
	old := DB
	DB = db
	t.Cleanup(func() { DB = old })
	require.NoError(t, db.AutoMigrate(&CentralAuthState{}, &CentralLoginCredential{}, &CentralSessionCredential{}, &NativeLoginCredential{}, &UserSession{}, &PasskeyCredential{}))
	require.NoError(t, db.Create(&CentralAuthState{UserID: 42, AuthVersion: 7, Active: true, BindingID: "binding"}).Error)
	require.NoError(t, db.Create(&UserSession{SID: "source-sid", UserID: 42, AuthRealm: CentralAuthRealm, UserAuthVersion: 7, Version: 1, Status: UserSessionStatusActive, RefreshHash: "fixture", LoginMethod: "product_broker", ExpiresAt: time.Now().Unix() + 600}).Error)
	passkey := PasskeyCredential{UserID: 42, CredentialID: "key", PublicKey: "public-key"}
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		return SetCentralLoginCredentialTx(tx, 42, "source-sid", "passkey", "key\x00public-key", func(tx *gorm.DB) error { return tx.Create(&passkey).Error })
	}))
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		return SetCentralLoginCredentialTx(tx, 42, "source-sid", "oidc_id", "oauth-id", func(tx *gorm.DB) error {
			return tx.Model(&User{}).Where("id = ?", 42).Update("oidc_id", "oauth-id").Error
		})
	}))
	require.NoError(t, db.Create(&UserSession{SID: "credential-sid", UserID: 42, AuthRealm: CentralAuthRealm, UserAuthVersion: 7, Version: 1, Status: UserSessionStatusActive, RefreshHash: "fixture", LoginMethod: "central_credential", ExpiresAt: time.Now().Unix() + 600}).Error)
	attempt := func() error {
		return db.Transaction(func(tx *gorm.DB) error {
			return DeleteCentralLoginCredentialTx(tx, 42, "credential-sid", "passkey", func(tx *gorm.DB) error {
				return tx.Unscoped().Where("user_id = ?", 42).Delete(&PasskeyCredential{}).Error
			})
		})
	}
	require.Error(t, attempt(), "missing session provenance must fail")
	sum := sha256.Sum256([]byte("oauth-id"))
	require.NoError(t, db.Create(&CentralSessionCredential{SID: "credential-sid", UserID: 42, Kind: "oidc_id", Digest: "tampered"}).Error)
	require.Error(t, attempt(), "invalid session digest must fail")
	require.NoError(t, db.Model(&CentralSessionCredential{}).Where("sid = ?", "credential-sid").Updates(map[string]any{"kind": "passkey", "digest": hex.EncodeToString(sum[:])}).Error)
	require.Error(t, attempt(), "the session's own passkey cannot be deleted")
	require.NoError(t, db.Model(&CentralSessionCredential{}).Where("sid = ?", "credential-sid").Updates(map[string]any{"kind": "unknown", "digest": hex.EncodeToString(sum[:])}).Error)
	require.Error(t, attempt(), "unknown session provenance must fail")
	require.NoError(t, db.Model(&UserSession{}).Where("sid = ?", "credential-sid").Update("login_method", "unknown").Error)
	require.Error(t, attempt(), "unknown login method must fail")
	_, err := GetPasskeyByUserID(42)
	require.NoError(t, err, "all denied attempts leave the passkey intact")
	require.NoError(t, db.Model(&UserSession{}).Where("sid = ?", "credential-sid").Update("login_method", "central_credential").Error)
	require.NoError(t, db.Model(&CentralSessionCredential{}).Where("sid = ?", "credential-sid").Updates(map[string]any{"kind": "oidc_id", "digest": hex.EncodeToString(sum[:])}).Error)
	require.NoError(t, attempt(), "verified independent OAuth credential may delete after step-up")
}
