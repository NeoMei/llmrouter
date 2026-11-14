package billing_setting

import (
	"testing"

	"github.com/QuantumNous/new-api/pkg/billingexpr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDeepSeekPricingExpressionsCompileAndBillDimensions(t *testing.T) {
	for _, model := range []string{"deepseek-flash", "deepseek-v4-flash", "deepseek-v4-flash-vision-exp", "deepseek-v4-pro"} {
		expression, ok := GetBillingExpr(model)
		require.True(t, ok, model)
		require.Equal(t, BillingModeTieredExpr, GetBillingMode(model), model)
		require.NoError(t, SmokeTestExpr(expression), model)

		quota, result, err := billingexpr.RunExpr(expression, billingexpr.TokenParams{P: 1_000_000, CR: 1_000_000, C: 1_000_000})
		require.NoError(t, err, model)
		assert.Positive(t, quota, model)
		assert.NotEmpty(t, result.MatchedTier, model)
	}
}

func TestDeepSeekExplicitBillingConfigurationWins(t *testing.T) {
	previousMode, previousExpr := billingSetting.BillingMode["deepseek-flash"], billingSetting.BillingExpr["deepseek-flash"]
	t.Cleanup(func() {
		delete(billingSetting.BillingMode, "deepseek-flash")
		delete(billingSetting.BillingExpr, "deepseek-flash")
		if previousMode != "" {
			billingSetting.BillingMode["deepseek-flash"] = previousMode
		}
		if previousExpr != "" {
			billingSetting.BillingExpr["deepseek-flash"] = previousExpr
		}
	})
	billingSetting.BillingMode["deepseek-flash"] = BillingModeRatio
	expr, ok := GetBillingExpr("deepseek-flash")
	assert.False(t, ok)
	assert.Empty(t, expr)
	assert.Equal(t, BillingModeRatio, GetBillingMode("deepseek-flash"))
}
