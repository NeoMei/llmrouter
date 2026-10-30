package controller

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/internal/platformaccount"
	"github.com/QuantumNous/new-api/internal/unihubaccess"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/service/authz"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func RefreshAuth(c *gin.Context) {
	setAuthNoStore(c)
	if strings.TrimSpace(c.GetHeader("X-Auth-Session")) == "" && service.SessionSelectionCleared(c) {
		writeAuthSessionError(c, service.ErrLoginSessionMismatch)
		return
	}
	cookieName, rawRefreshToken, err := service.SelectRefreshCookie(c, strings.TrimSpace(c.GetHeader("X-Auth-Session")))
	if err != nil {
		writeAuthSessionError(c, err)
		return
	}
	bundle, user, err := service.RefreshLoginSession(rawRefreshToken, c.GetHeader("X-Auth-Session"), c.ClientIP(), c.Request.UserAgent())
	if err != nil {
		if errors.Is(err, service.ErrRefreshTokenInvalid) || errors.Is(err, service.ErrLoginSessionRevoked) {
			service.ClearNamedRefreshCookie(c, cookieName)
			service.MarkSessionSelectionCleared(c)
		}
		writeAuthSessionError(c, err)
		return
	}
	identity, err := service.ParseAccessToken(bundle.AccessToken)
	if err != nil {
		writeAuthSessionError(c, err)
		return
	}
	if err = service.VerifyBrokerSource(c.Request.Context(), identity); err != nil {
		if errors.Is(err, service.ErrLoginSessionRevoked) {
			_, _ = model.RevokeUserSession(identity.UserID, identity.SessionID, "broker_source_revoked")
			service.ClearNamedRefreshCookie(c, cookieName)
			service.MarkSessionSelectionCleared(c)
			writeAuthSessionError(c, service.ErrLoginSessionRevoked)
		} else {
			// Rotation already occurred; retain the new HttpOnly cookie so a
			// temporary authority outage does not strand the local session.
			service.WriteRefreshCookie(c, bundle.RefreshToken)
			c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{"success": false, "code": "UNIHUB_SOURCE_UNAVAILABLE"})
		}
		return
	}
	service.WriteRefreshCookie(c, bundle.RefreshToken)
	native, nativeErr := service.IsNativeLoginSession(c.Request.Context(), identity)
	if nativeErr != nil {
		writeAuthSessionError(c, nativeErr)
		return
	}
	if platformaccount.RuntimeEnabled() && !native && !middleware.NativePrivateMaintenanceIdentity(c, user.Id) {
		session, sessionErr := model.GetUserSessionBySID(identity.SessionID)
		if sessionErr != nil {
			writeAuthSessionError(c, sessionErr)
			return
		}
		var identityErr error
		if session.AuthRealm == model.CentralAuthRealm {
			identityErr = applyCurrentProductIdentity(c, user, false, identity.UserAuthVersion)
		} else {
			identityErr = applyCurrentProductIdentity(c, user, false)
		}
		if identityErr != nil {
			// Runtime errors do not prove the local session was revoked. The
			// refresh cookie has already rotated and must remain retryable.
			c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{"success": false, "code": "PRODUCT_IDENTITY_UNAVAILABLE", "message": "Product identity authority is unavailable"})
			return
		}
		if e := model.RefreshProductBalance(c.Request.Context(), user); e != nil {
			c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{"success": false, "code": "PRODUCT_IDENTITY_UNAVAILABLE", "message": "Product identity authority is unavailable"})
			return
		}
	}
	data := productSelfData(c, user, native)
	if !native && !platformaccount.RuntimeEnabled() && user.IdentitySource == model.UniHubIdentitySource {
		identity, e := service.ParseAccessToken(bundle.AccessToken)
		if e != nil {
			writeAuthSessionError(c, e)
			return
		}
		p, _, e := service.ResolveUniHubSession(c.Request.Context(), identity)
		if e != nil {
			if errors.Is(e, model.ErrUniHubSessionBindingAbsentOrExpired) {
				writeAuthSessionError(c, service.ErrLoginSessionRevoked)
			} else {
				writeAuthSessionError(c, service.ErrProjectionUnavailable)
			}
			return
		}
		authz.SetProductPrincipal(c, p)
		data = productSelfData(c, user, native)
	}
	data["authentication_realm"] = bundle.Session.AuthenticationRealm
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "",
		"data": gin.H{
			"access_token":      bundle.AccessToken,
			"token_type":        bundle.TokenType,
			"access_expires_at": bundle.AccessExpiresAt,
			"user":              data,
			"session":           bundle.Session,
		},
	})
}

