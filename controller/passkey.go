package controller

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	passkeysvc "github.com/QuantumNous/new-api/service/passkey"
	"github.com/QuantumNous/new-api/setting/system_setting"

	"github.com/gin-gonic/gin"
	"github.com/go-webauthn/webauthn/protocol"
	webauthnlib "github.com/go-webauthn/webauthn/webauthn"
	"gorm.io/gorm"
)

const (
	securityProofScopeChannelKeyRead  = "channel.key.read"
	securityProofScopePasskeyRegister = "passkey.register"
	securityProofScopePasskeyDelete   = "passkey.delete"
)

type passkeyFinishRequest struct {
	FlowToken  string          `json:"flow_token"`
	Credential json.RawMessage `json:"credential"`
}

type passkeyVerifyBeginRequest struct {
	Scope string `json:"scope"`
}

func parsePasskeyFinishRequest(c *gin.Context) (*passkeyFinishRequest, error) {
	var request passkeyFinishRequest
	if err := common.DecodeJson(c.Request.Body, &request); err != nil {
		return nil, err
	}
	if request.FlowToken == "" || len(request.Credential) == 0 {
		return nil, errors.New("Passkey 流程参数不完整")
	}
	return &request, nil
}

func PasskeyRegisterBegin(c *gin.Context) {
	if !system_setting.GetPasskeySettings().Enabled {
		c.JSON(http.StatusOK, gin.H{
			"success": false,
			"message": "管理员未启用 Passkey 登录",
		})
		return
	}

	user, err := getAuthenticatedUser(c)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{
			"success": false,
			"message": err.Error(),
		})
		return
	}

	if !requirePasskeyRegistrationVerification(c, user.Id) {
		return
	}

	credential, err := model.GetPasskeyByUserID(user.Id)
	if err != nil && !errors.Is(err, model.ErrPasskeyNotFound) {
		common.ApiError(c, err)
		return
	}
	if errors.Is(err, model.ErrPasskeyNotFound) {
		credential = nil
	}

	wa, err := passkeysvc.BuildWebAuthn(c.Request)
	if err != nil {
		common.ApiError(c, err)
		return
	}

	waUser := passkeysvc.NewWebAuthnUser(user, credential)
	var options []webauthnlib.RegistrationOption
	if credential != nil {
		descriptor := credential.ToWebAuthnCredential().Descriptor()
		options = append(options, webauthnlib.WithExclusions([]protocol.CredentialDescriptor{descriptor}))
	}

	creation, sessionData, err := wa.BeginRegistration(waUser, options...)
	if err != nil {
		common.ApiError(c, err)
		return
	}

	identity, ok := middleware.GetSessionAuthIdentity(c)
	if !ok {
		common.ApiError(c, errors.New("当前认证方式不支持安全验证"))
		return
	}
	flowToken, expiresAt, err := passkeysvc.CreateSessionDataFlow(
		model.AuthFlowPurposePasskeyRegister,
		user.Id,
		identity.SessionID,
		securityProofScopePasskeyRegister,
		sessionData,
	)
	if err != nil {
		common.ApiError(c, err)
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "",
		"data": gin.H{
			"options":    creation,
			"flow_token": flowToken,
			"expires_at": expiresAt,
		},
	})
}

