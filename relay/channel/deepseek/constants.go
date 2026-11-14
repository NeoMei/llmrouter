package deepseek

var ModelList = []string{
	"deepseek-flash", "deepseek-flash-none", "deepseek-flash-low", "deepseek-flash-max",
	"deepseek-v4-pro", "deepseek-v4-pro-none", "deepseek-v4-pro-low", "deepseek-v4-pro-max",
}

var ChannelName = "deepseek"

// Built-in tool-price keys for server-side Search and Fetch calls executed by
// DeepSeek on Responses/Anthropic requests.
const (
	SearchToolPriceKey = "deepseek_search"
	FetchToolPriceKey  = "deepseek_fetch"
)
