package deepseek

import (
	"testing"

	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/constant"
	"github.com/stretchr/testify/require"
)

func TestVendorToolUsageReplacesProvisionalStreamCounts(t *testing.T) {
	info := &relaycommon.RelayInfo{ResponsesUsageInfo: &relaycommon.ResponsesUsageInfo{BuiltInTools: map[string]*relaycommon.BuildInToolInfo{}}}
	(&Adaptor{}).Init(info)
	info.RelayMode = constant.RelayModeResponses
	(&Adaptor{}).Init(info)
	for range 3 {
		info.CountBillableToolCall(dto.BuildInCallWebSearchCall)
	}
	require.Equal(t, 3, info.ResponsesUsageInfo.BuiltInTools[SearchToolPriceKey].CallCount)

	// The terminal answer has one completed search and one fetch; the failed
	// fetch is free. Counts replace, rather than add to, the streamed count.
	info.ApplyVendorToolUsage([]byte(`{"type":"response.completed","response":{"output":[
		{"type":"web_search_call","status":"completed","action":{"type":"search"}},
		{"type":"web_search_call","status":"completed","action":{"type":"open_page"}},
		{"type":"web_search_call","status":"failed","action":{"type":"open_page"}}
	]}}`))
	require.Equal(t, 1, info.ResponsesUsageInfo.BuiltInTools[SearchToolPriceKey].CallCount)
	require.Equal(t, 1, info.ResponsesUsageInfo.BuiltInTools[FetchToolPriceKey].CallCount)
}
