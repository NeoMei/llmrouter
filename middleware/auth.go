package middleware

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/internal/platformaccount"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/service/authz"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/QuantumNous/new-api/types"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

const authIdentityContextKey = "auth_identity"
const centralRealmContextKey = "authenticated_central_realm"

func CentralRealmAuthenticated(c *gin.Context) bool { return c.GetBool(centralRealmContextKey) }

type dashboardCredentialKind int

const (
	dashboardCredentialUnmatched dashboardCredentialKind = iota
	dashboardCredentialInternal
	dashboardCredentialPAT
)

func validUserInfo(username string, role int) bool {
	// check username is empty
	if strings.TrimSpace(username) == "" {
		return false
	}
	if !common.IsValidateRole(role) {
		return false
	}
	return true
}

func authHelper(c *gin.Context, minRole int) {
	user, identity, useAccessToken, err := authenticateDashboardRequest(c)
	if err != nil {
		writeDashboardAuthError(c, err)
		return
	}
	if user.Status != common.UserStatusEnabled && !c.GetBool(centralRealmContextKey) {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"success": false, "code": "AUTH_USER_DISABLED", "message": common.TranslateMessage(c, i18n.MsgAuthUserBanned)})
		return
	}
	if !nativeAuthenticatorBoundary(c, user.Id) {
		return
	}
	maintenance := !useAccessToken && nativePrivateMaintenanceRead(c, user.Id)
	managed := (platformaccount.RuntimeEnabled() || user.IdentitySource == model.UniHubIdentitySource) && !maintenance && !NativeRealmAuthenticated(c)
	if managed {
		if platformaccount.RuntimeEnabled() {
			version := user.AuthVersion
			if c.GetBool(centralRealmContextKey) {
				version = identity.UserAuthVersion
			}
			if !LoadRuntimeAuthority(c, user.Id, version) {
				return
			}
		} else {
			if useAccessToken {
				c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"success": false, "code": "UNIHUB_RUNTIME_AUTHORITY_REQUIRED"})
				return
			}
			if !loadUniHubAuthority(c, identity) {
				return
			}
		}
		p, _ := authz.ProductPrincipal(c)
		user.Role = RoleFromAccess(p.Access)
	}
	permissions, management := uniHubManagementPermission(c)
	if managed && minRole >= common.RoleAdminUser && !management {
		permissions, management = fallbackManagementPermission(c)
		if !management {
			c.AbortWithStatusJSON(403, gin.H{"success": false, "code": "UNIHUB_UNDECLARED_MANAGEMENT_ROUTE"})
			return
		}
	}
	runtimeSettings := managed && management && minRole == common.RoleRootUser && len(permissions) == 1 && permissions[0].Resource == "settings"
	if managed && management {
		permitted := false
		for _, permission := range permissions {
			permitted = permitted || authz.CanRequest(c, permission)
		}
		if !permitted {
			c.AbortWithStatusJSON(403, gin.H{"success": false, "code": "UNIHUB_PERMISSION_DENIED"})
			return
		}
	}
	if managed && management && c.Request.Method != "GET" && c.Request.Method != "HEAD" && c.Request.Method != "OPTIONS" {
		validOrigin := false
		if len(c.Request.Header.Values("Origin")) == 1 {
			if platformaccount.RuntimeEnabled() {
				validOrigin = isAllowedSessionOrigin(c.Request, c.GetHeader("Origin"))
			} else {
				cfg, e := platformaccount.Current()
				origin, _ := url.Parse(cfg.RedirectURI)
				validOrigin = e == nil && c.GetHeader("Origin") == origin.Scheme+"://"+origin.Host
			}
		}
		if !validOrigin {
			c.AbortWithStatusJSON(403, gin.H{"success": false, "code": "UNIHUB_ORIGIN_FORBIDDEN"})
			return
		}
	}
	if user.Role < minRole && !runtimeSettings {
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"success": false, "code": "AUTH_INSUFFICIENT_PRIVILEGE"})
		return
	}
	if !validUserInfo(user.Username, user.Role) {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"success": false, "code": "AUTH_USER_INVALID", "message": common.TranslateMessage(c, i18n.MsgAuthUserInfoInvalid)})
		return
	}

	setDashboardAuthContext(c, user, identity, useAccessToken)

	// 管理/root 写操作审计兜底：内聚在鉴权链路里，保证任何经过 AdminAuth/RootAuth
	// 的写接口都会自动留痕（无需在路由上单独挂审计中间件，避免漏挂）。
	// handler 内手动埋点者会设置 ContextKeyAuditLogged，finishAdminAudit 据此跳过。
	var auditWriter *auditResponseWriter
	if minRole >= common.RoleAdminUser {
		auditWriter = beginAdminAudit(c)
	}

	c.Next()

	finishAdminAudit(c, auditWriter)
}

