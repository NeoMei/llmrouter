package controller

import (
	"errors"
	userkit "github.com/NeoMei/UniHub/packages/userkit-go"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/internal/platformaccount"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service/authz"
	"github.com/gin-gonic/gin"
	"net/http"
	"strconv"
	"strings"
)

func productIdentityError(c *gin.Context, e error) {
	status := http.StatusServiceUnavailable
	// Runtime 401 may describe the service credential rather than the user.
	// Keep sessions retryable until the authority provides unambiguous evidence.
	var failure *userkit.RuntimeError
	if errors.As(e, &failure) && (failure.Status == 403 || failure.Status == 404 || failure.Status == 409) {
		status = failure.Status
	}
	c.AbortWithStatusJSON(status, gin.H{"success": false, "code": "PRODUCT_IDENTITY_UNAVAILABLE", "message": "Product identity authority is unavailable"})
}

func completePendingProductIdentity(c *gin.Context, u *model.User) error {
	if platformaccount.RuntimeEnabled() && u.ProductJITPending {
		if e := model.CompleteProductJITOnboarding(c.Request.Context(), u.Id); e != nil {
			return e
		}
		refreshed, e := model.GetUserById(u.Id, false)
		if e == nil {
			*u = *refreshed
		}
		return e
	}
	if !platformaccount.RuntimeEnabled() || !u.ProductRegistrationPending {
		return nil
	}
	if u.IdentitySource == model.ProductIdentityPending {
		client, e := platformaccount.RuntimeClient()
		if e != nil {
			return e
		}
		out, e := client.RegisterProductIdentity(c.Request.Context(), userkit.ProductIdentityRegistration{LocalUserID: strconv.Itoa(u.Id), OperationID: "register:" + strconv.Itoa(u.Id), Username: u.Username, DisplayName: u.DisplayName, Email: u.Email, EmailVerified: common.EmailVerificationEnabled, AuthVersion: u.AuthVersion})
		if e != nil {
			return e
		}
		bound, e := model.BindUniHubIdentity(platformaccount.Identity{Issuer: out.Access.Issuer, User: platformaccount.User{ID: out.Access.Subject, Name: out.Access.User.Name, Email: out.Access.User.Email}}, u.Id)
		if e != nil {
			return e
		}
		*u = *bound
	}
	if e := model.ActivateRegisteredProductFunding(c.Request.Context(), u); e != nil {
		return e
	}
	if e := model.DB.WithContext(c.Request.Context()).Model(&model.User{}).Where("id = ? AND product_registration_pending = ?", u.Id, true).Update("product_registration_pending", false).Error; e != nil {
		return e
	}
	u.ProductRegistrationPending = false
	return nil
}

func applyCurrentProductIdentity(c *gin.Context, u *model.User, authenticate bool, independentVersion ...int64) error {
	if !platformaccount.RuntimeEnabled() {
		return nil
	}
	if e := completePendingProductIdentity(c, u); e != nil {
		return e
	}
	if e := model.SyncProductAuthVersions(c.Request.Context(), u.Id); e != nil {
		return e
	}
	var current userkit.ProductIdentity
	var e error
	if authenticate {
		current, e = platformaccount.AuthenticateLocal(c.Request.Context(), strconv.Itoa(u.Id), u.AuthVersion, loginMethodFromContext(c))
	} else {
		current, e = platformaccount.IdentityForLocal(c.Request.Context(), strconv.Itoa(u.Id))
	}
	if e != nil {
		return e
	}
	expectedVersion := u.AuthVersion
	if len(independentVersion) > 0 {
		expectedVersion = independentVersion[0]
	}
	if current.AuthVersion != expectedVersion {
		return userkit.ErrDenied
	}
	if current.Access.Subject == "" || current.Access.User.ID != current.Binding.UserID || current.Access.Subject != current.Binding.UserID {
		return userkit.ErrDenied
	}
	if _, e = model.BindUniHubIdentity(platformaccount.Identity{Issuer: current.Access.Issuer, User: platformaccount.User{ID: current.Access.Subject}}, u.Id); e != nil {
		return e
	}
	principal := userkit.Principal{LocalUser: userkit.LocalUser{ID: strconv.Itoa(u.Id)}, Access: current.Access}
	if !principal.HasPermission("product:use") {
		return userkit.ErrDenied
	}
	authz.SetProductPrincipal(c, principal)
	u.Role = middleware.RoleFromAccess(current.Access)
	u.Status = common.UserStatusEnabled
	c.Set("product_actor_token", current.ActorToken)
	return nil
}