func SessionOptions(c *gin.Context) {
	setAuthNoStore(c)
	options := make([]service.SessionOption, 0, 2)
	unavailable := false
	for _, name := range []string{service.RefreshCookieName, service.CentralRefreshCookieName} {
		raw, err := c.Cookie(name)
		if err != nil || raw == "" {
			continue
		}
		option, err := service.InspectSessionOption(c.Request.Context(), raw, func(userID int) bool {
			return middleware.NativePrivateMaintenanceIdentity(c, userID)
		})
		if err != nil {
			if !errors.Is(err, service.ErrRefreshTokenInvalid) && !errors.Is(err, service.ErrLoginSessionRevoked) {
				unavailable = true
			}
			continue
		}
		if (name == service.RefreshCookieName && option.Realm != "native" && option.Realm != "legacy") || (name == service.CentralRefreshCookieName && option.Realm != "central") {
			continue
		}
		if option.Realm == "native" || option.Realm == "legacy" {
			policy, err := unihubaccess.FromEnv()
			if err != nil || (!policy.NativePublicEnabled && !policy.IsPrivateManagementRequest(c.Request) && !(option.PublicProjection && (policy.PublicEnabled || platformaccount.RuntimeEnabled()))) {
				continue
			}
		}
		options = append(options, *option)
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": gin.H{"sessions": options, "unavailable": unavailable}})
}

func AuthLogout(c *gin.Context) {
	setAuthNoStore(c)
	expectedSID := strings.TrimSpace(c.GetHeader("X-Auth-Session"))
	selectionSID := expectedSID
	if rawAccessToken, ok := dashboardBearer(c.GetHeader("Authorization")); ok && selectionSID == "" {
		if identity, err := service.ParseAccessToken(rawAccessToken); err == nil {
			selectionSID = identity.SessionID
		}
	}
	cookieName, rawRefreshToken, cookieErr := service.SelectRefreshCookie(c, selectionSID)
	if errors.Is(cookieErr, service.ErrLoginSessionMismatch) {
		writeAuthSessionError(c, cookieErr)
		return
	}
	cookieSID, hasCookieSID := service.RefreshTokenSID(rawRefreshToken)
	if expectedSID != "" && cookieErr == nil && hasCookieSID && cookieSID != expectedSID {
		writeAuthSessionError(c, service.ErrLoginSessionMismatch)
		return
	}
	if expectedSID == "" && selectionSID == "" && service.SessionSelectionCleared(c) {
		c.JSON(http.StatusOK, gin.H{"success": true, "message": ""})
		return
	}

	if rawAccessToken, ok := dashboardBearer(c.GetHeader("Authorization")); ok {
		if identity, err := service.ParseAccessToken(rawAccessToken); err == nil {
			if expectedSID != "" && expectedSID != identity.SessionID {
				writeAuthSessionError(c, service.ErrLoginSessionMismatch)
				return
			}
			if _, err := model.RevokeUserSession(identity.UserID, identity.SessionID, "logout"); err != nil {
				writeAuthSessionError(c, err)
				return
			}
			cookieCleared := false
			if cookieErr == nil && hasCookieSID && cookieSID == identity.SessionID {
				if err := service.RevokeByRefreshToken(rawRefreshToken, identity.SessionID, "logout"); err != nil {
					writeAuthSessionError(c, err)
					return
				}
				service.ClearNamedRefreshCookie(c, cookieName)
				cookieCleared = true
			}
			service.MarkSessionSelectionCleared(c)
			c.JSON(http.StatusOK, gin.H{
				"success": true,
				"message": "",
				"data":    gin.H{"revoked_sid": identity.SessionID, "cookie_cleared": cookieCleared},
			})
			return
		}
	}
	if cookieErr != nil || rawRefreshToken == "" {
		service.MarkSessionSelectionCleared(c)
		c.JSON(http.StatusOK, gin.H{"success": true, "message": ""})
		return
	}
	if err := service.RevokeByRefreshToken(rawRefreshToken, expectedSID, "logout"); err != nil {
		writeAuthSessionError(c, err)
		return
	}
	service.ClearNamedRefreshCookie(c, cookieName)
	service.MarkSessionSelectionCleared(c)
	c.JSON(http.StatusOK, gin.H{"success": true, "message": ""})
}

func sessionRequestRealm(c *gin.Context) string {
	if middleware.CentralRealmAuthenticated(c) {
		return model.CentralAuthRealm
	}
	return tokenRequestRealm(c)
}

func GetLoginSessions(c *gin.Context) {
	identity, ok := requireBrowserSession(c)
	if !ok {
		return
	}
	sessions, err := service.ListLoginSessions(identity.UserID, identity.SessionID, sessionRequestRealm(c))
	if err != nil {
		writeAuthSessionError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "", "data": sessions})
}

