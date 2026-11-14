package billing_setting

// DeepSeek peak window: Monday-Friday, 09:00-12:00 and 14:00-18:00 in
// Asia/Shanghai. Official public holidays are announced too late to encode
// statically, so they bill at peak here.
const deepSeekPeak = `weekday("Asia/Shanghai") >= 1 && weekday("Asia/Shanghai") <= 5 && ((hour("Asia/Shanghai") >= 9 && hour("Asia/Shanghai") < 12) || (hour("Asia/Shanghai") >= 14 && hour("Asia/Shanghai") < 18))`

// Values are USD per token. Peak/off-peak DeepSeek prices differ by a factor
// of two. The aliases still run the V4.1 Flash model at Flash pricing.
const deepSeekFlashExpr = deepSeekPeak + ` ? tier("peak", p * 0.000002 + cr * 0.00000004 + c * 0.000008) : tier("off_peak", p * 0.000001 + cr * 0.00000002 + c * 0.000004)`

var deepSeekV4ProExpr = deepSeekPeak + ` ? tier("peak", p * 0.000009 + cr * 0.0000003 + c * 0.000027) : tier("off_peak", p * 0.0000045 + cr * 0.00000015 + c * 0.0000135)`

var builtinBillingExpr = map[string]string{
	"deepseek-flash":               deepSeekFlashExpr,
	"deepseek-v4-flash":            deepSeekFlashExpr,
	"deepseek-v4-flash-vision-exp": deepSeekFlashExpr,
	"deepseek-v4-pro":              deepSeekV4ProExpr,
}
