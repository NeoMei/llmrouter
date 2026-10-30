package router

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"

	"encoding/base64"
	"encoding/binary"
	"gorm.io/gorm"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	userkit "github.com/NeoMei/UniHub/packages/userkit-go"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/internal/platformaccount"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/system_setting"
	"github.com/fxamacker/cbor/v2"
	"github.com/stretchr/testify/require"
)

func TestCentralPasskeyRegisterAndLoginWithVirtualAuthenticator(t *testing.T) {
	t.Run("disabled local account", func(t *testing.T) { testCentralPasskeyVirtualAuthenticator(t, false) })
	t.Run("enabled non-local account", func(t *testing.T) { testCentralPasskeyVirtualAuthenticator(t, true) })
}

func testCentralPasskeyVirtualAuthenticator(t *testing.T, nonLocal bool) {
	f := newUniHubRegressionFixture(t)
	require.NoError(t, model.RegisterProductFundingModels(f.db))
	require.NoError(t, f.db.AutoMigrate(&model.CentralAuthState{}, &model.CentralLoginCredential{}, &model.CentralSessionCredential{}, &model.NativeLoginCredential{}, &model.ProductAuthLifecycle{}, &model.ProductBrokerSessionProof{}, &model.AuthFlow{}, &model.Log{}, &model.PasskeyCredential{}))
	oldLog := model.LOG_DB
	model.LOG_DB = f.db
	t.Cleanup(func() { model.LOG_DB = oldLog })
	settings := system_setting.GetPasskeySettings()
	oldSettings := *settings
	settings.Enabled, settings.RPID, settings.Origins = true, "dashboard.test", "https://dashboard.test"
	t.Cleanup(func() { *settings = oldSettings })
	user := model.User{Username: "central-passkey", AffCode: "central-passkey", Password: "unused", Role: 1, Status: 1, AuthVersion: 1, FundingRealm: model.NativeFundingLocalOnly, FundingRealmEvidence: "reviewed", NativePassword: true}
	if nonLocal {
		user.FundingRealm = ""
		user.FundingRealmEvidence = ""
		user.NativePassword = false
		user.AuthVersion = 7
	}
	require.NoError(t, f.db.Create(&user).Error)
	var hub *httptest.Server
	denyAfterPasskeyStored := false
	denyAfterPasskeyDeleted := false
	hub = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if denyAfterPasskeyStored {
			var count int64
			if e := f.db.Model(&model.PasskeyCredential{}).Where("user_id = ?", user.Id).Count(&count).Error; e != nil || count != 0 {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
		}
		if denyAfterPasskeyDeleted {
			var count int64
			if e := f.db.Model(&model.PasskeyCredential{}).Where("user_id = ?", user.Id).Count(&count).Error; e != nil || count == 0 {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
		}
		if r.URL.Path == "/internal/v1/product-runtime/identity/browser/source/verify" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		access := userkit.Access{Issuer: hub.URL, Subject: "central-subject", User: userkit.User{ID: "central-subject"}, ProductID: "llmrouter", InstanceID: "default", Active: true, Version: 1, ExpiresAt: time.Now().Unix() + 600, Permissions: []string{"product:use"}}
		identity := userkit.ProductIdentity{Binding: userkit.RuntimeBinding{ID: "bound", LocalUserID: strconv.Itoa(user.Id), UserID: "central-subject", ProductID: "llmrouter", InstanceID: "default"}, Access: access, ProductRole: 1, AuthVersion: 7}
		data, err := common.Marshal(identity)
		require.NoError(t, err)
		_, err = w.Write(data)
		require.NoError(t, err)
	}))
	t.Cleanup(hub.Close)
	for key, value := range map[string]string{"UNIHUB_INDEPENDENT_CENTRAL_REALM_ENABLED": "true", "UNIHUB_PRODUCT_RUNTIME_ENABLED": "true", "UNIHUB_PRODUCT_API_ORIGIN": hub.URL, "UNIHUB_PRODUCT_PUBLIC_ORIGIN": hub.URL, "UNIHUB_PRODUCT_RUNTIME_TOKEN": strings.Repeat("s", 32), "UNIHUB_PRODUCT_ID": "llmrouter", "UNIHUB_PRODUCT_INSTANCE_ID": "default", "UNIHUB_PRODUCT_CLIENT_ID": "client", "UNIHUB_PRODUCT_REDIRECT_URI": "https://dashboard.test/api/unihub/callback", "NATIVE_PUBLIC_ACCESS": "true"} {
		t.Setenv(key, value)
	}
	config, err := platformaccount.FromEnv()
	require.NoError(t, err)
	prior, err := platformaccount.Current()
	require.NoError(t, err)
	platformaccount.Configure(config)
	t.Cleanup(func() { platformaccount.Configure(prior) })
	_, err = model.BindUniHubIdentity(platformaccount.Identity{Issuer: hub.URL, User: platformaccount.User{ID: "central-subject"}}, user.Id)
	require.NoError(t, err)
	require.NoError(t, f.db.Create(&model.CentralAuthState{UserID: user.Id, AuthVersion: 7, AccessVersion: 1, Active: true, BindingID: "bound", Issuer: hub.URL, Subject: "central-subject", ProductID: "llmrouter", InstanceID: "default"}).Error)
	central, err := service.CreateVerifiedCentralLoginSession(user.Id, 7, "product_broker", time.Now().Unix()+600, "", "")
	require.NoError(t, err)
	access := userkit.Access{Issuer: hub.URL, Subject: "central-subject", User: userkit.User{ID: "central-subject"}, ProductID: "llmrouter", InstanceID: "default", Active: true, ExpiresAt: time.Now().Unix() + 600}
	require.NoError(t, model.BindBrokerSourceProof(central.Session.SID, user.Id, access, config, "ubsp_"+strings.Repeat("a", 64), access.ExpiresAt))
	send := func(path, token string, body any) *httptest.ResponseRecorder {
		raw, e := common.Marshal(body)
		require.NoError(t, e)
		req := httptest.NewRequest("POST", "https://dashboard.test"+path, strings.NewReader(string(raw)))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", "https://dashboard.test")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		out := httptest.NewRecorder()
		f.router.ServeHTTP(out, req)
		return out
	}
	readFlow := func(response *httptest.ResponseRecorder) (string, string) {
		var v struct {
			Success bool `json:"success"`
			Data    struct {
				Flow    string `json:"flow_token"`
				Options struct {
					PublicKey struct {
						Challenge string `json:"challenge"`
					} `json:"publicKey"`
				} `json:"options"`
			} `json:"data"`
		}
		require.NoError(t, common.Unmarshal(response.Body.Bytes(), &v))
		require.True(t, v.Success, response.Body.String())
		return v.Data.Flow, v.Data.Options.PublicKey.Challenge
	}
	enc := base64.RawURLEncoding.EncodeToString
	private, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	credentialID := []byte("virtual-central-passkey")
	x := private.PublicKey.X.FillBytes(make([]byte, 32))
	y := private.PublicKey.Y.FillBytes(make([]byte, 32))
	cose, err := cbor.Marshal(map[int]any{1: 2, 3: -7, -1: 1, -2: x, -3: y})
	require.NoError(t, err)
	flow, challenge := readFlow(send("/api/user/passkey/register/begin", central.AccessToken, map[string]any{}))
	clientData, err := common.Marshal(map[string]any{"type": "webauthn.create", "challenge": challenge, "origin": "https://dashboard.test"})
	require.NoError(t, err)
	rp := sha256.Sum256([]byte("dashboard.test"))
	authData := append([]byte{}, rp[:]...)
	authData = append(authData, 0x45, 0, 0, 0, 0)
	authData = append(authData, make([]byte, 16)...)
	length := make([]byte, 2)
	binary.BigEndian.PutUint16(length, uint16(len(credentialID)))
	authData = append(authData, length...)
	authData = append(authData, credentialID...)
	authData = append(authData, cose...)
	attestation, err := cbor.Marshal(map[string]any{"fmt": "none", "attStmt": map[string]any{}, "authData": authData})
	require.NoError(t, err)
	reg := map[string]any{"id": enc(credentialID), "rawId": enc(credentialID), "type": "public-key", "response": map[string]any{"clientDataJSON": enc(clientData), "attestationObject": enc(attestation)}}
	advanceDuringBinding := true
	require.NoError(t, f.db.Callback().Update().Before("gorm:update").Register("test:advance-central-session", func(tx *gorm.DB) {
		values, isUpdate := tx.Statement.Dest.(map[string]interface{})
		_, authVersionWrite := values["auth_version"]
		if advanceDuringBinding && tx.Statement.Table == "users" && isUpdate && authVersionWrite {
			advanceDuringBinding = false
			tx.Exec("UPDATE user_sessions SET version = version + 1 WHERE sid = ?", central.Session.SID)
		}
	}))
	stale := send("/api/user/passkey/register/finish", central.AccessToken, map[string]any{"flow_token": flow, "credential": reg})
	require.NoError(t, f.db.Callback().Update().Remove("test:advance-central-session"))
	require.False(t, advanceDuringBinding, "fixture must advance the session after authentication and before binding")
	require.NotContains(t, stale.Body.String(), `"success":true`)
	_, err = model.GetPasskeyByUserID(user.Id)
	require.ErrorIs(t, err, model.ErrPasskeyNotFound, "a stale authenticated session must not bind a passkey")

	flow, challenge = readFlow(send("/api/user/passkey/register/begin", central.AccessToken, map[string]any{}))
	clientData, err = common.Marshal(map[string]any{"type": "webauthn.create", "challenge": challenge, "origin": "https://dashboard.test"})
	require.NoError(t, err)
	reg["response"] = map[string]any{"clientDataJSON": enc(clientData), "attestationObject": enc(attestation)}
	denyAfterPasskeyStored = true
	response := send("/api/user/passkey/register/finish", central.AccessToken, map[string]any{"flow_token": flow, "credential": reg})
	denyAfterPasskeyStored = false
	require.Contains(t, response.Body.String(), `"success":true`)
	require.Contains(t, response.Body.String(), `"auth_rotation_required":false`)
	if !nonLocal {
		require.NoError(t, f.db.Model(&model.User{}).Where("id = ?", user.Id).Updates(map[string]any{"status": common.UserStatusDisabled, "auth_version": 2}).Error)
	}
	flow, challenge = readFlow(send("/api/user/passkey/login/begin", "", map[string]any{}))
	clientData, err = common.Marshal(map[string]any{"type": "webauthn.get", "challenge": challenge, "origin": "https://dashboard.test"})
	require.NoError(t, err)
	authData = append([]byte{}, rp[:]...)
	authData = append(authData, 0x05, 0, 0, 0, 1)
	clientHash := sha256.Sum256(clientData)
	message := append(append([]byte{}, authData...), clientHash[:]...)
	signed := sha256.Sum256(message)
	signature, err := ecdsa.SignASN1(rand.Reader, private, signed[:])
	require.NoError(t, err)
	assertion := map[string]any{"id": enc(credentialID), "rawId": enc(credentialID), "type": "public-key", "response": map[string]any{"clientDataJSON": enc(clientData), "authenticatorData": enc(authData), "signature": enc(signature), "userHandle": enc([]byte(strconv.Itoa(user.Id)))}}
	if nonLocal {
		require.NoError(t, f.db.Create(&model.NativeLoginCredential{UserID: user.Id, Kind: "passkey", Digest: "conflicting-native-proof"}).Error)
		conflicting := send("/api/user/passkey/login/finish", "", map[string]any{"flow_token": flow, "credential": assertion})
		require.NotContains(t, conflicting.Body.String(), `"success":true`, "ambiguous central and native proof must fail closed")
		require.NoError(t, f.db.Where("user_id = ? AND kind = ?", user.Id, "passkey").Delete(&model.NativeLoginCredential{}).Error)
		flow, challenge = readFlow(send("/api/user/passkey/login/begin", "", map[string]any{}))
		clientData, err = common.Marshal(map[string]any{"type": "webauthn.get", "challenge": challenge, "origin": "https://dashboard.test"})
		require.NoError(t, err)
		clientHash = sha256.Sum256(clientData)
		message = append(append([]byte{}, authData...), clientHash[:]...)
		signed = sha256.Sum256(message)
		signature, err = ecdsa.SignASN1(rand.Reader, private, signed[:])
		require.NoError(t, err)
		assertion["response"] = map[string]any{"clientDataJSON": enc(clientData), "authenticatorData": enc(authData), "signature": enc(signature), "userHandle": enc([]byte(strconv.Itoa(user.Id)))}
	}
	response = send("/api/user/passkey/login/finish", "", map[string]any{"flow_token": flow, "credential": assertion})
	var logged struct {
		Success bool `json:"success"`
		Data    struct {
			AccessToken string                   `json:"access_token"`
			Session     service.LoginSessionView `json:"session"`
		} `json:"data"`
	}
	require.NoError(t, common.Unmarshal(response.Body.Bytes(), &logged))
	require.True(t, logged.Success, response.Body.String())
	require.Equal(t, model.CentralAuthRealm, logged.Data.Session.AuthenticationRealm)
	id, err := service.ParseAccessToken(logged.Data.AccessToken)
	require.NoError(t, err)
	_, _, err = service.ValidateLoginSession(id)
	require.NoError(t, err)
	require.NoError(t, f.db.Transaction(func(tx *gorm.DB) error {
		return model.SetCentralLoginCredentialTx(tx, user.Id, central.Session.SID, "oidc_id", "central-oidc-verified", func(tx *gorm.DB) error {
			return tx.Model(&model.User{}).Where("id = ?", user.Id).Update("oidc_id", "central-oidc-verified").Error
		})
	}))
	oauth, _, err := service.CreateCentralCredentialLogin(context.Background(), user.Id, "oidc_id", "central-oidc-verified", "oauth:fixture", "", "")
	require.NoError(t, err)
	makeProof := func(token string, counter byte) string {
		flow, challenge = readFlow(send("/api/user/passkey/verify/begin", token, map[string]any{"scope": "passkey.delete"}))
		clientData, err = common.Marshal(map[string]any{"type": "webauthn.get", "challenge": challenge, "origin": "https://dashboard.test"})
		require.NoError(t, err)
		authData = append([]byte{}, rp[:]...)
		authData = append(authData, 0x05, 0, 0, 0, counter)
		clientHash = sha256.Sum256(clientData)
		message = append(append([]byte{}, authData...), clientHash[:]...)
		signed = sha256.Sum256(message)
		signature, err = ecdsa.SignASN1(rand.Reader, private, signed[:])
		require.NoError(t, err)
		assertion = map[string]any{"id": enc(credentialID), "rawId": enc(credentialID), "type": "public-key", "response": map[string]any{"clientDataJSON": enc(clientData), "authenticatorData": enc(authData), "signature": enc(signature), "userHandle": enc([]byte(strconv.Itoa(user.Id)))}}
		response = send("/api/user/passkey/verify/finish", token, map[string]any{"flow_token": flow, "credential": assertion})
		var stepUp struct {
			Success bool `json:"success"`
			Data    struct {
				Proof string `json:"proof_token"`
			} `json:"data"`
		}
		require.NoError(t, common.Unmarshal(response.Body.Bytes(), &stepUp))
		require.True(t, stepUp.Success, response.Body.String())
		return stepUp.Data.Proof
	}
	deleteWithProof := func(token, proof string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodDelete, "https://dashboard.test/api/user/passkey", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("X-Security-Proof", proof)
		req.Header.Set("Origin", "https://dashboard.test")
		out := httptest.NewRecorder()
		f.router.ServeHTTP(out, req)
		return out
	}
	passkeyProof := makeProof(logged.Data.AccessToken, 2)
	denied := deleteWithProof(logged.Data.AccessToken, passkeyProof)
	require.NotContains(t, denied.Body.String(), `"success":true`, "a session authenticated by the passkey cannot delete itself")
	_, err = model.GetPasskeyByUserID(user.Id)
	require.NoError(t, err)
	oauthProof := makeProof(oauth.AccessToken, 3)
	advanceDuringBinding = true
	require.NoError(t, f.db.Callback().Update().Before("gorm:update").Register("test:advance-central-session", func(tx *gorm.DB) {
		values, isUpdate := tx.Statement.Dest.(map[string]interface{})
		_, authVersionWrite := values["auth_version"]
		if advanceDuringBinding && tx.Statement.Table == "users" && isUpdate && authVersionWrite {
			advanceDuringBinding = false
			tx.Exec("UPDATE user_sessions SET version = version + 1 WHERE sid = ?", oauth.Session.SID)
		}
	}))
	staleDelete := deleteWithProof(oauth.AccessToken, oauthProof)
	require.NoError(t, f.db.Callback().Update().Remove("test:advance-central-session"))
	require.False(t, advanceDuringBinding)
	require.NotContains(t, staleDelete.Body.String(), `"success":true`)
	_, err = model.GetPasskeyByUserID(user.Id)
	require.NoError(t, err, "a stale authenticated session must not delete the passkey")
	denyAfterPasskeyDeleted = true
	deleted := deleteWithProof(oauth.AccessToken, oauthProof)
	denyAfterPasskeyDeleted = false
	require.Contains(t, deleted.Body.String(), `"success":true`, "a separate verified OAuth session may delete the passkey")
	require.Contains(t, deleted.Body.String(), `"auth_rotation_required":false`)
	_, _, err = service.ValidateLoginSession(id)
	require.Error(t, err, "deleted passkey must invalidate its previously issued session")
	oauthID, err := service.ParseAccessToken(oauth.AccessToken)
	require.NoError(t, err)
	_, _, err = service.ValidateLoginSession(oauthID)
	require.NoError(t, err, "OAuth session remains valid after deleting a different credential")

	// The delete response can be delayed while the same session registers a
	// replacement. Both operations succeed without advancing that session.
	flow, challenge = readFlow(send("/api/user/passkey/register/begin", oauth.AccessToken, map[string]any{}))
	clientData, err = common.Marshal(map[string]any{"type": "webauthn.create", "challenge": challenge, "origin": "https://dashboard.test"})
	require.NoError(t, err)
	authData = append([]byte{}, rp[:]...)
	authData = append(authData, 0x45, 0, 0, 0, 0)
	authData = append(authData, make([]byte, 16)...)
	authData = append(authData, length...)
	authData = append(authData, credentialID...)
	authData = append(authData, cose...)
	attestation, err = cbor.Marshal(map[string]any{"fmt": "none", "attStmt": map[string]any{}, "authData": authData})
	require.NoError(t, err)
	reg = map[string]any{"id": enc(credentialID), "rawId": enc(credentialID), "type": "public-key", "response": map[string]any{"clientDataJSON": enc(clientData), "attestationObject": enc(attestation)}}
	removeSourceDuringBinding := true
	require.NoError(t, f.db.Callback().Update().Before("gorm:update").Register("test:remove-central-source", func(tx *gorm.DB) {
		values, isUpdate := tx.Statement.Dest.(map[string]interface{})
		_, authVersionWrite := values["auth_version"]
		if removeSourceDuringBinding && tx.Statement.Table == "users" && isUpdate && authVersionWrite {
			removeSourceDuringBinding = false
			tx.Exec("DELETE FROM central_login_credentials WHERE user_id = ? AND kind = ?", user.Id, "oidc_id")
		}
	}))
	staleSource := send("/api/user/passkey/register/finish", oauth.AccessToken, map[string]any{"flow_token": flow, "credential": reg})
	require.NoError(t, f.db.Callback().Update().Remove("test:remove-central-source"))
	require.False(t, removeSourceDuringBinding)
	require.NotContains(t, staleSource.Body.String(), `"success":true`)
	_, err = model.GetPasskeyByUserID(user.Id)
	require.ErrorIs(t, err, model.ErrPasskeyNotFound, "registration must not commit after session source is removed")
	flow, challenge = readFlow(send("/api/user/passkey/register/begin", oauth.AccessToken, map[string]any{}))
	clientData, err = common.Marshal(map[string]any{"type": "webauthn.create", "challenge": challenge, "origin": "https://dashboard.test"})
	require.NoError(t, err)
	reg["response"] = map[string]any{"clientDataJSON": enc(clientData), "attestationObject": enc(attestation)}
	replacement := send("/api/user/passkey/register/finish", oauth.AccessToken, map[string]any{"flow_token": flow, "credential": reg})
	require.Contains(t, replacement.Body.String(), `"success":true`)
	for _, result := range []*httptest.ResponseRecorder{deleted, replacement} {
		var body struct {
			Data struct {
				NoRotation *bool                    `json:"auth_rotation_required"`
				Session    service.LoginSessionView `json:"session"`
			} `json:"data"`
		}
		require.NoError(t, common.Unmarshal(result.Body.Bytes(), &body))
		require.NotNil(t, body.Data.NoRotation)
		require.False(t, *body.Data.NoRotation)
		require.Equal(t, oauth.Session.SID, body.Data.Session.SID)
	}
}