func productSelfData(c *gin.Context, u *model.User, nativeScope ...bool) map[string]interface{} {
	native := middleware.NativeRealmAuthenticated(c) || len(nativeScope) > 0 && nativeScope[0]
	data := buildRealmSelfUserData(u, native)
	// Realm is derived from authenticated session state, never the shared user
	// projection. PATs and other credentials do not carry a browser realm.
	realm := "UNKNOWN"
	if middleware.CentralRealmAuthenticated(c) {
		realm = model.CentralAuthRealm
	} else if native {
		realm = model.NativeFundingLocalOnly
	} else if identity, ok := middleware.GetSessionAuthIdentity(c); ok && identity.UserID == u.Id {
		realm = "LEGACY"
	}
	data["authentication_realm"] = realm
	if p, ok := authz.ProductPrincipal(c); ok {
		if u.FundingRealm == model.NativeFundingLocalOnly && !native {
			name := strings.TrimSpace(p.Access.User.Name)
			if name == "" {
				name = p.Access.Subject
			}
			data["username"] = name
			data["display_name"] = p.Access.User.Name
			data["email"] = p.Access.User.Email
			if p.Access.Active {
				data["status"] = common.UserStatusEnabled
			} else {
				data["status"] = common.UserStatusDisabled
			}
		}
		data["role"] = middleware.RoleFromAccess(p.Access)
		permissions := calculateUserPermissions(middleware.RoleFromAccess(p.Access))
		permissions["admin_permissions"] = authz.RequestCapabilities(c)
		data["permissions"] = permissions
	}
	return data
}

func mutateProductIdentity(c *gin.Context, target int, in userkit.ProductIdentityMutation) (userkit.ProductIdentity, error) {
	actor, e := model.GetUserById(c.GetInt("id"), false)
	if e != nil {
		return userkit.ProductIdentity{}, e
	}
	if e = model.SyncProductAuthVersions(c.Request.Context(), actor.Id); e != nil {
		return userkit.ProductIdentity{}, e
	}
	version := actor.AuthVersion
	if middleware.CentralRealmAuthenticated(c) {
		identity, ok := middleware.GetSessionAuthIdentity(c)
		if !ok || identity.UserID != actor.Id {
			return userkit.ProductIdentity{}, userkit.ErrDenied
		}
		version = identity.UserAuthVersion
	}
	proof, e := platformaccount.AuthenticateLocal(c.Request.Context(), strconv.Itoa(actor.Id), version, "session")
	if e != nil {
		return userkit.ProductIdentity{}, e
	}
	client, e := platformaccount.RuntimeClient()
	if e != nil {
		return userkit.ProductIdentity{}, e
	}
	if in.ExpectedVersion == 0 {
		snapshot, e := client.ProductIdentity(c.Request.Context(), strconv.Itoa(target))
		if e != nil {
			return userkit.ProductIdentity{}, e
		}
		in.ExpectedVersion = snapshot.Access.Version
	}
	if in.OperationID == "" {
		key, e := common.GenerateRandomCharsKey(32)
		if e != nil {
			return userkit.ProductIdentity{}, e
		}
		in.OperationID = key
	}
	return client.MutateProductIdentity(c.Request.Context(), proof.ActorToken, strconv.Itoa(target), in)
}

