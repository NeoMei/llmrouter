package platformbilling

import (
	"encoding/json"
	"math"
	"strconv"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/relay/toolpolicy"
)

func validateChatProtocol(request map[string]any) error {
	if value, present := request["metadata"]; present {
		if _, ok := value.(map[string]any); !ok {
			return ErrInvalid
		}
	}
	if value, present := request["stop"]; present && value != nil {
		if _, ok := value.(string); !ok {
			stops, ok := value.([]any)
			if !ok || len(stops) > 4 {
				return ErrInvalid
			}
			for _, stop := range stops {
				if _, ok := stop.(string); !ok {
					return ErrInvalid
				}
			}
		}
	}

	if !protocolStrings(request, "model reasoning_effort prompt_cache_key safety_identifier verbosity") || !protocolBools(request, "stream parallel_tool_calls logprobs store") {
		return ErrInvalid
	}
	if value, present := request["seed"]; present && value != nil {
		number, ok := value.(json.Number)
		if !ok {
			return ErrInvalid
		}
		if _, err := strconv.ParseInt(string(number), 10, 64); err != nil {
			return ErrInvalid
		}
	}
	if value, present := request["logit_bias"]; present && value != nil {
		biases, ok := value.(map[string]any)
		if !ok {
			return ErrInvalid
		}
		for token, value := range biases {
			if !canonicalCount.MatchString(token) {
				return ErrInvalid
			}
			if !protocolNumber(value, -100, 100) {
				return ErrInvalid
			}
		}
	}
	for _, key := range []string{"temperature", "top_p", "frequency_penalty", "presence_penalty"} {
		if value, present := request[key]; present {
			minimum, maximum := -math.MaxFloat64, math.MaxFloat64
			switch key {
			case "temperature":
				minimum, maximum = 0, 2
			case "top_p":
				minimum, maximum = 0, 1
			case "frequency_penalty", "presence_penalty":
				minimum, maximum = -2, 2
			}
			if !protocolNumber(value, minimum, maximum) {
				return ErrInvalid
			}
		}
	}
	for _, key := range []string{"top_k", "top_logprobs"} {
		if value, present := request[key]; present {
			n, err := confirmedCount(value)
			if err != nil || n > math.MaxInt32 || (key == "top_logprobs" && n > 20) {
				return ErrInvalid
			}
		}
	}
	if value, present := request["stream_options"]; present && !protocolStreamOptions(value) {
		return ErrInvalid
	}
	messages, ok := request["messages"].([]any)
	if !ok {
		return ErrInvalid
	}
	for _, value := range messages {
		message, ok := value.(map[string]any)
		if !ok || !protocolString(message["role"]) || !protocolEnum(message, "role", "system developer user assistant tool function") || !protocolStrings(message, "name tool_call_id reasoning_content reasoning") || !protocolBools(message, "prefix") {
			return ErrInvalid
		}
		role := message["role"].(string)
		assistant := role == "assistant"
		if role == "function" && !protocolString(message["name"]) {
			return ErrInvalid
		}
		if message["role"] == "tool" && !protocolString(message["tool_call_id"]) {
			return ErrInvalid
		}
		callsPresent := false
		if value, present := message["tool_calls"]; present {
			calls, ok := value.([]any)
			if !assistant || !ok || len(calls) == 0 {
				return ErrInvalid
			}
			callsPresent = true
			for _, value := range calls {
				call, ok := value.(map[string]any)
				if !ok || !protocolKeys(call, "id type function custom") || !protocolString(call["id"]) {
					return ErrInvalid
				}
				switch call["type"] {
				case "function":
					f, ok := call["function"].(map[string]any)
					if !ok || !protocolFunction(f, true) {
						return ErrInvalid
					}
					if _, present := call["custom"]; present {
						return ErrInvalid
					}
				case "custom":
					f, ok := call["custom"].(map[string]any)
					if !ok || !protocolCustom(f, true) {
						return ErrInvalid
					}
					if _, present := call["function"]; present {
						return ErrInvalid
					}
				default:
					return ErrInvalid
				}
			}
		}
		content, present := message["content"]
		if role == "function" {
			if !present {
				return ErrInvalid
			}
			if content != nil {
				if _, ok := content.(string); !ok {
					return ErrInvalid
				}
			}
			continue
		}
		if !present || content == nil {
			if !assistant || !callsPresent {
				return ErrInvalid
			}
		} else if _, ok := content.(string); !ok && !chatContent(content, role) {
			return ErrInvalid
		}
	}
	if value, present := request["tools"]; present {
		tools, ok := value.([]any)
		if !ok {
			return ErrInvalid
		}
		for _, value := range tools {
			tool, ok := value.(map[string]any)
			if !ok || !protocolKeys(tool, "type function custom") {
				return ErrInvalid
			}
			switch tool["type"] {
			case "function":
				definition, ok := tool["function"].(map[string]any)
				if !ok || !protocolFunction(definition, false) {
					return ErrInvalid
				}
				if _, present := tool["custom"]; present {
					return ErrInvalid
				}
			case "custom":
				definition, ok := tool["custom"].(map[string]any)
				if !ok || !protocolCustom(definition, false) {
					return ErrInvalid
				}
				if _, present := tool["function"]; present {
					return ErrInvalid
				}
			default:
				return ErrInvalid
			}
		}
	}
	if value, present := request["tool_choice"]; present {
		if choice, ok := value.(string); ok {
			if choice != "auto" && choice != "none" && choice != "required" {
				return ErrInvalid
			}
		} else {
			choice, ok := value.(map[string]any)
			if !ok || !protocolKeys(choice, "type function custom") {
				return ErrInvalid
			}
			kind, ok := choice["type"].(string)
			if !ok || (kind != "function" && kind != "custom") {
				return ErrInvalid
			}
			name, ok := choice[kind].(map[string]any)
			if !ok || !protocolKeys(name, "name") || !protocolString(name["name"]) {
				return ErrInvalid
			}
			if len(choice) != 2 {
				return ErrInvalid
			}
		}
	}
	if value, present := request["functions"]; present {
		functions, ok := value.([]any)
		if !ok {
			return ErrInvalid
		}
		for _, value := range functions {
			function, ok := value.(map[string]any)
			if !ok || !protocolFunction(function, false) {
				return ErrInvalid
			}
		}
	}
	if value, present := request["function_call"]; present {
		if choice, ok := value.(string); ok {
			if choice != "auto" && choice != "none" {
				return ErrInvalid
			}
		} else {
			choice, ok := value.(map[string]any)
			if !ok || !protocolKeys(choice, "name") || !protocolString(choice["name"]) {
				return ErrInvalid
			}
		}
	}
	if value, present := request["response_format"]; present {
		format, ok := value.(map[string]any)
		if !ok {
			return ErrInvalid
		}
		switch format["type"] {
		case "text", "json_object":
			if !protocolKeys(format, "type") {
				return ErrInvalid
			}
		case "json_schema":
			schema, ok := format["json_schema"].(map[string]any)
			if !protocolKeys(format, "type json_schema") || !ok || !protocolJSONSchema(schema, false) {
				return ErrInvalid
			}
		default:
			return ErrInvalid
		}
	}
	// Reuse effective tool-choice semantics after strict envelope validation; this
	// rejects an undeclared named tool before reservation, not at supplier time.
	encoded, err := common.Marshal(request)
	if err != nil {
		return ErrInvalid
	}
	var decoded dto.GeneralOpenAIRequest
	if common.Unmarshal(encoded, &decoded) != nil {
		return ErrInvalid
	}
	if _, err := toolpolicy.BuildFromChat(decoded.Tools, decoded.ToolChoice); err != nil {
		return ErrInvalid
	}
	return nil
}

