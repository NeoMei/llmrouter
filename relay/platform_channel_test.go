package relay

import (
	"github.com/QuantumNous/new-api/constant"
	"testing"
)

func TestPlatformChatChannelAllowlist(t *testing.T) {
	for _, v := range []int{constant.ChannelTypeOpenAI, constant.ChannelTypeDeepSeek, constant.ChannelTypeMoonshot, constant.ChannelTypeZhipu_v4} {
		if !platformChatChannel(v) {
			t.Fatalf("raw OpenAI chat adapter denied %d", v)
		}
	}
	for _, v := range []int{constant.ChannelTypeAnthropic, constant.ChannelTypeGemini, constant.ChannelTypeAdvancedCustom} {
		if platformChatChannel(v) {
			t.Fatalf("uncontracted adapter allowed %d", v)
		}
	}
}