func PasskeyRegisterFinish(c *gin.Context) {
	if !system_setting.GetPasskeySettings().Enabled {
		c.JSON(http.StatusOK, gin.H{
			"success": false,
			"message": "管理员未启用 Passkey 登录",
		})
		return
	}

	user, err := getAuthenticatedUser(c)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{
			"success": false,
			"message": err.Error(),
		})
		return
	}
	if !requirePasskeyRegistrationVerification(c, user.Id) {
		return
	}

	request, err := parsePasskeyFinishRequest(c)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	parsedCredential, err := protocol.ParseCredentialCreationResponseBytes(request.Credential)
	if err != nil {
		common.ApiError(c, err)
		return
	}

	wa, err := passkeysvc.BuildWebAuthn(c.Request)
	if err != nil {
		common.ApiError(c, err)
		return
	}

	credentialRecord, err := model.GetPasskeyByUserID(user.Id)
	if err != nil && !errors.Is(err, model.ErrPasskeyNotFound) {
		common.ApiError(c, err)
		return
	}
	if errors.Is(err, model.ErrPasskeyNotFound) {
		credentialRecord = nil
	}

	identity, ok := middleware.GetSessionAuthIdentity(c)
	if !ok {
		common.ApiError(c, errors.New("当前认证方式不支持安全验证"))
		return
	}
	sessionData, _, err := passkeysvc.PopSessionDataFlow(
		request.FlowToken,
		model.AuthFlowPurposePasskeyRegister,
		user.Id,
		identity.SessionID,
	)
	if err != nil {
		common.ApiError(c, err)
		return
	}

	waUser := passkeysvc.NewWebAuthnUser(user, credentialRecord)
	credential, err := wa.CreateCredential(waUser, *sessionData, parsedCredential)
	if err != nil {
		common.ApiError(c, err)
		return
	}

	passkeyCredential := model.NewPasskeyCredentialFromWebAuthn(user.Id, credential)
	if passkeyCredential == nil {
		common.ApiErrorMsg(c, "无法创建 Passkey 凭证")
		return
	}
	if middleware.CentralRealmAuthenticated(c) {
		bundle, err := service.CurrentSessionBundle(c.Request.Context(), identity)
		if err != nil {
			common.ApiError(c, err)
			return
		}
		value := passkeyCredential.CredentialID + "\x00" + passkeyCredential.PublicKey
		err = model.DB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
			if err := model.ValidateCentralCredentialSessionTx(tx, identity.UserID, identity.SessionID, identity.SessionVersion, identity.UserAuthVersion); err != nil {
				return err
			}
			return model.SetCentralLoginCredentialTx(tx, user.Id, identity.SessionID, "passkey", value, func(tx *gorm.DB) error { return tx.Create(passkeyCredential).Error })
		})
		if err != nil {
			common.ApiError(c, err)
			return
		}
		recordUserSecurityAudit(c, user.Id, "user.passkey_register", nil)
		data := authRotationData(bundle)
		data["auth_rotation_required"] = false
		c.JSON(http.StatusOK, gin.H{"success": true, "message": "Passkey 注册成功", "data": data})
		return
	}

	bundle, err := service.ChangePasskeyForCurrentSession(identity, tokenRequestRealm(c), passkeyCredential)
	if err != nil {
		common.ApiError(c, err)
		return
	}

	recordUserSecurityAudit(c, user.Id, "user.passkey_register", nil)
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "Passkey 注册成功",
		"data":    authRotationData(bundle),
	})
}

func PasskeyDelete(c *gin.Context) {
	user, err := getAuthenticatedUser(c)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{
			"success": false,
			"message": err.Error(),
		})
		return
	}

	if !requirePasskeyDeleteVerification(c, user.Id) {
		return
	}

	identity, ok := middleware.GetSessionAuthIdentity(c)
	if !ok {
		common.ApiError(c, errors.New("当前认证方式不支持安全验证"))
		return
	}
	if middleware.CentralRealmAuthenticated(c) {
		bundle, err := service.CurrentSessionBundle(c.Request.Context(), identity)
		if err != nil {
			common.ApiError(c, err)
			return
		}
		err = model.DB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
			if err := model.ValidateCentralCredentialSessionTx(tx, identity.UserID, identity.SessionID, identity.SessionVersion, identity.UserAuthVersion); err != nil {
				return err
			}
			return model.DeleteCentralLoginCredentialTx(tx, user.Id, identity.SessionID, "passkey", func(tx *gorm.DB) error {
				return tx.Unscoped().Where("user_id = ?", user.Id).Delete(&model.PasskeyCredential{}).Error
			})
		})
		if err != nil {
			common.ApiError(c, err)
			return
		}
		recordUserSecurityAudit(c, user.Id, "user.passkey_delete", nil)
		data := authRotationData(bundle)
		data["auth_rotation_required"] = false
		c.JSON(http.StatusOK, gin.H{"success": true, "message": "Passkey 已解绑", "data": data})
		return
	}
	bundle, err := service.ChangePasskeyForCurrentSession(identity, tokenRequestRealm(c), nil)
	if err != nil {
		common.ApiError(c, err)
		return
	}

	recordUserSecurityAudit(c, user.Id, "user.passkey_delete", nil)
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "Passkey 已解绑",
		"data":    authRotationData(bundle),
	})
}

