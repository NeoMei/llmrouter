package platformbilling

import (
	"encoding/json"
	"math"
	"reflect"
	"regexp"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
)

// validateDTOKeys checks only protocol-owned structs. Maps, RawMessage and any
// payloads (function parameter schemas, metadata, argument strings) retain their
// own case-sensitive user keys. Duplicate keys are rejected separately at every
// JSON nesting level, including those otherwise opaque payloads.
func validateDTOKeys(value any, typ reflect.Type) error {
	if value == nil {
		return nil
	}
	for typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	if typ == reflect.TypeOf(json.RawMessage{}) || typ.Kind() == reflect.Interface || typ.Kind() == reflect.Map {
		return nil
	}
	switch typ.Kind() {
	case reflect.Slice, reflect.Array:
		if items, ok := value.([]any); ok {
			for _, item := range items {
				if err := validateDTOKeys(item, typ.Elem()); err != nil {
					return err
				}
			}
		}
	case reflect.Struct:
		object, ok := value.(map[string]any)
		if !ok {
			return ErrInvalid
		}
		// The SDK sends explicit false for non-strict function schemas. Preserve
		// that value, but reject malformed types before any funds are reserved.
		if typ == reflect.TypeOf(dto.FunctionRequest{}) {
			if strict, present := object["strict"]; present {
				if _, ok := strict.(bool); !ok {
					return ErrInvalid
				}
			}
		}
		fields := map[string]reflect.Type{}
		for i := 0; i < typ.NumField(); i++ {
			field := typ.Field(i)
			tag := strings.Split(field.Tag.Get("json"), ",")[0]
			if field.PkgPath == "" && tag != "" && tag != "-" {
				fields[tag] = field.Type
			}
		}
		for name, child := range object {
			field, ok := fields[name]
			if !ok {
				return ErrInvalid
			}
			if err := validateDTOKeys(child, field); err != nil {
				return err
			}
		}
	}
	return nil
}

func strictChatRequest(body []byte) (map[string]any, error) {
	var request map[string]any
	if common.UnmarshalUnique(body, &request) != nil || request == nil {
		return nil, ErrInvalid
	}
	if validateDTOKeys(request, reflect.TypeOf(dto.GeneralOpenAIRequest{})) != nil {
		return nil, ErrInvalid
	}
	// These fields contain protocol objects declared as any/RawMessage in the
	// legacy DTO. Validate their protocol keys before its permissive decoder.
	if messages, ok := request["messages"].([]any); ok {
		for _, item := range messages {
			message, ok := item.(map[string]any)
			if !ok {
				return nil, ErrInvalid
			}
			if parts, ok := message["content"].([]any); ok {
				for _, part := range parts {
					if validateDTOKeys(part, reflect.TypeOf(dto.MediaContent{})) != nil {
						return nil, ErrInvalid
					}
				}
			}
			if calls, ok := message["tool_calls"].([]any); ok {
				for _, call := range calls {
					if validateDTOKeys(call, reflect.TypeOf(dto.ToolCallRequest{})) != nil {
						return nil, ErrInvalid
					}
				}
			}
		}
	}
	if choice, ok := request["tool_choice"].(map[string]any); ok {
		if validateDTOKeys(choice, reflect.TypeOf(dto.ToolCallRequest{})) != nil {
			return nil, ErrInvalid
		}
	}
	if format, ok := request["response_format"].(map[string]any); ok {
		if schema, ok := format["json_schema"].(map[string]any); ok {
			if validateDTOKeys(schema, reflect.TypeOf(dto.FormatJsonSchema{})) != nil {
				return nil, ErrInvalid
			}
		}
	}
	return request, nil
}

var canonicalCount = regexp.MustCompile(`^(0|[1-9][0-9]*)$`)

