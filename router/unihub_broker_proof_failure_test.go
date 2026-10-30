package router

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	userkit "github.com/NeoMei/UniHub/packages/userkit-go"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/internal/platformaccount"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestBrokerProofUnreadableDoesNotRevokeSession(t *testing.T) {
	f := newUniHubRegressionFixture(t)
	require.NoError(t, f.db.AutoMigrate(&model.ProductBrokerSessionProof{}))
	t.Setenv("UNIHUB_PRODUCT_RUNTIME_ENABLED", "true")
	t.Setenv("UNIHUB_PRODUCT_RUNTIME_TOKEN", strings.Repeat("s", 32))
	cfg, err := platformaccount.Current()
	require.NoError(t, err)
	user := model.User{Username: "broker-proof-failures", AffCode: "broker-proof-failures", AuthVersion: 1, Status: common.UserStatusEnabled, Role: common.RoleCommonUser}
	require.NoError(t, f.db.Create(&user).Error)
	_, err = model.BindUniHubIdentity(platformaccount.Identity{Issuer: cfg.PublicOrigin, User: platformaccount.User{ID: "member"}}, user.Id)
	require.NoError(t, err)
	bundle, err := service.CreateLoginSession(user.Id, "product_broker", "", "")
	require.NoError(t, err)
	access := userkit.Access{Issuer: cfg.PublicOrigin, Subject: "member", User: userkit.User{ID: "member"}, ProductID: cfg.ProductID, InstanceID: cfg.InstanceID, Active: true, ExpiresAt: time.Now().Add(time.Minute).Unix()}
	require.NoError(t, model.BindBrokerSourceProof(bundle.Session.SID, user.Id, access, cfg, "ubsp_"+strings.Repeat("a", 64), time.Now().Add(time.Minute).Unix()))
	identity, err := service.ParseAccessToken(bundle.AccessToken)
	require.NoError(t, err)
	var original model.ProductBrokerSessionProof
	require.NoError(t, f.db.First(&original, "sid = ?", bundle.Session.SID).Error)
	for _, tc := range []struct {
		name, column string
		value        any
	}{
		{"tampered expiry", "expires_at", time.Now().Add(-time.Minute).Unix()},
		{"tampered owner", "user_id", user.Id + 1},
		{"tampered subject", "subject", "other"},
		{"malformed ciphertext", "ciphertext", "invalid"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.NoError(t, f.db.Model(&model.ProductBrokerSessionProof{}).Where("sid = ?", original.SID).Update(tc.column, tc.value).Error)
			_, err := service.VerifyBrokerSourceWithExpiry(context.Background(), identity)
			require.ErrorIs(t, err, service.ErrBrokerSourceUnavailable)
			require.False(t, errors.Is(err, service.ErrLoginSessionRevoked))
			require.NoError(t, f.db.Model(&model.ProductBrokerSessionProof{}).Where("sid = ?", original.SID).Update(tc.column, map[string]any{"expires_at": original.ExpiresAt, "user_id": original.UserID, "subject": original.Subject, "ciphertext": original.Ciphertext}[tc.column]).Error)
		})
	}
	secret := common.SessionSecret
	common.SessionSecret = strings.Repeat("z", 32)
	_, err = service.VerifyBrokerSourceWithExpiry(context.Background(), identity)
	common.SessionSecret = secret
	require.ErrorIs(t, err, service.ErrBrokerSourceUnavailable)
	require.NoError(t, f.db.Delete(&original).Error)
	_, err = service.VerifyBrokerSourceWithExpiry(context.Background(), identity)
	require.ErrorIs(t, err, service.ErrLoginSessionRevoked)
}

