package platformbilling

import (
	"math"
	"reflect"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/relay/toolpolicy"
)

// Validate protocol envelopes against original-byte unique JSON. Canonical
// bytes are still made from this map, never a DTO that can omit false or zero.
func strictResponsesRequest(body []byte) (map[string]any, error) {
	var request map[string]any
	if common.UnmarshalUnique(body, &request) != nil || request == nil || validateDTOKeys(request, reflect.TypeOf(dto.OpenAIResponsesRequest{})) != nil {
		return nil, ErrInvalid
	}
	if !protocolStrings(request, "model instructions prompt_cache_key prompt_cache_retention safety_identifier user") || !protocolBools(request, "stream parallel_tool_calls background") {
		return nil, ErrInvalid
	}
	if value := request["store"]; value != nil {
		if _, ok := value.(bool); !ok {
			return nil, ErrInvalid
		}
	}
	if background, _ := request["background"].(bool); background {
		return nil, ErrInvalid
	}
	if previous, present := request["previous_response_id"]; present && previous != nil && !protocolString(previous) {
		return nil, ErrInvalid
	}
	if !protocolEnum(request, "truncation", "auto disabled") {
		return nil, ErrInvalid
	}
	for _, key := range []string{"temperature", "top_p"} {
		if v, present := request[key]; present {
			max := 1.0
			if key == "temperature" {
				max = 2
			}
			if !protocolNumber(v, 0, max) {
				return nil, ErrInvalid
			}
		}
	}
	if value, present := request["max_tool_calls"]; present {
		n, err := confirmedCount(value)
		if err != nil || n > math.MaxInt32 {
			return nil, ErrInvalid
		}
	}
	if value, present := request["top_logprobs"]; present {
		n, err := confirmedCount(value)
		if err != nil || n > 20 {
			return nil, ErrInvalid
		}
	}
	if value, present := request["stream_options"]; present && !protocolStreamOptions(value) {
		return nil, ErrInvalid
	}
	if value, present := request["include"]; present {
		items, ok := value.([]any)
		if !ok {
			return nil, ErrInvalid
		}
		for _, item := range items {
			if item != "reasoning.encrypted_content" && item != "message.output_text.logprobs" && item != "message.input_image.image_url" {
				return nil, ErrInvalid
			}
		}
	}
	if value, present := request["reasoning"]; present {
		reasoning, ok := value.(map[string]any)
		if !ok || !protocolKeys(reasoning, "effort summary mode context") || !protocolStrings(reasoning, "effort summary mode context") || !protocolEnum(reasoning, "effort", "none minimal low medium high xhigh") || !protocolEnum(reasoning, "summary", "auto concise detailed") {
			return nil, ErrInvalid
		}
	}
	if value, present := request["text"]; present {
		text, ok := value.(map[string]any)
		if !ok || !protocolKeys(text, "format verbosity") || !protocolEnum(text, "verbosity", "low medium high") {
			return nil, ErrInvalid
		}
		if value, present := text["format"]; present {
			format, ok := value.(map[string]any)
			if !ok {
				return nil, ErrInvalid
			}
			switch format["type"] {
			case "text", "json_object":
				if !protocolKeys(format, "type") {
					return nil, ErrInvalid
				}
			case "json_schema":
				if !protocolJSONSchema(format, true) {
					return nil, ErrInvalid
				}
			default:
				return nil, ErrInvalid
			}
		}
	}
	if value, present := request["metadata"]; present {
		if _, ok := value.(map[string]any); !ok {
			return nil, ErrInvalid
		}
	}
	if !responsesInput(request["input"]) {
		return nil, ErrInvalid
	}
	if value, present := request["tools"]; present {
		tools, ok := value.([]any)
		if !ok {
			return nil, ErrInvalid
		}
		for _, value := range tools {
			tool, ok := value.(map[string]any)
			if !ok {
				return nil, ErrInvalid
			}
			definition := make(map[string]any, len(tool))
			for key, value := range tool {
				if key != "type" {
					definition[key] = value
				}
			}
			switch tool["type"] {
			case "function":
				if !protocolFunction(definition, false) {
					return nil, ErrInvalid
				}
			case "custom":
				if !protocolCustom(definition, false) {
					return nil, ErrInvalid
				}
			default:
				return nil, ErrInvalid // Hosted tools must have bounded measured terms first.
			}
		}
	}
	if value, present := request["tool_choice"]; present {
		if choice, ok := value.(string); ok {
			if choice != "auto" && choice != "none" && choice != "required" {
				return nil, ErrInvalid
			}
		} else {
			choice, ok := value.(map[string]any)
			if !ok || !protocolKeys(choice, "type name") || !protocolString(choice["name"]) || (choice["type"] != "function" && choice["type"] != "custom") {
				return nil, ErrInvalid
			}
		}
	}
	var decoded dto.OpenAIResponsesRequest
	if common.Unmarshal(body, &decoded) != nil {
		return nil, ErrInvalid
	}
	if _, err := toolpolicy.BuildFromResponses(decoded.Tools, decoded.ToolChoice); err != nil {
		return nil, ErrInvalid
	}
	return request, nil
}

