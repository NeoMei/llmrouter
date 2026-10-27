package controller

import (
	"fmt"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/model_setting"
	"math"
	"strconv"
)

// ProductRuntimeOption permits only the product's request-processing settings.
// No prefix matching: adding identity, provider credentials or pricing options to
// the native registry never silently opens them to product administrators.
func ProductRuntimeOption(key string) bool {
	switch key {
	case "RetryTimes", "ChannelDisableThreshold", "AutomaticDisableChannelEnabled", "AutomaticEnableChannelEnabled", "AutomaticDisableKeywords", "AutomaticDisableStatusCodes", "AutomaticRetryStatusCodes",
		"global.pass_through_request_enabled", "global.thinking_model_blacklist", "global.chat_completions_to_responses_policy",
		"general_setting.ping_interval_enabled", "general_setting.ping_interval_seconds":
		return true
	}
	return false
}

func validateProductRuntimeOption(key, value string) error {
	if len(value) > 65536 {
		return fmt.Errorf("runtime option value too large")
	}
	switch key {
	case "RetryTimes", "general_setting.ping_interval_seconds":
		n, err := strconv.Atoi(value)
		limit := 10
		if key == "general_setting.ping_interval_seconds" {
			limit = 3600
		}
		if err != nil || n < 0 || n > limit {
			return fmt.Errorf("invalid runtime integer")
		}
	case "ChannelDisableThreshold":
		n, err := strconv.ParseFloat(value, 64)
		if err != nil || math.IsNaN(n) || math.IsInf(n, 0) || n < 0 || n > 3600 {
			return fmt.Errorf("invalid runtime threshold")
		}
	case "AutomaticDisableChannelEnabled", "AutomaticEnableChannelEnabled", "global.pass_through_request_enabled", "general_setting.ping_interval_enabled":
		if value != "true" && value != "false" {
			return fmt.Errorf("invalid runtime boolean")
		}
	case "global.thinking_model_blacklist":
		var models []string
		if err := common.UnmarshalUnique([]byte(value), &models); err != nil {
			return err
		}
	case "global.chat_completions_to_responses_policy":
		var policy model_setting.ChatCompletionsToResponsesPolicy
		if err := common.UnmarshalUnique([]byte(value), &policy); err != nil {
			return err
		}
	}
	return nil
}