func allowedUniHubPath(c *gin.Context) bool { return true }

func TryUserAuth() func(c *gin.Context) {
	return func(c *gin.Context) {
		user, identity, credentialKind, err := classifyDashboardCredential(c)
		if err != nil {
			writeDashboardAuthError(c, err)
			return
		}
		if credentialKind != dashboardCredentialUnmatched {
			if platformaccount.RuntimeEnabled() && !NativeRealmAuthenticated(c) {
				version := user.AuthVersion
				if c.GetBool(centralRealmContextKey) {
					version = identity.UserAuthVersion
				}
				if !LoadRuntimeAuthority(c, user.Id, version) {
					return
				}
				p, _ := authz.ProductPrincipal(c)
				user.Role = RoleFromAccess(p.Access)
			} else if user.IdentitySource == model.UniHubIdentitySource && !NativeRealmAuthenticated(c) {
				if !loadUniHubAuthority(c, identity) {
					return
				}
				p, _ := authz.ProductPrincipal(c)
				user.Role = RoleFromAccess(p.Access)
			}
			setDashboardAuthContext(c, user, identity, credentialKind == dashboardCredentialPAT)
		}
		c.Next()
	}
}

func chatCredentialOriginMatches(boundOrigin, rawOrigin string) bool {
	bound, err := url.Parse(boundOrigin)
	if err != nil || bound.Scheme == "" || bound.Host == "" {
		return false
	}
	origin, err := url.Parse(strings.TrimSpace(rawOrigin))
	if err != nil || origin.Scheme == "" || origin.Host == "" {
		return false
	}
	return strings.EqualFold(bound.Scheme, origin.Scheme) && strings.EqualFold(bound.Host, origin.Host)
}

func UserAuth() func(c *gin.Context) {
	return func(c *gin.Context) {
		authHelper(c, common.RoleCommonUser)
	}
}

func AdminAuth() func(c *gin.Context) {
	return func(c *gin.Context) {
		authHelper(c, common.RoleAdminUser)
	}
}

func RootAuth() func(c *gin.Context) {
	return func(c *gin.Context) {
		authHelper(c, common.RoleRootUser)
	}
}

// GetAuthIdentity returns a dashboard session identity. PAT-authenticated
// requests intentionally have no SessionID and cannot manage browser sessions.
func GetAuthIdentity(c *gin.Context) (service.AuthIdentity, bool) {
	value, ok := c.Get(authIdentityContextKey)
	if !ok {
		return service.AuthIdentity{}, false
	}
	identity, ok := value.(service.AuthIdentity)
	return identity, ok
}

// GetSessionAuthIdentity returns only identities backed by a live dashboard
// session. PAT-authenticated requests intentionally fail this check.
func GetSessionAuthIdentity(c *gin.Context) (service.AuthIdentity, bool) {
	identity, ok := GetAuthIdentity(c)
	if !ok {
		identity = service.AuthIdentity{
			UserID:          c.GetInt("id"),
			SessionID:       c.GetString("session_id"),
			UserAuthVersion: c.GetInt64("auth_version"),
			SessionVersion:  c.GetInt64("session_version"),
		}
	}
	if identity.UserID <= 0 || identity.SessionID == "" || identity.UserAuthVersion <= 0 || identity.SessionVersion <= 0 {
		return service.AuthIdentity{}, false
	}
	return identity, true
}

func authenticateDashboardRequest(c *gin.Context) (*model.UserBase, service.AuthIdentity, bool, error) {
	user, identity, credentialKind, err := classifyDashboardCredential(c)
	if err != nil {
		return nil, service.AuthIdentity{}, credentialKind == dashboardCredentialPAT, err
	}
	if credentialKind == dashboardCredentialUnmatched {
		return nil, service.AuthIdentity{}, false, service.ErrAuthTokenInvalid
	}
	return user, identity, credentialKind == dashboardCredentialPAT, nil
}