func PasskeyStatus(c *gin.Context) {
	user, err := getAuthenticatedUser(c)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{
			"success": false,
			"message": err.Error(),
		})
		return
	}

	credential, err := model.GetPasskeyByUserID(user.Id)
	if errors.Is(err, model.ErrPasskeyNotFound) {
		c.JSON(http.StatusOK, gin.H{
			"success": true,
			"message": "",
			"data": gin.H{
				"enabled": false,
			},
		})
		return
	}
	if err != nil {
		common.ApiError(c, err)
		return
	}
	if !passkeyBelongsToSession(c, user, credential) {
		return
	}

	data := gin.H{
		"enabled":      true,
		"last_used_at": credential.LastUsedAt,
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "",
		"data":    data,
	})
}

func PasskeyLoginBegin(c *gin.Context) {
	if !system_setting.GetPasskeySettings().Enabled {
		c.JSON(http.StatusOK, gin.H{
			"success": false,
			"message": "管理员未启用 Passkey 登录",
		})
		return
	}

	wa, err := passkeysvc.BuildWebAuthn(c.Request)
	if err != nil {
		common.ApiError(c, err)
		return
	}

	assertion, sessionData, err := wa.BeginDiscoverableLogin()
	if err != nil {
		common.ApiError(c, err)
		return
	}

	flowToken, expiresAt, err := passkeysvc.CreateSessionDataFlow(
		model.AuthFlowPurposePasskeyLogin,
		0,
		"",
		"",
		sessionData,
	)
	if err != nil {
		common.ApiError(c, err)
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "",
		"data": gin.H{
			"options":    assertion,
			"flow_token": flowToken,
			"expires_at": expiresAt,
		},
	})
}

