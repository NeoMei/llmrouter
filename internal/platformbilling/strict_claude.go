package platformbilling

import (
	"encoding/base64"
	"fmt"
	"math"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/relay/toolpolicy"
)

// Shape validation cannot prove a media input token ceiling or freeze a remote
// resource. Admission must wait for prepared media and a certified reservation
// policy; a byte count is not a media token count.
var errNativeMediaReservationUnavailable = fmt.Errorf("%w: native media requires prepared input and certified token reservation", ErrInvalid)

func parseClaudeOperation(config Config, body []byte) (operationRequest, error) {
	var result operationRequest
	if !nativeRequestBounds(config, body) {
		return result, ErrInvalid
	}
	var request map[string]any
	if common.UnmarshalUnique(body, &request) != nil || !protocolKeys(request, "model system messages max_tokens stop_sequences temperature top_p top_k stream tools tool_choice thinking metadata cache_control") || !protocolString(request["model"]) || !protocolBools(request, "stream") {
		return result, ErrInvalid
	}
	result.Model = request["model"].(string)
	if len(result.Model) > 255 {
		return result, ErrInvalid
	}
	output, err := confirmedCount(request["max_tokens"])
	if err != nil || output < 1 || output > config.MaxOutputTokens {
		return result, ErrInvalid
	}
	for _, key := range []string{"temperature", "top_p"} {
		if value, present := request[key]; present && !protocolNumber(value, 0, 1) {
			return result, ErrInvalid
		}
	}
	if value, present := request["top_k"]; present {
		count, err := confirmedCount(value)
		if err != nil || count > math.MaxInt32 {
			return result, ErrInvalid
		}
	}
	if value, present := request["stop_sequences"]; present && !nativeStringList(value) {
		return result, ErrInvalid
	}
	if value, present := request["cache_control"]; present && !claudeCacheControl(value) {
		return result, ErrInvalid
	}
	if value, present := request["metadata"]; present {
		metadata, ok := value.(map[string]any)
		if !ok || !protocolKeys(metadata, "user_id") || !protocolStrings(metadata, "user_id") {
			return result, ErrInvalid
		}
	}
	if value, present := request["thinking"]; present {
		thinking, ok := value.(map[string]any)
		if !ok || !protocolKeys(thinking, "type budget_tokens display") || !protocolEnum(thinking, "display", "summarized omitted") {
			return result, ErrInvalid
		}
		switch thinking["type"] {
		case "enabled":
			budget, err := confirmedCount(thinking["budget_tokens"])
			if err != nil || budget < 1024 || budget >= output {
				return result, ErrInvalid
			}
			// Manual thinking permits only the explicit legacy temperature
			// default and no top-k override.
			// API-version and model-specific stricter rules belong to the
			// authoritative producer check, not a guessed model-name branch.
			if value, present := request["temperature"]; present && !protocolNumber(value, 1, 1) {
				return result, ErrInvalid
			}
			if _, present := request["top_k"]; present {
				return result, ErrInvalid
			}
			if value, present := request["top_p"]; present && !protocolNumber(value, 0.95, 1) {
				return result, ErrInvalid
			}
		case "disabled":
			if len(thinking) != 1 {
				return result, ErrInvalid
			}
		case "adaptive":
			if _, present := thinking["budget_tokens"]; present {
				return result, ErrInvalid
			}
		default:
			return result, ErrInvalid
		}
	}
	hasMedia := false
	if value, present := request["system"]; present {
		valid, media := claudeContent(value, "system")
		if !valid {
			return result, ErrInvalid
		}
		hasMedia = media
	}
	messages, ok := request["messages"].([]any)
	if !ok || len(messages) == 0 {
		return result, ErrInvalid
	}
	for _, value := range messages {
		message, ok := value.(map[string]any)
		if !ok || !protocolKeys(message, "role content") || !protocolString(message["role"]) || !protocolEnum(message, "role", "user assistant") {
			return result, ErrInvalid
		}
		valid, media := claudeContent(message["content"], message["role"].(string))
		if !valid {
			return result, ErrInvalid
		}
		hasMedia = hasMedia || media
	}
	if value, present := request["tools"]; present {
		tools, ok := value.([]any)
		if !ok {
			return result, ErrInvalid
		}
		for _, value := range tools {
			tool, ok := value.(map[string]any)
			if !ok || !protocolKeys(tool, "type name description input_schema cache_control strict") || !protocolString(tool["name"]) || !protocolStrings(tool, "description") || !protocolBools(tool, "strict") || !protocolEnum(tool, "type", "custom") {
				return result, ErrInvalid
			}
			if _, ok := tool["input_schema"].(map[string]any); !ok {
				return result, ErrInvalid
			}
			if cache, present := tool["cache_control"]; present && !claudeCacheControl(cache) {
				return result, ErrInvalid
			}
		}
	}
	if value, present := request["tool_choice"]; present {
		choice, ok := value.(map[string]any)
		if !ok || !protocolKeys(choice, "type name disable_parallel_tool_use") || !protocolBools(choice, "disable_parallel_tool_use") {
			return result, ErrInvalid
		}
		switch choice["type"] {
		case "tool":
			if !protocolString(choice["name"]) {
				return result, ErrInvalid
			}
		case "auto", "any":
			if _, present := choice["name"]; present {
				return result, ErrInvalid
			}
		case "none":
			if len(choice) != 1 {
				return result, ErrInvalid
			}
		default:
			return result, ErrInvalid
		}
	}
	canonical, err := common.Marshal(request)
	if err != nil {
		return result, ErrInvalid
	}
	// Decode only after validating the original wire map. Keep that map's bytes
	// for digesting, because DTO omitempty would erase false and zero fields.
	var decoded dto.ClaudeRequest
	if common.Unmarshal(canonical, &decoded) != nil {
		return result, ErrInvalid
	}
	if _, err := toolpolicy.BuildFromClaude(decoded.Tools, decoded.ToolChoice); err != nil {
		return result, ErrInvalid
	}
	quantity, snapshot, mediaErr := nativeMediaReservation(config, "claude.messages.create", result.Model, request)
	if mediaErr != nil {
		return result, mediaErr
	}
	if !hasMedia && len(body) > config.MaxInputTokens/2 {
		return result, ErrInvalid
	}
	result.Canonical = canonical
	result.Output = output
	result.Quantity = quantity
	result.MediaSnapshot = snapshot
	return result, nil
}