func DeleteLoginSession(c *gin.Context) {
	identity, ok := requireBrowserSession(c)
	if !ok {
		return
	}
	sid := strings.TrimSpace(c.Param("sid"))
	if sid == "" {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "code": "AUTH_SESSION_ID_REQUIRED", "message": "session id is required"})
		return
	}
	revoked, err := model.RevokeUserSession(identity.UserID, sid, "user_revoked", sessionRequestRealm(c))
	if err != nil {
		writeAuthSessionError(c, err)
		return
	}
	if !revoked {
		c.JSON(http.StatusNotFound, gin.H{"success": false, "code": "AUTH_SESSION_NOT_FOUND", "message": "session not found"})
		return
	}
	if cookieName, rawRefreshToken, cookieErr := service.SelectRefreshCookie(c, sid); cookieErr == nil {
		cookieSID, ok := service.RefreshTokenSID(rawRefreshToken)
		if ok && cookieSID == sid {
			service.ClearNamedRefreshCookie(c, cookieName)
		}
	}
	if sid == identity.SessionID {
		service.MarkSessionSelectionCleared(c)
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "", "data": gin.H{"revoked_sid": sid, "current": sid == identity.SessionID}})
}

func RevokeOtherLoginSessions(c *gin.Context) {
	identity, ok := requireBrowserSession(c)
	if !ok {
		return
	}
	count, err := model.RevokeOtherUserSessions(identity.UserID, identity.SessionID, "user_revoked_others", sessionRequestRealm(c))
	if err != nil {
		writeAuthSessionError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "", "data": gin.H{"revoked_count": count}})
}

func requireBrowserSession(c *gin.Context) (service.AuthIdentity, bool) {
	identity, ok := middleware.GetSessionAuthIdentity(c)
	if !ok {
		c.JSON(http.StatusForbidden, gin.H{
			"success": false,
			"code":    "AUTH_SESSION_REQUIRED",
			"message": "a dashboard login session is required",
		})
		return service.AuthIdentity{}, false
	}
	return identity, true
}

func writeAuthSessionError(c *gin.Context, err error) {
	status, code := service.AuthSessionErrorCode(err)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		status, code = http.StatusUnauthorized, "AUTH_UNAUTHORIZED"
	}
	if status == http.StatusInternalServerError {
		// The response body only carries the generic AUTH_INTERNAL_ERROR
		// code; without this log the underlying Redis/database/session
		// failure is indistinguishable from the client side.
		logger.LogError(c.Request.Context(), fmt.Sprintf("auth session internal error (%s %s): %v", c.Request.Method, c.Request.URL.Path, err))
	}
	c.JSON(status, gin.H{"success": false, "code": code, "message": http.StatusText(status)})
}

func setAuthNoStore(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
}

func authRotationData(bundle *service.AuthBundle) gin.H {
	return gin.H{
		"access_token":      bundle.AccessToken,
		"token_type":        bundle.TokenType,
		"access_expires_at": bundle.AccessExpiresAt,
		"session":           bundle.Session,
	}
}

func dashboardBearer(header string) (string, bool) {
	parts := strings.Fields(header)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || parts[1] == "" {
		return "", false
	}
	return parts[1], true
}