func classifyDashboardCredential(c *gin.Context) (*model.UserBase, service.AuthIdentity, dashboardCredentialKind, error) {
	if len(c.Request.Header.Values("Authorization")) > 1 {
		return nil, service.AuthIdentity{}, dashboardCredentialInternal, service.ErrAuthTokenInvalid
	}
	raw, ok := authorizationToken(c.GetHeader("Authorization"))
	if !ok {
		return nil, service.AuthIdentity{}, dashboardCredentialUnmatched, nil
	}
	identity, internal, err := service.ParseDashboardAccessToken(raw)
	if internal {
		if err != nil {
			return nil, service.AuthIdentity{}, dashboardCredentialInternal, err
		}
		session, user, err := service.ValidateLoginSession(identity)
		if err != nil {
			return nil, service.AuthIdentity{}, dashboardCredentialInternal, err
		}
		if err := service.VerifyBrokerSource(c.Request.Context(), identity); err != nil {
			return nil, service.AuthIdentity{}, dashboardCredentialInternal, err
		}
		if session.AuthRealm == model.CentralAuthRealm {
			if session.LoginMethod == "unihub" {
				if _, _, err := service.ResolveUniHubSession(c.Request.Context(), identity); err != nil {
					return nil, service.AuthIdentity{}, dashboardCredentialInternal, service.ErrBrokerSourceUnavailable
				}
			} else if session.LoginMethod != "product_broker" && session.LoginMethod != "central_credential" {
				return nil, service.AuthIdentity{}, dashboardCredentialInternal, service.ErrLoginSessionRevoked
			}
			c.Set(centralRealmContextKey, true)
		}
		native, err := authenticateNativeSession(c, identity)
		if err != nil {
			return nil, service.AuthIdentity{}, dashboardCredentialInternal, err
		}
		if !native {
			if err := validateProjectionCredential(user); err != nil {
				return nil, service.AuthIdentity{}, dashboardCredentialInternal, err
			}
		}
		return user, identity, dashboardCredentialInternal, nil
	}
	patUser, err := model.ValidateAccessToken(raw)
	if err != nil {
		return nil, service.AuthIdentity{}, dashboardCredentialPAT, err
	}
	if patUser == nil || patUser.Id <= 0 {
		return nil, service.AuthIdentity{}, dashboardCredentialUnmatched, nil
	}
	native, nativeErr := model.IsNativeLoginCredential(c.Request.Context(), patUser.Id, "pat", raw)
	{
		var centralProof model.CentralLoginCredential
		proofErr := model.DB.WithContext(c.Request.Context()).Where("user_id = ? AND kind = ?", patUser.Id, "pat").First(&centralProof).Error
		if proofErr != nil && !errors.Is(proofErr, gorm.ErrRecordNotFound) {
			return nil, service.AuthIdentity{}, dashboardCredentialPAT, service.ErrAuthStoreUnavailable
		}
		if proofErr == nil {
			if os.Getenv("UNIHUB_INDEPENDENT_CENTRAL_REALM_ENABLED") != "true" {
				return nil, service.AuthIdentity{}, dashboardCredentialPAT, service.ErrAuthStoreUnavailable
			}
			var nativeProof model.NativeLoginCredential
			conflictErr := model.DB.WithContext(c.Request.Context()).Where("user_id = ? AND kind = ?", patUser.Id, "pat").First(&nativeProof).Error
			if conflictErr == nil {
				return nil, service.AuthIdentity{}, dashboardCredentialPAT, service.ErrAuthTokenInvalid
			}
			if !errors.Is(conflictErr, gorm.ErrRecordNotFound) {
				return nil, service.AuthIdentity{}, dashboardCredentialPAT, service.ErrAuthStoreUnavailable
			}
			central, centralErr := model.IsCentralLoginCredential(c.Request.Context(), patUser.Id, "pat", raw)
			if errors.Is(centralErr, model.ErrNativeCredentialUnverified) || (centralErr == nil && !central) {
				return nil, service.AuthIdentity{}, dashboardCredentialPAT, service.ErrAuthTokenInvalid
			}
			if centralErr != nil {
				return nil, service.AuthIdentity{}, dashboardCredentialPAT, service.ErrAuthStoreUnavailable
			}
			state, stateErr := model.ReadCentralAuthState(c.Request.Context(), patUser.Id)
			if stateErr != nil || !state.Active {
				return nil, service.AuthIdentity{}, dashboardCredentialPAT, service.ErrAuthStoreUnavailable
			}
			user, userErr := model.GetUserCache(patUser.Id)
			if userErr != nil {
				return nil, service.AuthIdentity{}, dashboardCredentialPAT, userErr
			}
			if projectionErr := validateProjectionCredential(user); projectionErr != nil {
				return nil, service.AuthIdentity{}, dashboardCredentialPAT, projectionErr
			}
			c.Set(centralRealmContextKey, true)
			return user, service.AuthIdentity{UserID: user.Id, UserAuthVersion: state.AuthVersion}, dashboardCredentialPAT, nil
		}
		if !native && nativeErr == nil {
			// An unproven PAT may be a historical legacy credential. Do not
			// downgrade it to legacy when this user has central-realm evidence.
			// Each read must succeed: unavailable evidence is not absence.
			var centralState model.CentralAuthState
			stateErr := model.DB.WithContext(c.Request.Context()).Where("user_id = ?", patUser.Id).First(&centralState).Error
			if stateErr == nil {
				return nil, service.AuthIdentity{}, dashboardCredentialPAT, service.ErrAuthTokenInvalid
			}
			if !errors.Is(stateErr, gorm.ErrRecordNotFound) {
				return nil, service.AuthIdentity{}, dashboardCredentialPAT, service.ErrAuthStoreUnavailable
			}
			var centralSession model.UserSession
			sessionErr := model.DB.WithContext(c.Request.Context()).Where("user_id = ? AND auth_realm = ?", patUser.Id, model.CentralAuthRealm).First(&centralSession).Error
			if sessionErr == nil {
				return nil, service.AuthIdentity{}, dashboardCredentialPAT, service.ErrAuthTokenInvalid
			}
			if !errors.Is(sessionErr, gorm.ErrRecordNotFound) {
				return nil, service.AuthIdentity{}, dashboardCredentialPAT, service.ErrAuthStoreUnavailable
			}
			var otherProof model.CentralLoginCredential
			otherErr := model.DB.WithContext(c.Request.Context()).Where("user_id = ?", patUser.Id).First(&otherProof).Error
			if otherErr == nil {
				return nil, service.AuthIdentity{}, dashboardCredentialPAT, service.ErrAuthTokenInvalid
			}
			if !errors.Is(otherErr, gorm.ErrRecordNotFound) {
				return nil, service.AuthIdentity{}, dashboardCredentialPAT, service.ErrAuthStoreUnavailable
			}
			var sessionProof model.CentralSessionCredential
			bindingErr := model.DB.WithContext(c.Request.Context()).Where("user_id = ?", patUser.Id).First(&sessionProof).Error
			if bindingErr == nil {
				return nil, service.AuthIdentity{}, dashboardCredentialPAT, service.ErrAuthTokenInvalid
			}
			if !errors.Is(bindingErr, gorm.ErrRecordNotFound) {
				return nil, service.AuthIdentity{}, dashboardCredentialPAT, service.ErrAuthStoreUnavailable
			}
		}
	}
	if nativeErr != nil {
		return nil, service.AuthIdentity{}, dashboardCredentialPAT, nativeErr
	}
	if native {
		c.Set(nativeRealmContextKey, true)
	}
	user, err := model.GetUserCache(patUser.Id)
	if err != nil {
		return nil, service.AuthIdentity{}, dashboardCredentialPAT, err
	}
	if err := validateProjectionCredential(user); !native && err != nil {
		return nil, service.AuthIdentity{}, dashboardCredentialPAT, err
	}
	return user, service.AuthIdentity{UserID: user.Id, UserAuthVersion: user.AuthVersion}, dashboardCredentialPAT, nil
}