func confirmedCount(value any) (int, error) {
	number, ok := value.(json.Number)
	if !ok || !canonicalCount.MatchString(string(number)) {
		return 0, ErrInvalid
	}
	n, err := strconv.ParseInt(string(number), 10, strconv.IntSize)
	if err != nil {
		return 0, ErrInvalid
	}
	return int(n), nil
}

// ObserveRawEnvelope is the only provider-observation entry. Core counters are
// checked before DTO zero defaults or token estimates can erase their absence.
// Missing usage on ordinary SSE delta frames is normal; an explicit invalid
// usage observation invalidates any earlier provisional usage.
func (s *Session) ObserveRawEnvelope(body []byte, stream bool) {
	var envelope map[string]any
	if common.UnmarshalUnique(body, &envelope) != nil || envelope == nil {
		s.invalidateUsage()
		return
	}
	for key := range envelope {
		if key != "usage" && strings.EqualFold(key, "usage") {
			s.invalidateUsage()
			return
		}
	}
	raw, present := envelope["usage"]
	if !present {
		if !stream {
			s.invalidateUsage()
		}
		return
	}
	s.invalidateUsage()
	usage, ok := raw.(map[string]any)
	if !ok {
		return
	}
	allowed := map[string]bool{"prompt_tokens": true, "completion_tokens": true, "total_tokens": true, "prompt_tokens_details": true, "completion_tokens_details": true}
	for key := range usage {
		if !allowed[key] {
			return
		}
	}
	p, e := confirmedCount(usage["prompt_tokens"])
	if e != nil {
		return
	}
	c, e := confirmedCount(usage["completion_tokens"])
	if e != nil {
		return
	}
	total, e := confirmedCount(usage["total_tokens"])
	if e != nil || p > math.MaxInt-c || total != p+c {
		return
	}
	dtoUsage := dto.Usage{PromptTokens: p, CompletionTokens: c, TotalTokens: total}
	for key, fields := range map[string]map[string]bool{
		"prompt_tokens_details":     {"cached_tokens": true, "cache_write_tokens": true, "cached_creation_tokens": true, "audio_tokens": true, "image_tokens": true, "text_tokens": true},
		"completion_tokens_details": {"reasoning_tokens": true, "audio_tokens": true, "image_tokens": true, "text_tokens": true, "accepted_prediction_tokens": true, "rejected_prediction_tokens": true},
	} {
		detail := usage[key]
		if detail == nil {
			continue
		}
		object, ok := detail.(map[string]any)
		if !ok {
			return
		}
		for name, value := range object {
			if !fields[name] {
				return
			}
			if value == nil {
				continue
			}
			n, err := confirmedCount(value)
			if err != nil {
				return
			}
			switch key + "." + name {
			case "prompt_tokens_details.cached_tokens":
				dtoUsage.PromptTokensDetails.CachedTokens = n
			case "prompt_tokens_details.cache_write_tokens":
				dtoUsage.PromptTokensDetails.CacheWriteTokens = n
			case "prompt_tokens_details.cached_creation_tokens":
				dtoUsage.PromptTokensDetails.CachedCreationTokens = n
			case "prompt_tokens_details.audio_tokens":
				dtoUsage.PromptTokensDetails.AudioTokens = n
			case "prompt_tokens_details.image_tokens":
				dtoUsage.PromptTokensDetails.ImageTokens = n
			case "prompt_tokens_details.text_tokens":
				dtoUsage.PromptTokensDetails.TextTokens = n
			case "completion_tokens_details.reasoning_tokens":
				dtoUsage.CompletionTokenDetails.ReasoningTokens = n
			case "completion_tokens_details.audio_tokens":
				dtoUsage.CompletionTokenDetails.AudioTokens = n
			case "completion_tokens_details.image_tokens":
				dtoUsage.CompletionTokenDetails.ImageTokens = n
			case "completion_tokens_details.text_tokens":
				dtoUsage.CompletionTokenDetails.TextTokens = n
			}
		}
	}
	s.observe(&dtoUsage)
}