func PasskeyLoginFinish(c *gin.Context) {
	if !system_setting.GetPasskeySettings().Enabled {
		c.JSON(http.StatusOK, gin.H{
			"success": false,
			"message": "管理员未启用 Passkey 登录",
		})
		return
	}

	request, err := parsePasskeyFinishRequest(c)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	parsedCredential, err := protocol.ParseCredentialRequestResponseBytes(request.Credential)
	if err != nil {
		common.ApiError(c, err)
		return
	}

	wa, err := passkeysvc.BuildWebAuthn(c.Request)
	if err != nil {
		common.ApiError(c, err)
		return
	}

	sessionData, _, err := passkeysvc.PopSessionDataFlow(
		request.FlowToken,
		model.AuthFlowPurposePasskeyLogin,
		0,
		"",
	)
	if err != nil {
		common.ApiError(c, err)
		return
	}

	handler := func(rawID, userHandle []byte) (webauthnlib.User, error) {
		// 首先通过凭证ID查找用户
		credential, err := model.GetPasskeyByCredentialID(rawID)
		if err != nil {
			return nil, fmt.Errorf("未找到 Passkey 凭证: %w", err)
		}

		// 通过凭证获取用户
		user := &model.User{Id: credential.UserID}
		if err := user.FillUserById(); err != nil {
			return nil, fmt.Errorf("用户信息获取失败: %w", err)
		}

		if e := model.SyncProductAuthVersions(c.Request.Context(), user.Id); e != nil {
			return nil, e
		}
		if e := model.ReconcileProductIdentity(c.Request.Context(), user); e != nil {
			return nil, e
		}
		if user.Status != common.UserStatusEnabled {
			central, centralErr := model.IsCentralLoginCredential(c.Request.Context(), user.Id, "passkey", credential.CredentialID+"\x00"+credential.PublicKey)
			if centralErr != nil || !central {
				return nil, errors.New("该用户已被禁用")
			}
		}

		if len(userHandle) > 0 {
			userID, parseErr := strconv.Atoi(string(userHandle))
			if parseErr != nil {
				// 记录异常但继续验证，因为某些客户端可能使用非数字格式
				common.SysLog(fmt.Sprintf("PasskeyLogin: userHandle parse error for credential, length: %d", len(userHandle)))
			} else if userID != user.Id {
				return nil, errors.New("用户句柄与凭证不匹配")
			}
		}

		return passkeysvc.NewWebAuthnUser(user, credential), nil
	}

	waUser, credential, err := wa.ValidatePasskeyLogin(handler, *sessionData, parsedCredential)
	if err != nil {
		common.ApiError(c, err)
		return
	}

	userWrapper, ok := waUser.(*passkeysvc.WebAuthnUser)
	if !ok {
		common.ApiErrorMsg(c, "Passkey 登录状态异常")
		return
	}

	modelUser := userWrapper.ModelUser()
	if modelUser == nil {
		common.ApiErrorMsg(c, "Passkey 登录状态异常")
		return
	}

	stored, err := model.GetPasskeyByCredentialID(credential.ID)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	native, err := model.IsNativeLoginCredential(c.Request.Context(), modelUser.Id, "passkey", stored.CredentialID)
	var centralProof model.CentralLoginCredential
	centralLookup := model.DB.WithContext(c.Request.Context()).Where("user_id = ? AND kind = ?", modelUser.Id, "passkey").First(&centralProof).Error
	if centralLookup == nil {
		var nativeProof model.NativeLoginCredential
		nativeLookup := model.DB.WithContext(c.Request.Context()).Where("user_id = ? AND kind = ?", modelUser.Id, "passkey").First(&nativeProof).Error
		if nativeLookup == nil || !errors.Is(nativeLookup, gorm.ErrRecordNotFound) {
			common.ApiError(c, model.ErrNativeCredentialUnverified)
			return
		}
		value := stored.CredentialID + "\x00" + stored.PublicKey
		central, centralErr := model.IsCentralLoginCredential(c.Request.Context(), modelUser.Id, "passkey", value)
		if centralErr != nil || !central {
			common.ApiError(c, model.ErrNativeCredentialUnverified)
			return
		}
		c.Set("central_credential_kind", "passkey")
		c.Set("central_credential_value", value)
	} else if !errors.Is(centralLookup, gorm.ErrRecordNotFound) {
		common.ApiError(c, centralLookup)
		return
	} else if err != nil {
		common.ApiError(c, err)
		return
	} else if modelUser.Status != common.UserStatusEnabled {
		common.ApiErrorMsg(c, "该用户已被禁用")
		return
	}
	if native {
		c.Set("native_password_authenticated", true)
	}
	if err := model.UpdatePasskeyAssertionState(modelUser.Id, credential, time.Now()); err != nil {
		common.ApiError(c, err)
		return
	}
	setupLogin(modelUser, c)
}

func AdminResetPasskey(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		common.ApiErrorMsg(c, "无效的用户 ID")
		return
	}

	user := &model.User{Id: id}
	if err := user.FillUserById(); err != nil {
		common.ApiError(c, err)
		return
	}
	myRole := c.GetInt("role")
	if !refreshManagedTarget(c, user) {
		return
	}
	if !canManageTargetRole(myRole, user.Role) {
		common.ApiErrorMsg(c, "no permission")
		return
	}

	if !authorizeProductIdentityWrite(c, user.Id, "passkey") {
		return
	}
	if _, err := model.GetPasskeyByUserID(user.Id); err != nil {
		if errors.Is(err, model.ErrPasskeyNotFound) {
			c.JSON(http.StatusOK, gin.H{
				"success": false,
				"message": "该用户尚未绑定 Passkey",
			})
			return
		}
		common.ApiError(c, err)
		return
	}

	if err := model.DeletePasskeyByUserIDWithAuthVersion(user.Id, tokenRequestRealm(c)); err != nil {
		common.ApiError(c, err)
		return
	}
	if _, err := model.RevokeAllUserSessions(user.Id, "admin_passkey_reset"); err != nil {
		common.ApiError(c, err)
		return
	}

	recordManageAuditFor(c, user.Id, "user.reset_passkey", map[string]interface{}{
		"username": user.Username,
		"id":       user.Id,
	})
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "Passkey 已重置",
	})
}

