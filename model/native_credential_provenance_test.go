package model

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestNativeCredentialEvidenceBinding(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "provenance.db")), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&User{}, &Token{}, &ProductFundingAccount{}, &NativeCredentialProvenance{}))
	user := User{Id: 42, Username: "native", Password: "stored", Status: common.UserStatusEnabled, AuthVersion: 1, FundingRealm: NativeFundingLocalOnly, FundingRealmEvidence: "reviewed-evidence"}
	token := Token{Id: 7, UserId: 42, Key: "fixture-key", Status: common.TokenStatusEnabled, ExpiredTime: -1}
	require.NoError(t, db.Create(&user).Error)
	require.NoError(t, db.Create(&token).Error)
	key := []byte("01234567890123456789012345678901")
	p := NativeCredentialProvenance{Kind: NativeCredentialRelayKey, CredentialID: "7", UserID: 42, Source: NativeCredentialSourceNative, EvidenceRef: "reviewed-evidence", AuthVersion: 1}
	p.Binding = nativeCredentialKeyBinding(key, p, token.Key)
	require.NoError(t, db.Create(&p).Error)
	check := func() error {
		return CheckNativeRelayKeyProvenance(context.Background(), db, key, 42, 7, time.Now().Unix())
	}
	require.NoError(t, check())
	for _, tc := range []struct {
		name    string
		mutate  func()
		restore func()
	}{
		{"changed secret", func() { require.NoError(t, db.Model(&token).Update("key", "replacement").Error) }, func() { require.NoError(t, db.Model(&token).Update("key", "fixture-key").Error) }},
		{"disabled key", func() { require.NoError(t, db.Model(&token).Update("status", common.TokenStatusDisabled).Error) }, func() { require.NoError(t, db.Model(&token).Update("status", common.TokenStatusEnabled).Error) }},
		{"pending registration", func() { require.NoError(t, db.Model(&user).Update("product_registration_pending", true).Error) }, func() { require.NoError(t, db.Model(&user).Update("product_registration_pending", false).Error) }},
		{"pending jit", func() { require.NoError(t, db.Model(&user).Update("product_jit_pending", true).Error) }, func() { require.NoError(t, db.Model(&user).Update("product_jit_pending", false).Error) }},
		{"pending identity", func() { require.NoError(t, db.Model(&user).Update("identity_source", ProductIdentityPending).Error) }, func() { require.NoError(t, db.Model(&user).Update("identity_source", "unihub").Error) }},
		{"central funds", func() { require.NoError(t, db.Create(&ProductFundingAccount{UserID: 42, State: "central"}).Error) }, func() { require.NoError(t, db.Delete(&ProductFundingAccount{}, 42).Error) }},
		{"owner realm", func() { require.NoError(t, db.Model(&user).Update("funding_realm", "").Error) }, func() { require.NoError(t, db.Model(&user).Update("funding_realm", NativeFundingLocalOnly).Error) }},
		{"central source", func() { require.NoError(t, db.Model(&p).Update("source", "central_product").Error) }, func() { require.NoError(t, db.Model(&p).Update("source", NativeCredentialSourceNative).Error) }},
		{"unknown source", func() { require.NoError(t, db.Model(&p).Update("source", "unknown").Error) }, func() { require.NoError(t, db.Model(&p).Update("source", NativeCredentialSourceNative).Error) }},
		{"expired key", func() { require.NoError(t, db.Model(&token).Update("expired_time", time.Now().Unix()-1).Error) }, func() { require.NoError(t, db.Model(&token).Update("expired_time", -1).Error) }},
		{"central key binding", func() { require.NoError(t, db.Model(&token).Update("funding_account_id", "central").Error) }, func() { require.NoError(t, db.Model(&token).Update("funding_account_id", "").Error) }},
		{"auth version", func() { require.NoError(t, db.Model(&user).Update("auth_version", 2).Error) }, func() { require.NoError(t, db.Model(&user).Update("auth_version", 1).Error) }},
	} {
		t.Run(tc.name, func(t *testing.T) { tc.mutate(); require.Error(t, check()); tc.restore(); require.NoError(t, check()) })
	}
	require.Error(t, CheckNativeRelayKeyProvenance(context.Background(), db, key, 43, 7, time.Now().Unix()))
	require.Error(t, CheckNativeRelayKeyProvenance(context.Background(), db, key, 42, 19, time.Now().Unix()))
	require.Error(t, CheckNativeRelayKeyProvenance(context.Background(), db, []byte("weak"), 42, 7, time.Now().Unix()))
	require.NoError(t, db.Delete(&token).Error)
	require.Error(t, check(), "soft deleted keys must remain unavailable")
	var saved NativeCredentialProvenance
	require.NoError(t, db.First(&saved, "kind = ? AND credential_id = ?", NativeCredentialRelayKey, "7").Error)
	assert.NotContains(t, saved.Binding, "fixture-key")
}

func TestDeletedHistoricalKeyIDsCannotBeAccepted(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "deleted.db")), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&User{}, &Token{}, &ProductFundingAccount{}, &NativeCredentialProvenance{}))
	u := User{Id: 42, Username: "native", Password: "stored", Status: common.UserStatusEnabled, AuthVersion: 1, FundingRealm: NativeFundingLocalOnly, FundingRealmEvidence: "reviewed"}
	require.NoError(t, db.Create(&u).Error)
	key := []byte("01234567890123456789012345678901")
	for _, id := range []int{3, 13} {
		token := Token{Id: id, UserId: u.Id, Key: fmt.Sprintf("fixture-%d", id), Status: common.TokenStatusEnabled, ExpiredTime: -1}
		require.NoError(t, db.Create(&token).Error)
		p := NativeCredentialProvenance{Kind: NativeCredentialRelayKey, CredentialID: strconv.Itoa(id), UserID: u.Id, Source: NativeCredentialSourceNative, EvidenceRef: "reviewed", AuthVersion: 1}
		p.Binding = nativeCredentialKeyBinding(key, p, token.Key)
		require.NoError(t, db.Create(&p).Error)
		require.NoError(t, db.Delete(&token).Error)
		require.Error(t, CheckNativeRelayKeyProvenance(context.Background(), db, key, u.Id, id, time.Now().Unix()))
		var persisted Token
		require.NoError(t, db.Unscoped().First(&persisted, id).Error)
		require.True(t, persisted.DeletedAt.Valid)
	}
}
