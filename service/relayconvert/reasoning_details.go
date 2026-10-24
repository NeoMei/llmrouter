package relayconvert

import (
	"encoding/json"
	"net/url"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	sharedclaude "github.com/QuantumNous/new-api/service/relayconvert/internal/shared/claude"
	"github.com/tidwall/gjson"
)

// FilterUnsupportedReasoningDetails confines Anthropic state to a destination
// that supports it. It preserves plaintext reasoning and unknown vendor entries.
// Raw-body passthrough deliberately bypasses normal request conversion.
func FilterUnsupportedReasoningDetails(request *dto.GeneralOpenAIRequest, info *relaycommon.RelayInfo) {
	if request == nil {
		return
	}
	if sharedclaude.AcceptSignedThinking(info) {
		return
	}
	if info != nil && info.ChannelMeta != nil && info.ChannelType == constant.ChannelTypeOpenRouter {
		target, err := url.Parse(info.ChannelBaseUrl)
		if info.ChannelBaseUrl == "" || (err == nil && target.Scheme == "https" && target.Host == "openrouter.ai" && target.User == nil) {
			return
		}
	}
	for i := range request.Messages {
		raw := request.Messages[i].ReasoningDetails
		var entries []json.RawMessage
		if len(raw) == 0 || common.Unmarshal(raw, &entries) != nil {
			continue
		}
		filtered := make([]json.RawMessage, 0, len(entries))
		for _, entry := range entries {
			detailType := gjson.GetBytes(entry, "type").String()
			format := gjson.GetBytes(entry, "format").String()
			if format == sharedclaude.ReasoningDetailFormat || (format == "" && detailType == "reasoning.text" && gjson.GetBytes(entry, "signature").Exists()) {
				continue
			}
			filtered = append(filtered, entry)
		}
		if len(filtered) == len(entries) {
			continue
		}
		request.Messages[i].ReasoningDetails = nil
		if len(filtered) > 0 {
			request.Messages[i].ReasoningDetails, _ = common.Marshal(filtered)
		}
	}
}