func PasskeyVerifyBegin(c *gin.Context) {
	if !system_setting.GetPasskeySettings().Enabled {
		c.JSON(http.StatusOK, gin.H{
			"success": false,
			"message": "管理员未启用 Passkey 登录",
		})
		return
	}

	user, err := getAuthenticatedUser(c)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{
			"success": false,
			"message": err.Error(),
		})
		return
	}
	var request passkeyVerifyBeginRequest
	if err := common.DecodeJson(c.Request.Body, &request); err != nil {
		common.ApiError(c, errors.New("无效的 Passkey 验证请求"))
		return
	}
	if !isAllowedSecurityProofScope(request.Scope) {
		common.ApiError(c, errors.New("不支持的安全验证范围"))
		return
	}

	credential, err := model.GetPasskeyByUserID(user.Id)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{
			"success": false,
			"message": "该用户尚未绑定 Passkey",
		})
		return
	}
	if !passkeyBelongsToSession(c, user, credential) {
		return
	}

	wa, err := passkeysvc.BuildWebAuthn(c.Request)
	if err != nil {
		common.ApiError(c, err)
		return
	}

	waUser := passkeysvc.NewWebAuthnUser(user, credential)
	assertion, sessionData, err := wa.BeginLogin(waUser)
	if err != nil {
		common.ApiError(c, err)
		return
	}

	identity, ok := middleware.GetSessionAuthIdentity(c)
	if !ok {
		common.ApiError(c, errors.New("当前认证方式不支持安全验证"))
		return
	}
	flowToken, expiresAt, err := passkeysvc.CreateSessionDataFlow(
		model.AuthFlowPurposePasskeyStepUp,
		user.Id,
		identity.SessionID,
		request.Scope,
		sessionData,
	)
	if err != nil {
		common.ApiError(c, err)
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "",
		"data": gin.H{
			"options":    assertion,
			"flow_token": flowToken,
			"expires_at": expiresAt,
		},
	})
}

func PasskeyVerifyFinish(c *gin.Context) {
	if !system_setting.GetPasskeySettings().Enabled {
		c.JSON(http.StatusOK, gin.H{
			"success": false,
			"message": "管理员未启用 Passkey 登录",
		})
		return
	}

	user, err := getAuthenticatedUser(c)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{
			"success": false,
			"message": err.Error(),
		})
		return
	}

	request, err := parsePasskeyFinishRequest(c)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	parsedCredential, err := protocol.ParseCredentialRequestResponseBytes(request.Credential)
	if err != nil {
		common.ApiError(c, err)
		return
	}

	wa, err := passkeysvc.BuildWebAuthn(c.Request)
	if err != nil {
		common.ApiError(c, err)
		return
	}

	credential, err := model.GetPasskeyByUserID(user.Id)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{
			"success": false,
			"message": "该用户尚未绑定 Passkey",
		})
		return
	}
	if !passkeyBelongsToSession(c, user, credential) {
		return
	}

	identity, ok := middleware.GetSessionAuthIdentity(c)
	if !ok {
		common.ApiError(c, errors.New("当前认证方式不支持安全验证"))
		return
	}
	sessionData, scope, err := passkeysvc.PopSessionDataFlow(
		request.FlowToken,
		model.AuthFlowPurposePasskeyStepUp,
		user.Id,
		identity.SessionID,
	)
	if err != nil {
		common.ApiError(c, err)
		return
	}

	waUser := passkeysvc.NewWebAuthnUser(user, credential)
	validatedCredential, err := wa.ValidateLogin(waUser, *sessionData, parsedCredential)
	if err != nil {
		common.ApiError(c, err)
		return
	}

	if err := model.UpdatePasskeyAssertionState(user.Id, validatedCredential, time.Now()); err != nil {
		common.ApiError(c, err)
		return
	}

	proofToken, proofExpiresAt, err := service.IssueSecurityProof(identity, secureVerificationMethodPasskey, []string{scope})
	if err != nil {
		common.ApiError(c, err)
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "Passkey 验证成功",
		"data": gin.H{
			"proof_token": proofToken,
			"expires_at":  proofExpiresAt,
			"method":      secureVerificationMethodPasskey,
			"scope":       scope,
		},
	})
}

