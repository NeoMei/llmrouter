package deepseek

import (
	"github.com/QuantumNous/new-api/dto"
	"github.com/tidwall/gjson"
)

// responsesToolUsage replaces provisional stream counts with the completed
// web_search_call items of the final response. open_page is a Fetch call; all
// other actions are Search. Failed calls are free. An absent output array is
// inconclusive and leaves stream counts intact.
func responsesToolUsage(raw []byte) map[string]int {
	output := gjson.GetBytes(raw, "output")
	if !output.Exists() {
		output = gjson.GetBytes(raw, "response.output")
	}
	if !output.IsArray() || len(output.Array()) == 0 {
		return nil
	}
	counts := map[string]int{SearchToolPriceKey: 0, FetchToolPriceKey: 0}
	for _, item := range output.Array() {
		if item.Get("type").String() != dto.BuildInCallWebSearchCall || item.Get("status").String() != "completed" {
			continue
		}
		if item.Get("action.type").String() == "open_page" {
			counts[FetchToolPriceKey]++
		} else {
			counts[SearchToolPriceKey]++
		}
	}
	return counts
}
