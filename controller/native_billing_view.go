package controller

import (
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/metering"
	"github.com/gin-gonic/gin"
	"net/http"
	"strconv"
)

// ListOwnNativeBilling exposes immutable tariffs and final quantities, never
// provider evidence identifiers, administrator notes, or other users' calls.
func ListOwnNativeBilling(c *gin.Context) {
	var rows []model.NativeBilling
	if err := model.DB.WithContext(c.Request.Context()).Where("user_id = ?", c.GetInt("id")).Order("created_at DESC,id").Limit(100).Find(&rows).Error; err != nil {
		common.ApiError(c, err)
		return
	}
	type view struct {
		ID           string                 `json:"id"`
		State        string                 `json:"state"`
		Platform     bool                   `json:"platform"`
		Reserved     string                 `json:"reserved"`
		Final        *string                `json:"final,omitempty"`
		Price        *metering.Price        `json:"price,omitempty"`
		Measurements []metering.Measurement `json:"measurements,omitempty"`
	}
	result := make([]view, 0, len(rows))
	for _, row := range rows {
		v := view{ID: row.ID, State: row.State, Platform: row.Platform, Reserved: strconv.Itoa(row.ReservedQuota)}
		var contract model.NativeAdmission
		if common.UnmarshalJsonStr(row.Contract, &contract) == nil {
			v.Price = &contract.Price
		}
		if row.State == "settled" {
			final := strconv.Itoa(row.FinalQuota)
			v.Final = &final
			var usage metering.Usage
			if common.UnmarshalJsonStr(row.Evidence, &usage) == nil {
				v.Measurements = usage.Measurements
			}
		}
		result = append(result, v)
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": result})
}
