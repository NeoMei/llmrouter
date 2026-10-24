package ocr

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/billing_setting"
	"github.com/QuantumNous/new-api/setting/config"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNativePriceMustBeExplicitAndRepresentable(t *testing.T) {
	old := ratio_setting.ModelPrice2JSONString()
	t.Cleanup(func() { require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(old)) })
	require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(`{"fixture-ocr":0.0002,"fixture-free":0,"fixture-small":0.00000001,"fixture-large":1e30}`))
	for _, tc := range []struct {
		model   string
		price   float64
		invalid bool
	}{{"fixture-ocr", 0.0002, false}, {"fixture-free", 0, false}, {"missing-price", 0, true}, {"fixture-small", 0, true}, {"fixture-large", 0, true}} {
		t.Run(tc.model, func(t *testing.T) {
			a, err := ResolveAdmission(AuthorizedAdmission{Model: tc.model, UserGroup: "default", UsingGroup: "default"})
			if tc.invalid {
				require.Error(t, err)
				if tc.model == "fixture-large" {
					var pe *PriceError
					require.ErrorAs(t, err, &pe)
					require.NotNil(t, pe.Clamp)
					_, err = common.Marshal(pe.Audit())
					require.NoError(t, err)
				}
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.price, a.Price)
		})
	}
}
func TestNativePriceDoesNotBypassExpressionOrNegativeRatio(t *testing.T) {
	old := ratio_setting.ModelPrice2JSONString()
	oldGroups := ratio_setting.GroupRatio2JSONString()
	t.Cleanup(func() {
		require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(old))
		require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(oldGroups))
	})
	require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(`{"fixture-ocr":0.0002}`))
	settings := config.GlobalConfig.Get("billing_setting").(*billing_setting.BillingSetting)
	oldModes := settings.BillingMode
	settings.BillingMode = map[string]string{"fixture-ocr": billing_setting.BillingModeTieredExpr}
	t.Cleanup(func() { settings.BillingMode = oldModes })
	_, err := ResolveAdmission(AuthorizedAdmission{Model: "fixture-ocr", UserGroup: "default", UsingGroup: "default"})
	require.Error(t, err)
	settings.BillingMode = map[string]string{}
	require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(`{"default":-1}`))
	_, err = ResolveAdmission(AuthorizedAdmission{Model: "fixture-ocr", UserGroup: "default", UsingGroup: "default"})
	require.Error(t, err)
}