func TestPurgeBrokerProofPreservesUnauthenticatedExpiry(t *testing.T) {
	f := newUniHubRegressionFixture(t)
	require.NoError(t, f.db.AutoMigrate(&model.ProductBrokerSessionProof{}))
	cfg, err := platformaccount.Current()
	require.NoError(t, err)
	user := model.User{Username: "broker-purge", AffCode: "broker-purge", AuthVersion: 1, Status: common.UserStatusEnabled, Role: common.RoleCommonUser}
	require.NoError(t, f.db.Create(&user).Error)
	_, err = model.BindUniHubIdentity(platformaccount.Identity{Issuer: cfg.PublicOrigin, User: platformaccount.User{ID: "member"}}, user.Id)
	require.NoError(t, err)
	bundle, err := service.CreateLoginSession(user.Id, "product_broker", "", "")
	require.NoError(t, err)
	access := userkit.Access{Issuer: cfg.PublicOrigin, Subject: "member", User: userkit.User{ID: "member"}, ProductID: cfg.ProductID, InstanceID: cfg.InstanceID, Active: true, ExpiresAt: time.Now().Add(time.Minute).Unix()}
	require.NoError(t, model.BindBrokerSourceProof(bundle.Session.SID, user.Id, access, cfg, "ubsp_"+strings.Repeat("a", 64), time.Now().Add(time.Minute).Unix()))
	var original model.ProductBrokerSessionProof
	require.NoError(t, f.db.First(&original, "sid = ?", bundle.Session.SID).Error)
	originalExpiry := original.ExpiresAt
	require.NoError(t, f.db.Model(&model.ProductBrokerSessionProof{}).Where("sid = ?", original.SID).Update("expires_at", time.Now().Add(-time.Minute).Unix()).Error)
	require.NoError(t, model.PurgeBrokerSourceProofs(time.Now().Unix()))
	var retained model.ProductBrokerSessionProof
	require.NoError(t, f.db.First(&retained, "sid = ?", bundle.Session.SID).Error)
	require.NoError(t, f.db.Model(&retained).Update("expires_at", originalExpiry).Error)
	_, proof, err := model.OpenBrokerSourceProof(bundle.Session.SID, user.Id, "member", cfg)
	require.NoError(t, err)
	require.Equal(t, "ubsp_"+strings.Repeat("a", 64), proof)
}

