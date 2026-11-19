package dto

import "github.com/tidwall/gjson"

const BillingUsageSourceDeepSeekProductChat = "deepseek_chat_product_v1"

const (
	deepSeekChatCacheHit   = "prompt_cache_hit_tokens"
	deepSeekChatCacheMiss  = "prompt_cache_miss_tokens"
	chatCachedTokens       = "prompt_tokens_details.cached_tokens"
	chatCacheCreationAlias = "prompt_tokens_details.cache_creation_input_tokens"
)

// DeepSeek sends the cumulative usage on the last content frame, with one
// finished choice, before DONE. The stream observer separately requires DONE.
func deepSeekProductChatFinalFrame(root gjson.Result) bool {
	choices := root.Get("choices")
	if !choices.IsArray() {
		return false
	}
	items := choices.Array()
	if len(items) != 1 {
		return false
	}
	reason := items[0].Get("finish_reason")
	return reason.Type == gjson.String && reason.String() != ""
}
