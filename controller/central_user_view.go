package controller

import (
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	userkit "github.com/NeoMei/UniHub/packages/userkit-go"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/internal/platformaccount"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/service/authz"
	"github.com/gin-gonic/gin"
)

// CentralUserView is the authoritative product-directory row. It never exposes
// native profiles, credentials, funds, quotas or route/pricing groups.
type CentralUserView struct {
	ID          string `json:"id"`
	Username    string `json:"username"`
	DisplayName string `json:"display_name"`
	Email       string `json:"email"`
	Role        int    `json:"role"`
	Status      int    `json:"status"`
	Version     int64  `json:"assignment_version"`
}

var centralActorToken = regexp.MustCompile(`^upa_[0-9a-f]{64}$`)

const centralUserDirectoryLimit = 500

var errCentralUserPermission = errors.New("central user read permission required")

func centralUserAuthority(c *gin.Context) (*userkit.RuntimeClient, string, error) {
	if !platformaccount.RuntimeEnabled() {
		return nil, "", errors.New("central directory unavailable")
	}
	p, ok := authz.ProductPrincipal(c)
	if !ok {
		return nil, "", errCentralUserPermission
	}
	if !authz.CanRequest(c, authz.Permission{Resource: "user", Action: "read"}) {
		return nil, "", errCentralUserPermission
	}
	identity, ok := middleware.GetSessionAuthIdentity(c)
	if !ok || p.LocalUser.ID != strconv.Itoa(identity.UserID) {
		return nil, "", errors.New("live session required")
	}
	config, err := platformaccount.Current()
	if err != nil {
		return nil, "", err
	}
	if p.Access.Issuer != config.PublicOrigin || p.Access.ProductID != config.ProductID || p.Access.InstanceID != config.InstanceID {
		return nil, "", errors.New("central authority scope mismatch")
	}
	if err = model.VerifyUniHubIdentityBinding(identity.UserID, p.Access.Issuer, p.Access.Subject); err != nil {
		return nil, "", err
	}
	if _, _, err = service.ValidateLoginSession(identity); err != nil {
		return nil, "", err
	}
	projected, err := model.CheckUniHubProjection(identity.UserID)
	if err != nil || !projected {
		return nil, "", errors.New("projection required")
	}
	current, err := platformaccount.AuthenticateLocal(c.Request.Context(), strconv.Itoa(identity.UserID), identity.UserAuthVersion, "session")
	if err != nil {
		return nil, "", err
	}
	now := time.Now().Unix()
	if current.AuthVersion != identity.UserAuthVersion || current.Binding.UserID != p.Access.Subject || current.Access.Subject != p.Access.Subject || current.Access.User.ID != p.Access.Subject || current.Access.Issuer != config.PublicOrigin || current.Access.ProductID != config.ProductID || current.Access.InstanceID != config.InstanceID || !centralActorToken.MatchString(current.ActorToken) || current.ActorExpiresAt <= now || current.ActorExpiresAt > now+130 {
		return nil, "", errors.New("current actor unavailable")
	}
	session, _, validationErr := service.ValidateLoginSession(identity)
	if validationErr != nil {
		return nil, "", validationErr
	}
	if session.LoginMethod == "product_broker" {
		if err = service.VerifyBrokerSource(c.Request.Context(), identity); err != nil {
			return nil, "", err
		}
	}
	if _, _, err = service.ValidateLoginSession(identity); err != nil {
		return nil, "", err
	}
	client, err := platformaccount.RuntimeClient()
	if err != nil {
		return nil, "", err
	}
	scope := client.Scope()
	if scope.ProductID != config.ProductID || scope.InstanceID != config.InstanceID {
		return nil, "", errors.New("runtime client scope mismatch")
	}
	return client, current.ActorToken, nil
}

func containsRole(roles []string, role string) bool {
	for _, candidate := range roles {
		if candidate == role {
			return true
		}
	}
	return false
}

func centralUserViewRows(managed []userkit.RuntimeManagedUser, config platformaccount.Config) ([]CentralUserView, error) {
	if len(managed) >= centralUserDirectoryLimit {
		return nil, errors.New("directory exceeds supported limit")
	}
	rows := make([]CentralUserView, 0, len(managed))
	for _, entry := range managed {
		assignment := entry.Assignment
		if assignment.ProductID != config.ProductID || assignment.InstanceID != config.InstanceID || entry.User.ID == "" {
			return nil, errors.New("foreign assignment")
		}
		role := common.RoleCommonUser
		if containsRole(assignment.Roles, "product_root") {
			role = common.RoleRootUser
		} else if containsRole(assignment.Roles, "product_admin") || containsRole(assignment.Roles, "product_administrator") || containsRole(assignment.Roles, "channel_operator") || containsRole(assignment.Roles, "channel_viewer") || containsRole(assignment.Roles, "product_viewer") {
			role = common.RoleAdminUser
		}
		name := strings.TrimSpace(entry.User.Name)
		if name == "" {
			name = entry.User.ID
		}
		status := common.UserStatusDisabled
		if assignment.Active {
			status = common.UserStatusEnabled
		}
		rows = append(rows, CentralUserView{ID: entry.User.ID, Username: name, DisplayName: name, Email: entry.User.Email, Role: role, Status: status, Version: assignment.Version})
	}
	return rows, nil
}

func serveCentralUserView(c *gin.Context, keyword string, roleFilter, statusFilter *int) {
	pageInfo := common.GetPageQuery(c)
	client, actor, err := centralUserAuthority(c)
	if err != nil {
		if errors.Is(err, errCentralUserPermission) {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"success": false, "code": "UNIHUB_PERMISSION_DENIED"})
			return
		}
		common.SysError("central user directory authority unavailable: " + err.Error())
		c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{"success": false, "code": "CENTRAL_USER_DIRECTORY_UNAVAILABLE"})
		return
	}
	config, err := platformaccount.Current()
	if err != nil {
		c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{"success": false, "code": "CENTRAL_USER_DIRECTORY_UNAVAILABLE"})
		return
	}
	managed, err := client.Users(c.Request.Context(), actor, keyword)
	if err != nil {
		common.SysError("central user directory read failed: " + err.Error())
		c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{"success": false, "code": "CENTRAL_USER_DIRECTORY_UNAVAILABLE"})
		return
	}
	rows, err := centralUserViewRows(managed, config)
	if err != nil {
		c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{"success": false, "code": "CENTRAL_USER_DIRECTORY_UNAVAILABLE"})
		return
	}
	filtered := make([]CentralUserView, 0, len(rows))
	for _, row := range rows {
		if roleFilter != nil && row.Role != *roleFilter || statusFilter != nil && row.Status != *statusFilter {
			continue
		}
		filtered = append(filtered, row)
	}
	total := len(filtered)
	start, end := pageInfo.GetStartIdx(), 0
	if start > total {
		start = total
	}
	end = start + pageInfo.GetPageSize()
	if end > total {
		end = total
	}
	pageInfo.SetTotal(total)
	pageInfo.SetItems(filtered[start:end])
	common.ApiSuccess(c, pageInfo)
}