func validateProjectionCredential(user *model.UserBase) error {
	projected, err := model.CheckUniHubProjection(user.Id)
	if err != nil {
		return err
	}
	if projected {
		user.IdentitySource = model.UniHubIdentitySource
	}
	return nil
}

func authorizationToken(header string) (string, bool) {
	header = strings.TrimSpace(header)
	if header == "" {
		return "", false
	}
	parts := strings.Fields(header)
	if len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") {
		header = parts[1]
	} else if len(parts) != 1 {
		return "", false
	}
	return header, header != ""
}

func setDashboardAuthContext(c *gin.Context, user *model.UserBase, identity service.AuthIdentity, useAccessToken bool) {
	c.Header("Auth-Version", "864b7076dbcd0a3c01b5520316720ebf")
	c.Set("username", user.Username)
	c.Set("role", user.Role)
	c.Set("id", user.Id)
	c.Set("group", user.Group)
	c.Set("user_group", user.Group)
	c.Set("use_access_token", useAccessToken)
	c.Set("session_id", identity.SessionID)
	c.Set("auth_version", identity.UserAuthVersion)
	c.Set("session_version", identity.SessionVersion)
	c.Set(authIdentityContextKey, identity)
	user.WriteContext(c)
}

func writeDashboardAuthError(c *gin.Context, err error) {
	if errors.Is(err, service.ErrAuthStoreUnavailable) {
		c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{"success": false, "code": "AUTH_STORE_UNAVAILABLE"})
		return
	}
	if errors.Is(err, service.ErrBrokerSourceUnavailable) {
		c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{"success": false, "code": "UNIHUB_SOURCE_UNAVAILABLE"})
		return
	}
	if errors.Is(err, service.ErrAuthTokenExpired) {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"success": false, "code": "AUTH_TOKEN_EXPIRED", "message": common.TranslateMessage(c, i18n.MsgAuthNotLoggedIn)})
		return
	}
	if errors.Is(err, service.ErrLoginSessionRevoked) || errors.Is(err, gorm.ErrRecordNotFound) {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"success": false, "code": "AUTH_SESSION_REVOKED", "message": common.TranslateMessage(c, i18n.MsgAuthNotLoggedIn)})
		return
	}
	if errors.Is(err, service.ErrAuthTokenInvalid) {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"success": false, "code": "AUTH_UNAUTHORIZED", "message": common.TranslateMessage(c, i18n.MsgAuthAccessTokenInvalid)})
		return
	}
	common.SysLog("dashboard authentication error: " + err.Error())
	c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"success": false, "code": "AUTH_INTERNAL_ERROR", "message": common.TranslateMessage(c, i18n.MsgDatabaseError)})
}

