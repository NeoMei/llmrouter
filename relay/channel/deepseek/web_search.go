package deepseek

import (
	"github.com/tidwall/gjson"
)

// responsesToolUsage returns DeepSeek's authoritative server-tool billing
// counters from a complete response or a Responses terminal envelope.
//
// usage.server_tool_use.tool_search, tool_fetch, and tool_image_search are the
// billed counts. web_search_requests and web_fetch_requests count display
// result blocks and include failed calls, so they must never be used for
// billing. DeepSeek omits server_tool_use entirely when every counter is zero;
// a present usage object therefore proves the final billed counts are zero and
// intentionally replaces the provisional counts gathered from stream items.
// When usage itself is absent, return nil so an incomplete frame does not erase
// those provisional counts.
func responsesToolUsage(raw []byte) map[string]int {
	usagePath := "usage"
	usage := gjson.GetBytes(raw, usagePath)
	if (!usage.Exists() || usage.Type == gjson.Null) && gjson.GetBytes(raw, "response").Exists() {
		usagePath = "response.usage"
		usage = gjson.GetBytes(raw, usagePath)
	}
	if !usage.Exists() || usage.Type == gjson.Null {
		return nil
	}
	serverToolUsePath := usagePath + ".server_tool_use"
	serverToolUse := gjson.GetBytes(raw, serverToolUsePath)
	if !serverToolUse.Exists() || serverToolUse.Type == gjson.Null {
		return map[string]int{SearchToolPriceKey: 0, FetchToolPriceKey: 0, ImageSearchToolPriceKey: 0}
	}
	return map[string]int{
		SearchToolPriceKey:      nonNegativeInt(serverToolUse.Get("tool_search")),
		FetchToolPriceKey:       nonNegativeInt(serverToolUse.Get("tool_fetch")),
		ImageSearchToolPriceKey: nonNegativeInt(serverToolUse.Get("tool_image_search")),
	}
}

func nonNegativeInt(value gjson.Result) int {
	if !value.Exists() {
		return 0
	}
	count := int(value.Int())
	if count < 0 {
		return 0
	}
	return count
}