func responsesInput(value any) bool {
	if _, ok := value.(string); ok {
		return true
	}
	items, ok := value.([]any)
	if !ok {
		return false
	}
	for _, value := range items {
		item, ok := value.(map[string]any)
		if !ok {
			return false
		}
		switch item["type"] {
		case nil, "message":
			if item["type"] == nil && len(item) == 1 && protocolString(item["id"]) {
				continue
			}
			if value, present := item["type"]; present && value != "message" {
				return false
			}
			if !protocolKeys(item, "type role content id status phase") || !protocolString(item["role"]) || !protocolEnum(item, "role", "user assistant system developer") || !protocolStrings(item, "id") || !protocolEnum(item, "status", "in_progress completed incomplete") {
				return false
			}
			if phase, present := item["phase"]; present {
				if item["role"] != "assistant" || (phase != nil && !protocolEnum(item, "phase", "commentary final_answer")) {
					return false
				}
			}
			if _, ok := item["content"].(string); !ok && !responsesContent(item["content"], true) {
				return false
			}
		case "reasoning":
			if !protocolKeys(item, "type id summary content encrypted_content status") || !protocolStrings(item, "id") || !protocolEnum(item, "status", "in_progress completed incomplete") {
				return false
			}
			if encrypted, present := item["encrypted_content"]; present && encrypted != nil {
				if _, ok := encrypted.(string); !ok {
					return false
				}
			}
			summary, ok := item["summary"].([]any)
			if !ok {
				return false
			}
			for _, value := range summary {
				part, ok := value.(map[string]any)
				if !ok || !protocolKeys(part, "type text") || part["type"] != "summary_text" {
					return false
				}
				if _, ok := part["text"].(string); !ok {
					return false
				}
			}
			if value, present := item["content"]; present {
				content, ok := value.([]any)
				if !ok {
					return false
				}
				for _, value := range content {
					part, ok := value.(map[string]any)
					if !ok || !protocolKeys(part, "type text") || part["type"] != "reasoning_text" {
						return false
					}
					if _, ok := part["text"].(string); !ok {
						return false
					}
				}
			}
		case "function_call", "custom_tool_call":
			payload := "arguments"
			if item["type"] == "custom_tool_call" {
				payload = "input"
			}
			if !protocolKeys(item, "type id call_id name status "+payload) || !protocolString(item["call_id"]) || !protocolString(item["name"]) || !protocolStrings(item, "id") || !protocolEnum(item, "status", "in_progress completed incomplete") {
				return false
			}
			if _, ok := item[payload].(string); !ok {
				return false
			}
		case "function_call_output", "custom_tool_call_output":
			if !protocolKeys(item, "type id call_id output status") || !protocolString(item["call_id"]) || !protocolStrings(item, "id") || !protocolEnum(item, "status", "in_progress completed incomplete") {
				return false
			}
			if _, ok := item["output"].(string); !ok && !responsesContent(item["output"], false) {
				return false
			}
		case "compaction":
			if !protocolKeys(item, "type id encrypted_content") || !protocolString(item["encrypted_content"]) {
				return false
			}
			if id := item["id"]; id != nil {
				if _, ok := id.(string); !ok {
					return false
				}
			}
		case "item_reference":
			if !protocolKeys(item, "type id") || !protocolString(item["id"]) {
				return false
			}
		default:
			return false
		}
	}
	return true
}

func responsesContent(value any, history bool) bool {
	parts, ok := value.([]any)
	if !ok {
		return false
	}
	for _, value := range parts {
		part, ok := value.(map[string]any)
		if !ok {
			return false
		}
		switch part["type"] {
		case "input_text":
			if !protocolKeys(part, "type text") {
				return false
			}
			if _, ok := part["text"].(string); !ok {
				return false
			}
		case "input_image":
			if !protocolKeys(part, "type image_url file_id detail") || !protocolStrings(part, "image_url file_id") || !protocolEnum(part, "detail", "auto low high original") {
				return false
			}
			if protocolString(part["image_url"]) == protocolString(part["file_id"]) {
				return false
			}
		case "input_file":
			if !protocolKeys(part, "type file_id file_url file_data filename") || !protocolStrings(part, "file_id file_url file_data filename") {
				return false
			}
			sources := 0
			for _, key := range []string{"file_id", "file_url", "file_data"} {
				if protocolString(part[key]) {
					sources++
				}
			}
			if sources != 1 {
				return false
			}
		case "output_text":
			if !history || !protocolKeys(part, "type text annotations logprobs") {
				return false
			}
			if _, ok := part["text"].(string); !ok {
				return false
			}
			for _, key := range []string{"annotations", "logprobs"} {
				if value, present := part[key]; present {
					if _, ok := value.([]any); !ok {
						return false
					}
				}
			}
		case "refusal":
			if !history || !protocolKeys(part, "type refusal") {
				return false
			}
			if _, ok := part["refusal"].(string); !ok {
				return false
			}
		default:
			return false
		}
	}
	return true
}