func RequirePermission(permission authz.Permission) func(c *gin.Context) {
	return func(c *gin.Context) {
		role := c.GetInt("role")
		userID := c.GetInt("id")
		_ = role
		_ = userID
		if authz.CanRequest(c, permission) {
			c.Next()
			return
		}
		c.JSON(http.StatusForbidden, gin.H{
			"success": false,
			"message": common.TranslateMessage(c, i18n.MsgAuthInsufficientPrivilege),
		})
		c.Abort()
	}
}

func WssAuth(c *gin.Context) {

}

// TokenOrUserAuth allows either session-based user auth or API token auth.
// Used for endpoints that need to be accessible from both the dashboard and API clients.
func TokenOrUserAuth() func(c *gin.Context) {
	return func(c *gin.Context) {
		raw, ok := authorizationToken(c.GetHeader("Authorization"))
		if ok {
			identity, internal, err := service.ParseDashboardAccessToken(raw)
			if !internal {
				TokenAuth()(c)
				return
			}
			if err != nil {
				writeDashboardAuthError(c, err)
				return
			}
			session, user, err := service.ValidateLoginSession(identity)
			if err != nil {
				writeDashboardAuthError(c, err)
				return
			}
			if err := service.VerifyBrokerSource(c.Request.Context(), identity); err != nil {
				writeDashboardAuthError(c, err)
				return
			}
			if session.AuthRealm == model.CentralAuthRealm {
				if session.LoginMethod == "unihub" {
					if _, _, err := service.ResolveUniHubSession(c.Request.Context(), identity); err != nil {
						writeDashboardAuthError(c, service.ErrBrokerSourceUnavailable)
						return
					}
				} else if session.LoginMethod != "product_broker" && session.LoginMethod != "central_credential" {
					writeDashboardAuthError(c, service.ErrLoginSessionRevoked)
					return
				}
				c.Set(centralRealmContextKey, true)
			}
			native, err := authenticateNativeSession(c, identity)
			if err != nil {
				writeDashboardAuthError(c, err)
				return
			}
			if err := validateProjectionCredential(user); !native && err != nil {
				writeDashboardAuthError(c, err)
				return
			}
			if platformaccount.RuntimeEnabled() && !NativeRealmAuthenticated(c) {
				version := user.AuthVersion
				if c.GetBool(centralRealmContextKey) {
					version = identity.UserAuthVersion
				}
				if !LoadRuntimeAuthority(c, user.Id, version) {
					return
				}
				p, _ := authz.ProductPrincipal(c)
				user.Role = RoleFromAccess(p.Access)
			} else if user.IdentitySource == model.UniHubIdentitySource && !NativeRealmAuthenticated(c) {
				if !loadUniHubAuthority(c, identity) {
					return
				}
				p, _ := authz.ProductPrincipal(c)
				user.Role = RoleFromAccess(p.Access)
			}
			setDashboardAuthContext(c, user, identity, false)
			c.Next()
			return
		}
		// Opaque credentials are relay API keys here, never dashboard PATs.
		TokenAuth()(c)
	}
}

