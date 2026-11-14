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

	// The terminal usage reports one billed search query and one billed fetch.
	// Display counters include failed calls and must not affect the bill.
	info.ApplyVendorToolUsage([]byte(`{"type":"response.completed","response":{"usage":{
		"server_tool_use":{"web_search_requests":3,"web_fetch_requests":2,"tool_search":1,"tool_fetch":1,"tool_image_search":0}
	}}}`))
	require.Equal(t, 1, info.ResponsesUsageInfo.BuiltInTools[SearchToolPriceKey].CallCount)
	require.Equal(t, 1, info.ResponsesUsageInfo.BuiltInTools[FetchToolPriceKey].CallCount)
	require.Zero(t, info.ResponsesUsageInfo.BuiltInTools[ImageSearchToolPriceKey].CallCount)

	// An absent usage object is inconclusive and keeps the provisional counts.
	info.ApplyVendorToolUsage([]byte(`{"type":"response.failed","response":{"status":"failed","output":[]}}`))
	require.Equal(t, 1, info.ResponsesUsageInfo.BuiltInTools[SearchToolPriceKey].CallCount)
}
