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

		quota, result, err := billingexpr.RunExpr(expression, billingexpr.TokenParams{P: 1, CR: 1, C: 1})
		require.NoError(t, err, model)
		assert.Equal(t, "standard", result.MatchedTier, model)
		if model == "deepseek-v4-pro" {
			assert.InDelta(t, (9+0.30+27)/7.3, quota, 1e-9, model)
		} else {
			assert.InDelta(t, (2+0.04+8)/7.3, quota, 1e-9, model)
		}
	}
}

// A single standard tier means the same expression price at every clock,
// weekday and holiday. These literals intentionally pin the selected policy.
func TestDeepSeekBillingHasNoTimeCondition(t *testing.T) {
	for _, model := range []string{"deepseek-flash", "deepseek-v4-flash", "deepseek-v4-flash-vision-exp", "deepseek-v4-pro"} {
		expression, ok := GetBillingExpr(model)
		require.True(t, ok, model)
		assert.NotContains(t, expression, "hour(", model)
		assert.NotContains(t, expression, "weekday(", model)
		assert.Contains(t, expression, `tier("standard"`, model)
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