// TokenAuthReadOnly 宽松版本的令牌认证中间件，用于只读查询接口。
// 只验证令牌 key 是否存在，不检查令牌状态、过期时间和额度。
// 即使令牌已过期、已耗尽或已禁用，也允许访问。
// 仍然检查用户是否被封禁。
func TokenAuthReadOnly() func(c *gin.Context) {
	return func(c *gin.Context) {
		key := c.Request.Header.Get("Authorization")
		if key == "" {
			c.JSON(http.StatusUnauthorized, gin.H{
				"success": false,
				"message": common.TranslateMessage(c, i18n.MsgTokenNotProvided),
			})
			c.Abort()
			return
		}
		if strings.HasPrefix(key, "Bearer ") || strings.HasPrefix(key, "bearer ") {
			key = strings.TrimSpace(key[7:])
		}
		key = strings.TrimPrefix(key, "sk-")
		parts := strings.Split(key, "-")
		key = parts[0]

		token, err := model.GetTokenByKey(key, true, true)
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				c.JSON(http.StatusUnauthorized, gin.H{
					"success": false,
					"message": common.TranslateMessage(c, i18n.MsgTokenInvalid),
				})
			} else {
				common.SysLog("TokenAuthReadOnly GetTokenByKey database error: " + err.Error())
				c.JSON(http.StatusInternalServerError, gin.H{
					"success": false,
					"message": common.TranslateMessage(c, i18n.MsgDatabaseError),
				})
			}
			c.Abort()
			return
		}

		// TokenAuthReadOnly must keep allowing other token states to query read-only
		// data, such as token usage logs; only explicitly disabled tokens are denied.
		if token.Status == common.TokenStatusDisabled {
			c.JSON(http.StatusUnauthorized, gin.H{
				"success": false,
				"message": common.TranslateMessage(c, i18n.MsgTokenStatusUnavailable),
			})
			c.Abort()
			return
		}

		userCache, err := model.GetUserCache(token.UserId)
		if err != nil {
			common.SysLog(fmt.Sprintf("TokenAuthReadOnly GetUserCache error for user %d: %v", token.UserId, err))
			c.JSON(http.StatusInternalServerError, gin.H{
				"success": false,
				"message": common.TranslateMessage(c, i18n.MsgDatabaseError),
			})
			c.Abort()
			return
		}
		native, nativeErr := model.NativeRealmTokenReadOnly(c.Request.Context(), token)
		if nativeErr != nil {
			writeDashboardAuthError(c, nativeErr)
			return
		}
		if native {
			c.Set(nativeRealmContextKey, true)
		}
		if err := validateProjectionCredential(userCache); !native && err != nil {
			writeDashboardAuthError(c, err)
			return
		}
		if !native && (platformaccount.RuntimeEnabled() || userCache.IdentitySource == model.UniHubIdentitySource) {
			if !LoadRuntimeAuthority(c, userCache.Id, userCache.AuthVersion) {
				return
			}
		}
		if userCache.Status != common.UserStatusEnabled {
			c.JSON(http.StatusForbidden, gin.H{
				"success": false,
				"message": common.TranslateMessage(c, i18n.MsgAuthUserBanned),
			})
			c.Abort()
			return
		}

		c.Set("id", token.UserId)
		c.Set("token_id", token.Id)
		c.Set("token_key", token.Key)
		c.Next()
	}
}

func TokenAuth() func(c *gin.Context) { return tokenAuth(false) }

// TokenAuthOwnedJobRead keeps the full relay identity, origin, IP, user and group
// checks, but permits a quota-exhausted token to retrieve its already paid job.
// Only GET may use this policy; expired, disabled and deleted tokens still fail.
func TokenAuthOwnedJobRead() func(c *gin.Context) { return tokenAuth(true) }