func handleCentralUserManagement(c *gin.Context, user *model.User, action string) {
	in := userkit.ProductIdentityMutation{Event: "authorization"}
	switch action {
	case "promote":
		role := common.RoleAdminUser
		in.ProductRole = &role
	case "demote":
		role := common.RoleCommonUser
		in.ProductRole = &role
		in.PermissionOverrides = map[string]map[string]bool{}
	case "enable":
		active := true
		in.Active = &active
		in.Event = "enable"
	case "disable":
		active := false
		in.Active = &active
		in.Event = "disable"
	case "delete":
		in.Event = "delete"
	default:
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"success": false, "message": "Invalid user action"})
		return
	}
	out, e := mutateProductIdentity(c, user.Id, in)
	if e != nil {
		productIdentityError(c, e)
		return
	}
	user.Role = out.ProductRole
	user.Status = common.UserStatusEnabled
	if !out.Access.Active {
		user.Status = common.UserStatusDisabled
	}
	if action == "delete" {
		e = user.Delete()
	} else {
		e = user.Update(false)
	}
	if e != nil {
		productIdentityError(c, e)
		return
	}
	if e = model.SyncProductAuthVersions(c.Request.Context(), user.Id); e != nil {
		productIdentityError(c, e)
		return
	}
	recordManageAuditFor(c, user.Id, "user.manage", map[string]interface{}{"action": action, "id": user.Id})
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "", "data": gin.H{"role": user.Role, "status": user.Status}})
}

func authorizeProductIdentityWrite(c *gin.Context, target int, event string) bool {
	if middleware.NativeRealmAuthenticated(c) {
		_, err := model.NativeRealmUser(c.Request.Context(), target)
		if err != nil {
			productIdentityError(c, err)
			return false
		}
		return true
	}
	if !platformaccount.RuntimeEnabled() {
		return true
	}
	if _, e := mutateProductIdentity(c, target, userkit.ProductIdentityMutation{Event: event}); e != nil {
		productIdentityError(c, e)
		return false
	}
	return true
}

// CurrentProductUserData is the original dashboard DTO with current authority.
func CurrentProductUserData(c *gin.Context, u *model.User) map[string]interface{} {
	return productSelfData(c, u)
}

func legacyProductView(c *gin.Context) bool {
	_, ok := authz.ProductPrincipal(c)
	return ok && !platformaccount.RuntimeEnabled()
}

func refreshProductUserForRead(c *gin.Context, user *model.User) error {
	if e := model.RefreshProductBalance(c.Request.Context(), user); e != nil {
		return e
	}
	return model.ReconcileProductIdentity(c.Request.Context(), user)
}

// refreshManagedTarget reads administrative metadata without requiring target
// product access: disabled users still need enable/reset management.
func refreshManagedTarget(c *gin.Context, u *model.User) bool {
	if middleware.NativeRealmAuthenticated(c) && u.FundingRealm != model.NativeFundingLocalOnly {
		productIdentityError(c, model.ErrNativeCredentialUnverified)
		return false
	}
	if u.FundingRealm == model.NativeFundingLocalOnly {
		if !middleware.NativeRealmAuthenticated(c) {
			productIdentityError(c, model.ErrNativeCredentialUnverified)
			return false
		}
		return true
	}
	if !platformaccount.RuntimeEnabled() {
		return true
	}
	if e := model.SyncProductAuthVersions(c.Request.Context(), u.Id); e != nil {
		productIdentityError(c, e)
		return false
	}
	out, e := platformaccount.IdentityForLocal(c.Request.Context(), strconv.Itoa(u.Id))
	if e == nil {
		e = model.VerifyUniHubIdentityBinding(u.Id, out.Access.Issuer, out.Access.Subject)
	}
	if e == nil && out.AuthVersion != u.AuthVersion {
		e = model.ErrUserAuthVersionConflict
	}
	if e != nil {
		productIdentityError(c, e)
		return false
	}
	u.Role = out.ProductRole
	u.Status = common.UserStatusDisabled
	if out.Access.Active {
		u.Status = common.UserStatusEnabled
	}
	return true
}
