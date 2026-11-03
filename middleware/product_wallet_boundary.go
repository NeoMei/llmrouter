package middleware

import (
	"net/http"

	"github.com/QuantumNous/new-api/internal/platformaccount"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service/authz"
	"github.com/gin-gonic/gin"
)

// Only the individually declared original wallet routes may cross the native
// public boundary. Product authority must also own this funding account.
func ProductWalletBoundary() gin.HandlerFunc {
	return func(c *gin.Context) {
		if _, managed := authz.ProductPrincipal(c); !managed {
			NativePublicBoundary()(c)
			return
		}
		if _, err := model.ProductWalletAccount(c.Request.Context(), c.GetInt("id")); err != nil {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"success": false, "code": "PRODUCT_WALLET_OWNER_REQUIRED"})
			return
		}
		c.Next()
	}
}

// A CNY wallet cannot use a checkout/reward path that still awards LR quota.
// Verified provider callbacks are separate routes and retain their old asset.
func ProductCashWalletWriteBoundary() gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request.Method != http.MethodPost || !platformaccount.UnifiedCashFundingEnabled() {
			c.Next()
			return
		}
		switch c.FullPath() {
		case "/api/user/topup", "/api/user/pay", "/api/user/amount", "/api/user/stripe/pay", "/api/user/stripe/amount", "/api/user/creem/pay", "/api/user/waffo/pay", "/api/user/waffo/amount", "/api/user/waffo-pancake/pay", "/api/user/waffo-pancake/amount", "/api/user/checkin", "/api/user/aff_transfer",
			"/api/subscription/epay/pay", "/api/subscription/stripe/pay", "/api/subscription/creem/pay", "/api/subscription/waffo-pancake/pay":
			state, err := model.ProductFundingState(c.Request.Context(), c.GetInt("id"))
			_, managed := authz.ProductPrincipal(c)
			if err != nil || managed || state != "local" {
				c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"success": false, "code": "CASH_WALLET_OPERATION_UNSUPPORTED"})
				return
			}
		}
		c.Next()
	}
}