func tokenAuth(ownedJobRead bool) func(c *gin.Context) {
	return func(c *gin.Context) {
		if ownedJobRead && c.Request.Method != http.MethodGet {
			abortWithOpenAiMessage(c, http.StatusMethodNotAllowed, "owned job authentication is read-only")
			return
		}
		// 先检测是否为ws
		if c.Request.Header.Get("Sec-WebSocket-Protocol") != "" {
			// Sec-WebSocket-Protocol: realtime, openai-insecure-api-key.sk-xxx, openai-beta.realtime-v1
			// read sk from Sec-WebSocket-Protocol
			key := c.Request.Header.Get("Sec-WebSocket-Protocol")
			parts := strings.Split(key, ",")
			for _, part := range parts {
				part = strings.TrimSpace(part)
				if strings.HasPrefix(part, "openai-insecure-api-key") {
					key = strings.TrimPrefix(part, "openai-insecure-api-key.")
					break
				}
			}
			c.Request.Header.Set("Authorization", "Bearer "+key)
		}
		// 检查path包含/v1/messages 或 /v1/models
		if strings.Contains(c.Request.URL.Path, "/v1/messages") || strings.Contains(c.Request.URL.Path, "/v1/models") {
			anthropicKey := c.Request.Header.Get("x-api-key")
			if anthropicKey != "" {
				c.Request.Header.Set("Authorization", "Bearer "+anthropicKey)
			}
		}
		// gemini api 从query中获取key
		if strings.HasPrefix(c.Request.URL.Path, "/v1beta/models") ||
			strings.HasPrefix(c.Request.URL.Path, "/v1beta/openai/models") ||
			strings.HasPrefix(c.Request.URL.Path, "/v1/models/") {
			skKey := c.Query("key")
			if skKey != "" {
				c.Request.Header.Set("Authorization", "Bearer "+skKey)
			}
			// 从x-goog-api-key header中获取key
			xGoogKey := c.Request.Header.Get("x-goog-api-key")
			if xGoogKey != "" {
				c.Request.Header.Set("Authorization", "Bearer "+xGoogKey)
			}
		}
		key := c.Request.Header.Get("Authorization")
		parts := make([]string, 0)
		if strings.HasPrefix(key, "Bearer ") || strings.HasPrefix(key, "bearer ") {
			key = strings.TrimSpace(key[7:])
		}
		if key == "" || key == "midjourney-proxy" {
			key = c.Request.Header.Get("mj-api-secret")
			if strings.HasPrefix(key, "Bearer ") || strings.HasPrefix(key, "bearer ") {
				key = strings.TrimSpace(key[7:])
			}
			key = strings.TrimPrefix(key, "sk-")
			parts = strings.Split(key, "-")
			key = parts[0]
		} else {
			key = strings.TrimPrefix(key, "sk-")
			parts = strings.Split(key, "-")
			key = parts[0]
		}
		var token *model.Token
		var err error
		if ownedJobRead {
			// Read current SQL identity instead of a possibly stale credential cache.
			token, err = model.GetTokenByKey(key, true, true)
			if err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					err = model.ErrTokenInvalid
				} else {
					err = fmt.Errorf("%w: %v", model.ErrDatabase, err)
				}
			} else if (token.Status != common.TokenStatusEnabled && token.Status != common.TokenStatusExhausted) || (token.ExpiredTime != -1 && token.ExpiredTime < common.GetTimestamp()) {
				err = model.ErrTokenInvalid
			}
		} else {
			token, err = model.ValidateUserToken(key)
		}
		if token != nil {
			id := c.GetInt("id")
			if id == 0 {
				c.Set("id", token.UserId)
			}
		}
		if err != nil {
			if errors.Is(err, model.ErrDatabase) {
				common.SysLog("TokenAuth ValidateUserToken database error: " + err.Error())
				abortWithOpenAiMessage(c, http.StatusInternalServerError,
					common.TranslateMessage(c, i18n.MsgDatabaseError))
			} else {
				abortWithOpenAiMessage(c, http.StatusUnauthorized,
					common.TranslateMessage(c, i18n.MsgTokenInvalid))
			}
			return
		}
		if token.Purpose == "chat_embed" && !chatCredentialOriginMatches(token.BoundOrigin, c.GetHeader("Origin")) {
			abortWithOpenAiMessage(c, http.StatusForbidden, "chat credential origin mismatch", types.ErrorCodeAccessDenied)
			return
		}

		native, nativeErr := model.NativeRealmToken(c.Request.Context(), token, ownedJobRead)
		if nativeErr != nil {
			abortWithOpenAiMessage(c, http.StatusUnauthorized, "native credential unavailable")
			return
		}
		allowIps := token.GetIpLimits()
		if len(allowIps) > 0 {
			clientIp := c.ClientIP()
			logger.LogDebug(c, "Token has IP restrictions, checking client IP %s", clientIp)
			ip := net.ParseIP(clientIp)
			if ip == nil {
				abortWithOpenAiMessage(c, http.StatusForbidden, "无法解析客户端 IP 地址")
				return
			}
			if common.IsIpInCIDRList(ip, allowIps) == false {
				abortWithOpenAiMessage(c, http.StatusForbidden, "您的 IP 不在令牌允许访问的列表中", types.ErrorCodeAccessDenied)
				return
			}
			logger.LogDebug(c, "Client IP %s passed the token IP restrictions check", clientIp)
		}

		userCache, err := model.GetUserCache(token.UserId)
		if err != nil {
			common.SysLog(fmt.Sprintf("TokenAuth GetUserCache error for user %d: %v", token.UserId, err))
			abortWithOpenAiMessage(c, http.StatusInternalServerError,
				common.TranslateMessage(c, i18n.MsgDatabaseError))
			return
		}
		if err := validateProjectionCredential(userCache); !native && err != nil {
			writeDashboardAuthError(c, err)
			return
		}
		if !native {
			var owner model.User
			if err := model.DB.WithContext(c.Request.Context()).First(&owner, token.UserId).Error; err != nil {
				writeDashboardAuthError(c, err)
				return
			}
			if owner.FundingRealm == model.NativeFundingLocalOnly {
				abortWithOpenAiMessage(c, http.StatusForbidden, "native funding requires native credential")
				return
			}
		}
		if native {
			c.Set(nativeRealmContextKey, true)
			common.SetContextKey(c, constant.ContextKeyNativeRealm, true)
		} else if nativeLocalLegacyToken(c, userCache.Id) {
			common.SetContextKey(c, constant.ContextKeyNativeLocalLegacy, true)
		} else if platformaccount.RuntimeEnabled() || userCache.IdentitySource == model.UniHubIdentitySource {
			if !LoadRuntimeAuthority(c, userCache.Id, userCache.AuthVersion) {
				return
			}
		}
		userEnabled := userCache.Status == common.UserStatusEnabled
		if !userEnabled {
			abortWithOpenAiMessage(c, http.StatusForbidden, common.TranslateMessage(c, i18n.MsgAuthUserBanned))
			return
		}

		userCache.WriteContext(c)

		userGroup := userCache.Group
		tokenGroup := token.Group
		if tokenGroup != "" {
			// check common.UserUsableGroups[userGroup]
			if _, ok := service.GetUserUsableGroups(userGroup)[tokenGroup]; !ok {
				abortWithOpenAiMessage(c, http.StatusForbidden, fmt.Sprintf("无权访问 %s 分组", tokenGroup))
				return
			}
			// check group in common.GroupRatio
			if !ratio_setting.ContainsGroupRatio(tokenGroup) {
				if tokenGroup != "auto" {
					abortWithOpenAiMessage(c, http.StatusForbidden, fmt.Sprintf("分组 %s 已被弃用", tokenGroup))
					return
				}
			}
			userGroup = tokenGroup
		}
		common.SetContextKey(c, constant.ContextKeyUsingGroup, userGroup)

		err = SetupContextForToken(c, token, parts...)
		if err != nil {
			return
		}
		c.Next()
	}
}