func TestPurgeBrokerProofAuthenticatedExpiryAndSnapshotReplacement(t *testing.T) {
	f := newUniHubRegressionFixture(t)
	require.NoError(t, f.db.AutoMigrate(&model.ProductBrokerSessionProof{}))
	key := hmac.New(sha256.New, []byte(common.SessionSecret))
	_, err := key.Write([]byte("llmrouter/unihub/broker-source/aes-gcm/v1"))
	require.NoError(t, err)
	block, err := aes.NewCipher(key.Sum(nil))
	require.NoError(t, err)
	aead, err := cipher.NewGCM(block)
	require.NoError(t, err)
	proof := "ubsp_" + strings.Repeat("a", 64)
	old := model.ProductBrokerSessionProof{SID: "expired-proof", UserID: 1, Issuer: "issuer", Subject: "subject", ProductID: "p", InstanceID: "i", ClientID: "c", ExpiresAt: time.Now().Add(-time.Minute).Unix()}
	aad, err := common.Marshal([]any{"unihub-broker-source/v1", old.ProductID, old.InstanceID, old.ClientID, old.Issuer, old.Subject, old.UserID, old.SID, old.ExpiresAt})
	require.NoError(t, err)
	nonce := make([]byte, aead.NonceSize())
	old.Ciphertext = base64.RawURLEncoding.EncodeToString(aead.Seal(nonce, nonce, []byte(proof), aad))
	require.NoError(t, f.db.Create(&old).Error)
	orphan := old
	orphan.SID = "possibly-new-session"
	orphan.ExpiresAt = time.Now().Add(time.Minute).Unix()
	orphanAAD, err := common.Marshal([]any{"unihub-broker-source/v1", orphan.ProductID, orphan.InstanceID, orphan.ClientID, orphan.Issuer, orphan.Subject, orphan.UserID, orphan.SID, orphan.ExpiresAt})
	require.NoError(t, err)
	orphan.Ciphertext = base64.RawURLEncoding.EncodeToString(aead.Seal(nonce, nonce, []byte(proof), orphanAAD))
	require.NoError(t, f.db.Create(&orphan).Error)
	replaced := false
	callback := "test:replace_broker_proof"
	require.NoError(t, f.db.Callback().Query().After("gorm:query").Register(callback, func(tx *gorm.DB) {
		if replaced || tx.Statement.Table != "product_broker_session_proofs" {
			return
		}
		if _, ok := tx.Statement.Dest.(*[]model.ProductBrokerSessionProof); !ok {
			return
		}
		replaced = true
		newNonce := make([]byte, aead.NonceSize())
		newNonce[0] = 1
		fresh := base64.RawURLEncoding.EncodeToString(aead.Seal(newNonce, newNonce, []byte(proof), aad))
		if err := f.db.Model(&model.ProductBrokerSessionProof{}).Where("sid = ?", old.SID).Update("ciphertext", fresh).Error; err != nil {
			tx.AddError(err)
		}
	}))
	t.Cleanup(func() { _ = f.db.Callback().Query().Remove(callback) })
	require.NoError(t, model.PurgeBrokerSourceProofs(time.Now().Unix()))
	require.True(t, replaced)
	var row model.ProductBrokerSessionProof
	require.NoError(t, f.db.First(&row, "sid = ?", old.SID).Error)
	require.NotEqual(t, old.Ciphertext, row.Ciphertext)
	require.NoError(t, f.db.Callback().Query().Remove(callback))
	require.NoError(t, model.PurgeBrokerSourceProofs(time.Now().Unix()))
	require.ErrorIs(t, f.db.First(&row, "sid = ?", old.SID).Error, gorm.ErrRecordNotFound)
	var orphanRow model.ProductBrokerSessionProof
	require.NoError(t, f.db.First(&orphanRow, "sid = ?", orphan.SID).Error, "an unexpired proof without a lockable session must be retained")
}

