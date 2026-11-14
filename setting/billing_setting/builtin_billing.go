package billing_setting

// DeepSeek billing deliberately uses one flat weekday rate. It does not
// inspect weekday, time of day, or Chinese statutory holidays. V1 expression
// coefficients are USD per million tokens. Official CNY list prices are
// converted at the configured USD exchange rate by this factor.
const deepSeekFlashExpr = `tier("standard", p * (2 / 7.3) + cr * (0.04 / 7.3) + c * (8 / 7.3))`

var deepSeekV4ProExpr = `tier("standard", p * (9 / 7.3) + cr * (0.30 / 7.3) + c * (27 / 7.3))`

var builtinBillingExpr = map[string]string{
	"deepseek-flash":               deepSeekFlashExpr,
	"deepseek-v4-flash":            deepSeekFlashExpr,
	"deepseek-v4-flash-vision-exp": deepSeekFlashExpr,
	"deepseek-v4-pro":              deepSeekV4ProExpr,
}