func passkeyBelongsToSession(c *gin.Context, user *model.User, credential *model.PasskeyCredential) bool {
	if middleware.CentralRealmAuthenticated(c) {
		valid, err := model.IsCentralLoginCredential(c.Request.Context(), user.Id, "passkey", credential.CredentialID+"\x00"+credential.PublicKey)
		if err == nil && valid {
			return true
		}
		common.ApiError(c, model.ErrNativeCredentialUnverified)
		return false
	}
	if middleware.NativeRealmAuthenticated(c) {
		valid, err := model.IsNativeLoginCredential(c.Request.Context(), user.Id, "passkey", credential.CredentialID)
		if err == nil && valid {
			return true
		}
		common.ApiError(c, model.ErrNativeCredentialUnverified)
		return false
	}
	return true
}

func getAuthenticatedUser(c *gin.Context) (*model.User, error) {
	id := c.GetInt("id")
	if id == 0 {
		return nil, errors.New("未登录")
	}
	user := &model.User{Id: id}
	if err := user.FillUserById(); err != nil {
		return nil, err
	}
	if user.Status != common.UserStatusEnabled && !middleware.CentralRealmAuthenticated(c) {
		return nil, errors.New("该用户已被禁用")
	}
	return user, nil
}

func requirePasskeyRegistrationVerification(c *gin.Context, userID int) bool {
	if middleware.CentralRealmAuthenticated(c) {
		// Initial central enrollment cannot reuse a native or unknown slot.
		if _, err := model.GetPasskeyByUserID(userID); err == nil || !errors.Is(err, model.ErrPasskeyNotFound) {
			common.ApiError(c, model.ErrNativeCredentialUnverified)
			return false
		}
		return true
	}
	twoFA, err := model.GetTwoFAByUserId(userID)
	if err != nil {
		common.ApiError(c, err)
		return false
	}
	if twoFA == nil || !twoFA.IsEnabled {
		return true
	}
	return middleware.RequireSecurityProof(c, securityProofScopePasskeyRegister, []string{secureVerificationMethod2FA})
}

func requirePasskeyDeleteVerification(c *gin.Context, userID int) bool {
	if middleware.CentralRealmAuthenticated(c) {
		credential, err := model.GetPasskeyByUserID(userID)
		if err != nil {
			common.ApiError(c, err)
			return false
		}
		central, err := model.IsCentralLoginCredential(c.Request.Context(), userID, "passkey", credential.CredentialID+"\x00"+credential.PublicKey)
		if err != nil || !central {
			common.ApiError(c, model.ErrNativeCredentialUnverified)
			return false
		}
		return middleware.RequireSecurityProof(c, securityProofScopePasskeyDelete, []string{secureVerificationMethodPasskey})
	}
	twoFA, err := model.GetTwoFAByUserId(userID)
	if err != nil {
		common.ApiError(c, err)
		return false
	}
	if twoFA != nil && twoFA.IsEnabled {
		return middleware.RequireSecurityProof(c, securityProofScopePasskeyDelete, []string{secureVerificationMethod2FA})
	}

	_, err = model.GetPasskeyByUserID(userID)
	if err != nil {
		if errors.Is(err, model.ErrPasskeyNotFound) {
			c.JSON(http.StatusOK, gin.H{
				"success": false,
				"message": "该用户尚未绑定 Passkey",
			})
			return false
		}
		common.ApiError(c, err)
		return false
	}

	return middleware.RequireSecurityProof(c, securityProofScopePasskeyDelete, []string{secureVerificationMethodPasskey})
}
