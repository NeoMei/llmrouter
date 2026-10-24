package claude

import (
	"encoding/json"
	"net/url"
	"regexp"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
)

const ReasoningDetailFormat = "anthropic-claude-v1"

// ReasoningDetail is only the Anthropic subset we interpret. DTOs retain the raw
// carrier so unknown entries and extension fields survive ordinary Chat relay.
type ReasoningDetail struct {
	Type      string  `json:"type"`
	Text      *string `json:"text,omitempty"`
	Signature string  `json:"signature,omitempty"`
	Data      string  `json:"data,omitempty"`
	Format    string  `json:"format,omitempty"`
	Index     int     `json:"index"`
}

var thinkingSignaturePattern = regexp.MustCompile(`^[A-Za-z0-9+/_=-]{16,}$`)
var syntheticSignaturePattern = regexp.MustCompile(`(?i)^(fc|call|msg|rs|resp|reasoning|item|ws|tool|func|function)[-_]`)

func ValidThinkingSignature(signature string) bool {
	return thinkingSignaturePattern.MatchString(signature) && !syntheticSignaturePattern.MatchString(signature)
}

// Replay signed state only to the Anthropic credential domain that issued it.
// A channel using another Messages-compatible provider does not share that domain.
func AcceptSignedThinking(info *relaycommon.RelayInfo) bool {
	if info == nil || info.ChannelMeta == nil || info.ChannelType != constant.ChannelTypeAnthropic {
		return false
	}
	if info.ChannelBaseUrl == "" {
		return true
	}
	target, err := url.Parse(info.ChannelBaseUrl)
	return err == nil && target.Scheme == "https" && target.Host == "api.anthropic.com" && target.User == nil
}

func SignedThinkingBlocks(raw json.RawMessage) []dto.ClaudeMediaMessage {
	var entries []ReasoningDetail
	if len(raw) == 0 || common.Unmarshal(raw, &entries) != nil || len(entries) == 0 {
		return nil
	}
	blocks := make([]dto.ClaudeMediaMessage, 0, len(entries))
	for _, entry := range entries {
		switch entry.Type {
		case "reasoning.text":
			if entry.Text == nil || !ValidThinkingSignature(entry.Signature) || (entry.Format != "" && entry.Format != ReasoningDetailFormat) {
				return nil
			}
			blocks = append(blocks, dto.ClaudeMediaMessage{Type: "thinking", Thinking: entry.Text, Signature: entry.Signature})
		case "reasoning.encrypted":
			if entry.Format != ReasoningDetailFormat || entry.Data == "" {
				return nil
			}
			blocks = append(blocks, dto.ClaudeMediaMessage{Type: "redacted_thinking", Data: entry.Data})
		default:
			return nil
		}
	}
	return blocks
}

func ReasoningDetails(blocks []dto.ClaudeMediaMessage) json.RawMessage {
	entries := make([]ReasoningDetail, 0, len(blocks))
	for _, block := range blocks {
		entry := ReasoningDetail{Format: ReasoningDetailFormat, Index: len(entries)}
		switch block.Type {
		case "thinking":
			if block.Thinking == nil || !ValidThinkingSignature(block.Signature) {
				continue
			}
			entry.Type = "reasoning.text"
			entry.Text = block.Thinking
			entry.Signature = block.Signature
		case "redacted_thinking":
			if block.Data == "" {
				continue
			}
			entry.Type = "reasoning.encrypted"
			entry.Data = block.Data
		default:
			continue
		}
		entries = append(entries, entry)
	}
	if len(entries) == 0 {
		return nil
	}
	raw, _ := common.Marshal(entries)
	return raw
}
