package ocr

import (
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
)

// ManagedNativeChannelKey preserves the native account selector for a direct
// PaddleOCR channel containing exactly one enabled credential. Pool metadata
// does not establish cardinality; a second member is never an implicit fallback.
func ManagedNativeChannelKey(channel *model.Channel) (string, bool) {
	if channel == nil || channel.Type != constant.ChannelTypePaddleOCR || channel.Status != common.ChannelStatusEnabled {
		return "", false
	}
	keys := channel.GetKeys()
	if len(keys) != 1 || strings.TrimSpace(keys[0]) == "" {
		return "", false
	}
	if status, exists := channel.ChannelInfo.MultiKeyStatusList[0]; exists && status != common.ChannelStatusEnabled {
		return "", false
	}
	return keys[0], true
}

func platformAdmissionKey(admission AuthorizedAdmission, channel *model.Channel) (string, bool) {
	if admission.NativePricing {
		return ManagedNativeChannelKey(channel)
	}
	// Selected-scope platform tariffs retain their certified single-key policy.
	return channel.Key, !channel.ChannelInfo.IsMultiKey
}