func nativeRequestBounds(config Config, body []byte) bool {
	return config.MaxBodyBytes > 0 && config.MaxBodyBytes <= 64<<20 && int64(len(body)) <= config.MaxBodyBytes && config.MaxInputTokens > 0 && config.MaxInputTokens <= 1<<20 && config.MaxOutputTokens > 0 && config.MaxOutputTokens <= 32768 && utf8.Valid(body)
}

func nativeStringList(value any) bool {
	list, ok := value.([]any)
	if !ok {
		return false
	}
	for _, value := range list {
		if _, ok := value.(string); !ok {
			return false
		}
	}
	return true
}

func claudeCacheControl(value any) bool {
	cache, ok := value.(map[string]any)
	return ok && protocolKeys(cache, "type ttl") && cache["type"] == "ephemeral" && protocolEnum(cache, "ttl", "5m 1h")
}

// claudeContent validates the provider tagged union without interpreting opaque
// tool input or thinking signatures. Tool-result nested blocks cannot recurse
// into more tool results.
func claudeContent(value any, role string) (bool, bool) {
	if _, ok := value.(string); ok {
		return true, false
	}
	blocks, ok := value.([]any)
	if !ok || len(blocks) == 0 {
		return false, false
	}
	hasMedia := false
	for _, value := range blocks {
		block, ok := value.(map[string]any)
		if !ok {
			return false, false
		}
		if cache, present := block["cache_control"]; present && !claudeCacheControl(cache) {
			return false, false
		}
		switch block["type"] {
		case "text":
			if !protocolKeys(block, "type text cache_control") {
				return false, false
			}
			if _, ok := block["text"].(string); !ok {
				return false, false
			}
		case "thinking":
			if role != "assistant" || !protocolKeys(block, "type thinking signature") || !protocolString(block["signature"]) {
				return false, false
			}
			if _, ok := block["thinking"].(string); !ok {
				return false, false
			}
		case "redacted_thinking":
			if role != "assistant" || !protocolKeys(block, "type data") || !protocolString(block["data"]) {
				return false, false
			}
		case "tool_use":
			if role != "assistant" || !protocolKeys(block, "type id name input cache_control") || !protocolString(block["id"]) || !protocolString(block["name"]) {
				return false, false
			}
			if _, ok := block["input"].(map[string]any); !ok {
				return false, false
			}
		case "tool_result":
			if role != "user" || !protocolKeys(block, "type tool_use_id content is_error cache_control") || !protocolString(block["tool_use_id"]) || !protocolBools(block, "is_error") {
				return false, false
			}
			if content, present := block["content"]; present {
				valid, media := claudeContent(content, "result")
				if !valid {
					return false, false
				}
				hasMedia = hasMedia || media
			}
		case "image", "document":
			if role != "user" && role != "result" {
				return false, false
			}
			kind := block["type"].(string)
			allowed := "type source cache_control"
			if kind == "document" {
				allowed += " title context citations"
			}
			if !protocolKeys(block, allowed) || !protocolStrings(block, "title context") {
				return false, false
			}
			if citations, present := block["citations"]; present {
				object, ok := citations.(map[string]any)
				if !ok || !protocolKeys(object, "enabled") || !protocolBools(object, "enabled") {
					return false, false
				}
			}
			if !claudeMediaSource(block["source"], kind) {
				return false, false
			}
			hasMedia = true
		default:
			return false, false
		}
	}
	return true, hasMedia
}

func claudeMediaSource(value any, kind string) bool {
	source, ok := value.(map[string]any)
	if !ok {
		return false
	}
	switch source["type"] {
	case "base64":
		if !protocolKeys(source, "type media_type data") || !protocolString(source["data"]) {
			return false
		}
		if kind == "image" {
			if !protocolEnum(source, "media_type", "image/jpeg image/png image/gif image/webp") || !protocolString(source["media_type"]) {
				return false
			}
		} else if source["media_type"] != "application/pdf" {
			return false
		}
		_, err := base64.StdEncoding.DecodeString(source["data"].(string))
		return err == nil
	case "url":
		return protocolKeys(source, "type url") && nativeMediaURL(source["url"])
	case "text":
		return kind == "document" && protocolKeys(source, "type media_type data") && source["media_type"] == "text/plain" && protocolString(source["data"])
	case "file":
		return kind == "document" && protocolKeys(source, "type file_id") && protocolString(source["file_id"])
	default:
		return false
	}
}

func nativeMediaURL(value any) bool {
	raw, ok := value.(string)
	if !ok || strings.TrimSpace(raw) != raw {
		return false
	}
	parsed, err := url.Parse(raw)
	return err == nil && (parsed.Scheme == "https" || parsed.Scheme == "http") && parsed.Host != "" && parsed.User == nil && parsed.Fragment == ""
}
