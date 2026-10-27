package controller

import (
	"errors"
	userkit "github.com/NeoMei/UniHub/packages/userkit-go"
	"net/http"
	"strconv"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/internal/platformaccount"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
)

// The original administrator page keeps its add/subtract/set semantics. Only
// the backend authority changes; a spending budget is never credited as money.
func handleProductQuotaAdjustment(c *gin.Context, user *model.User, mode string, value int) bool {
	store, managed, err := model.ProductFundingStoreForUser(c.Request.Context(), user.Id)
	if err != nil {
		common.ApiError(c, err)
		return true
	}
	if !managed {
		return false
	}
	if value < 0 || (mode != "override" && value == 0) || (mode != "add" && mode != "subtract" && mode != "override") {
		common.ApiError(c, model.ErrProductFundingConflict)
		return true
	}
	actor, err := model.GetUserById(c.GetInt("id"), true)
	if err != nil {
		common.ApiError(c, err)
		return true
	}
	proof, err := platformaccount.AuthenticateLocal(c.Request.Context(), strconv.Itoa(actor.Id), actor.AuthVersion, "session")
	if err != nil {
		common.ApiError(c, err)
		return true
	}
	key := c.GetHeader("Idempotency-Key")
	if key == "" {
		key = c.GetString(common.RequestIdKey)
	}
	if key == "" {
		key = common.GetUUID()
	}
	result, err := store.Adjust(c.Request.Context(), proof.ActorToken, actor.Id, user.Id, key, mode, value)
	if err != nil {
		var remote *userkit.RuntimeError
		if errors.As(err, &remote) && (remote.Status == 400 || remote.Status == 401 || remote.Status == 403 || (remote.Status == 409 && remote.Code != "operation_pending")) {
			common.ApiError(c, err)
		} else {
			c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{"success": false, "code": "PRODUCT_CREDIT_PENDING", "message": "Credit adjustment is pending confirmation"})
		}
		return true
	}
	if result.Status != "APPLIED" {
		common.ApiError(c, model.ErrProductFundingPending)
		return true
	}
	recordManageAuditFor(c, user.Id, "user.quota_"+mode, map[string]interface{}{"quota": value, "funding_owner": "unihub", "receipt_id": result.ID})
	c.JSON(http.StatusOK, gin.H{"success": true, "message": ""})
	return true
}
