package controller

import (
	"github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
)

// settleTaskBillingFn is a test seam for RelayTask's terminal settlement. The
// production implementation remains service.SettleBilling.
var settleTaskBillingFn = func(c *gin.Context, info *common.RelayInfo, quota int) error {
	return service.SettleBilling(c, info, quota)
}