func chatContent(value any, role string) bool {
	parts, ok := value.([]any)
	if !ok || len(parts) == 0 {
		return false
	}
	for _, value := range parts {
		part, ok := value.(map[string]any)
		if !ok {
			return false
		}
		kind, ok := part["type"].(string)
		if role != "user" && kind != "text" && !(role == "assistant" && kind == "refusal") {
			return false
		}
		if !ok || !protocolKeys(part, "type "+kind+" fps cache_control") {
			return false
		}
		if value, present := part["fps"]; present {
			if kind != "video_url" || !protocolNumber(value, math.SmallestNonzeroFloat64, math.MaxFloat64) {
				return false
			}
		}
		if value, present := part["cache_control"]; present {
			cache, ok := value.(map[string]any)
			if !ok || !protocolKeys(cache, "type ttl") || cache["type"] != "ephemeral" || !protocolEnum(cache, "ttl", "5m 1h") {
				return false
			}
		}
		switch kind {
		case "text":
			if _, ok := part["text"].(string); !ok {
				return false
			}
		case "refusal":
			if role != "assistant" || !protocolKeys(part, "type refusal") {
				return false
			}
			if _, ok := part["refusal"].(string); !ok {
				return false
			}
		case "image_url":
			image, ok := part["image_url"].(map[string]any)
			if !ok || !protocolKeys(image, "url detail mime_type") || !protocolString(image["url"]) || !protocolStrings(image, "mime_type") || !protocolEnum(image, "detail", "auto low high original") {
				return false
			}
		case "input_audio":
			audio, ok := part["input_audio"].(map[string]any)
			if !ok || !protocolKeys(audio, "data format") || !protocolString(audio["data"]) || !protocolString(audio["format"]) || !protocolEnum(audio, "format", "wav mp3") {
				return false
			}
		case "file":
			file, ok := part["file"].(map[string]any)
			if !ok || !protocolKeys(file, "file_id filename file_data") || !protocolStrings(file, "file_id filename file_data") {
				return false
			}
			if protocolString(file["file_id"]) == protocolString(file["file_data"]) {
				return false
			}
			if protocolString(file["file_data"]) && !protocolString(file["filename"]) {
				return false
			}
		case "video_url":
			if protocolString(part["video_url"]) {
				continue
			}
			video, ok := part["video_url"].(map[string]any)
			if !ok || !protocolKeys(video, "url fps") || !protocolString(video["url"]) {
				return false
			}
			if value, present := video["fps"]; present && !protocolNumber(value, math.SmallestNonzeroFloat64, math.MaxFloat64) {
				return false
			}
		default:
			return false
		}
	}
	return true
}