func TestBrokerProofChangedDuringSourceCheckIsUnavailable(t *testing.T) {
	f := newUniHubRegressionFixture(t)
	require.NoError(t, f.db.AutoMigrate(&model.ProductBrokerSessionProof{}))
	t.Setenv("UNIHUB_PRODUCT_RUNTIME_ENABLED", "true")
	t.Setenv("UNIHUB_PRODUCT_RUNTIME_TOKEN", strings.Repeat("s", 32))
	cfg, err := platformaccount.Current()
	require.NoError(t, err)
	user := model.User{Username: "broker-change", AffCode: "broker-change", AuthVersion: 1, Status: common.UserStatusEnabled, Role: common.RoleCommonUser}
	require.NoError(t, f.db.Create(&user).Error)
	_, err = model.BindUniHubIdentity(platformaccount.Identity{Issuer: cfg.PublicOrigin, User: platformaccount.User{ID: "member"}}, user.Id)
	require.NoError(t, err)
	bundle, err := service.CreateLoginSession(user.Id, "product_broker", "", "")
	require.NoError(t, err)
	access := userkit.Access{Issuer: cfg.PublicOrigin, Subject: "member", User: userkit.User{ID: "member"}, ProductID: cfg.ProductID, InstanceID: cfg.InstanceID, Active: true, ExpiresAt: time.Now().Add(time.Minute).Unix()}
	proof := "ubsp_" + strings.Repeat("a", 64)
	require.NoError(t, model.BindBrokerSourceProof(bundle.Session.SID, user.Id, access, cfg, proof, time.Now().Add(time.Minute).Unix()))
	var row model.ProductBrokerSessionProof
	require.NoError(t, f.db.First(&row, "sid = ?", bundle.Session.SID).Error)
	key := hmac.New(sha256.New, []byte(common.SessionSecret))
	_, err = key.Write([]byte("llmrouter/unihub/broker-source/aes-gcm/v1"))
	require.NoError(t, err)
	block, err := aes.NewCipher(key.Sum(nil))
	require.NoError(t, err)
	aead, err := cipher.NewGCM(block)
	require.NoError(t, err)
	aad, err := common.Marshal([]any{"unihub-broker-source/v1", row.ProductID, row.InstanceID, row.ClientID, row.Issuer, row.Subject, row.UserID, row.SID, row.ExpiresAt})
	require.NoError(t, err)
	nonce := make([]byte, aead.NonceSize())
	nonce[0] = 1
	replacement := base64.RawURLEncoding.EncodeToString(aead.Seal(nonce, nonce, []byte(proof), aad))
	require.NotEqual(t, row.Ciphertext, replacement)
	_, initialProof, err := model.OpenBrokerSourceProof(row.SID, user.Id, "member", cfg)
	require.NoError(t, err)
	require.Equal(t, proof, initialProof)
	var proofReads atomic.Int32
	callback := "test:count_broker_source_reads"
	require.NoError(t, f.db.Callback().Query().After("gorm:query").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table == "product_broker_session_proofs" {
			proofReads.Add(1)
		}
	}))
	t.Cleanup(func() { _ = f.db.Callback().Query().Remove(callback) })
	var calls atomic.Int32
	var replaced atomic.Bool
	var requestValid atomic.Bool
	var replaceOnVerify atomic.Bool
	hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var request struct {
			Proof string `json:"source_proof_token"`
			User  string `json:"central_user_id"`
		}
		if r.Method != http.MethodPost || r.URL.Path != "/internal/v1/product-runtime/identity/browser/source/verify" || r.Header.Get("Authorization") != "Bearer "+strings.Repeat("s", 32) || common.DecodeJson(r.Body, &request) != nil || request.Proof != proof || request.User != "member" || proofReads.Load() != 1 {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		requestValid.Store(true)
		if !replaceOnVerify.Load() {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if err := f.db.Model(&model.ProductBrokerSessionProof{}).Where("sid = ?", row.SID).Update("ciphertext", replacement).Error; err != nil {
			w.WriteHeader(500)
			return
		}
		replaced.Store(true)
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(hub.Close)
	t.Setenv("UNIHUB_PRODUCT_API_ORIGIN", hub.URL)
	identity, err := service.ParseAccessToken(bundle.AccessToken)
	require.NoError(t, err)
	expires, err := service.VerifyBrokerSourceWithExpiry(context.Background(), identity)
	require.NoError(t, err, "unchanged binding must pass the same mock verifier")
	require.Equal(t, row.ExpiresAt, expires)
	require.EqualValues(t, 1, calls.Load())
	require.EqualValues(t, 2, proofReads.Load())
	require.True(t, requestValid.Load())
	require.False(t, replaced.Load())
	calls.Store(0)
	proofReads.Store(0)
	requestValid.Store(false)
	replaceOnVerify.Store(true)
	_, err = service.VerifyBrokerSourceWithExpiry(context.Background(), identity)
	require.ErrorIs(t, err, service.ErrBrokerSourceUnavailable)
	require.EqualValues(t, 1, calls.Load(), "the private verifier must actually run")
	require.True(t, requestValid.Load(), "the expected verifier request must succeed after the initial proof read")
	require.True(t, replaced.Load(), "the verifier must replace the bound ciphertext before returning 204")
	require.EqualValues(t, 2, proofReads.Load(), "the proof must be re-read after the verifier response")
	current, currentProof, err := model.OpenBrokerSourceProof(row.SID, user.Id, "member", cfg)
	require.NoError(t, err, "replacement must be valid under the original authenticated binding")
	require.Equal(t, replacement, current.Ciphertext)
	require.Equal(t, proof, currentProof)
}