func SetupContextForToken(c *gin.Context, token *model.Token, parts ...string) error {
	if token == nil {
		return fmt.Errorf("token is nil")
	}
	c.Set("id", token.UserId)
	c.Set("token_id", token.Id)
	c.Set("token_key", token.Key)
	c.Set("token_name", token.Name)
	c.Set("token_unlimited_quota", token.UnlimitedQuota)
	if !token.UnlimitedQuota {
		c.Set("token_quota", token.RemainQuota)
	}
	if token.ModelLimitsEnabled {
		c.Set("token_model_limit_enabled", true)
		c.Set("token_model_limit", token.GetModelLimitsMap())
	} else {
		c.Set("token_model_limit_enabled", false)
	}
	common.SetContextKey(c, constant.ContextKeyTokenGroup, token.Group)
	common.SetContextKey(c, constant.ContextKeyTokenCrossGroupRetry, token.CrossGroupRetry)
	if len(parts) > 1 {
		allowed := false
		if p, managed := authz.ProductPrincipal(c); managed {
			allowed = RoleFromAccess(p.Access) >= common.RoleAdminUser && p.HasPermission("channel:operate")
		} else if !platformaccount.RuntimeEnabled() || NativeRealmAuthenticated(c) {
			allowed = model.IsAdmin(token.UserId)
		}
		if allowed {
			c.Set("specific_channel_id", parts[1])
		} else {
			c.Header("specific_channel_version", "701e3ae1dc3f7975556d354e0675168d004891c8")
			abortWithOpenAiMessage(c, http.StatusForbidden, "普通用户不支持指定渠道")
			return fmt.Errorf("普通用户不支持指定渠道")
		}
	}
	return nil
}
