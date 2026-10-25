package controller

import (
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/metering"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"net/http"
)

func ListNativeBilling(c *gin.Context) {
	var rows []model.NativeBilling
	if err := model.DB.WithContext(c.Request.Context()).Order("created_at DESC,id").Limit(100).Find(&rows).Error; err != nil {
		common.ApiError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": rows})
}
func GetNativeBilling(c *gin.Context) {
	var n model.NativeBilling
	if err := model.DB.WithContext(c.Request.Context()).First(&n, "id = ?", c.Param("id")).Error; err != nil {
		common.ApiError(c, err)
		return
	}
	var ops []model.OCROperation
	if !n.Platform {
		if err := model.DB.WithContext(c.Request.Context()).Where("job_id = ?", n.ID).Order("kind,phase").Find(&ops).Error; err != nil {
			common.ApiError(c, err)
			return
		}
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": n, "operations": ops})
}
func ResolveNativeBilling(c *gin.Context) {
	var in struct {
		ActionID string          `json:"action_id"`
		Action   string          `json:"action"`
		Usage    *metering.Usage `json:"usage"`
		Reason   string          `json:"reason"`
	}
	if err := common.DecodeJson(c.Request.Body, &in); err != nil {
		common.ApiError(c, err)
		return
	}
	n, err := service.ResolveNativeBilling(c.Request.Context(), c.Param("id"), in.ActionID, c.GetInt("id"), in.Action, in.Usage, in.Reason)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	recordManageAudit(c, "native_billing.resolve", map[string]interface{}{"call_id": n.ID, "action_id": in.ActionID, "action": in.Action})
	c.JSON(http.StatusOK, gin.H{"success": true, "data": n})
}
