package platformbilling

import (
	"encoding/json"
	"slices"
	"strconv"
	"strings"
)

func protocolKeys(object map[string]any, allowed string) bool {
	if object == nil {
		return false
	}
	for key := range object {
		if !slices.Contains(strings.Fields(allowed), key) {
			return false
		}
	}
	return true
}

func protocolString(value any) bool {
	s, ok := value.(string)
	return ok && strings.TrimSpace(s) != ""
}

func protocolStrings(object map[string]any, fields string) bool {
	for _, key := range strings.Fields(fields) {
		if value, exists := object[key]; exists {
			if _, ok := value.(string); !ok {
				return false
			}
		}
	}
	return true
}

func protocolBools(object map[string]any, fields string) bool {
	for _, key := range strings.Fields(fields) {
		if value, exists := object[key]; exists {
			if _, ok := value.(bool); !ok {
				return false
			}
		}
	}
	return true
}

func protocolEnum(object map[string]any, field, allowed string) bool {
	value, exists := object[field]
	if !exists {
		return true
	}
	s, ok := value.(string)
	return ok && s != "" && slices.Contains(strings.Fields(allowed), s)
}

func protocolNumber(value any, minimum, maximum float64) bool {
	n, ok := value.(json.Number)
	if !ok {
		return false
	}
	f, err := strconv.ParseFloat(string(n), 64)
	return err == nil && f >= minimum && f <= maximum
}

func protocolFunction(object map[string]any, call bool) bool {
	if call {
		if !protocolKeys(object, "name arguments") || !protocolString(object["name"]) {
			return false
		}
		_, ok := object["arguments"].(string)
		return ok
	}
	if !protocolKeys(object, "name description parameters strict") || !protocolString(object["name"]) || !protocolStrings(object, "description") || !protocolBools(object, "strict") {
		return false
	}
	if parameters, exists := object["parameters"]; exists && parameters != nil {
		if _, ok := parameters.(map[string]any); !ok {
			return false
		}
	}
	return true
}

func protocolCustom(object map[string]any, call bool) bool {
	if call {
		if !protocolKeys(object, "name input") || !protocolString(object["name"]) {
			return false
		}
		_, ok := object["input"].(string)
		return ok
	}
	if !protocolKeys(object, "name description format") || !protocolString(object["name"]) || !protocolStrings(object, "description") {
		return false
	}
	if format, exists := object["format"]; exists {
		f, ok := format.(map[string]any)
		if !ok {
			return false
		}
		switch f["type"] {
		case "text":
			return protocolKeys(f, "type")
		case "grammar":
			return protocolKeys(f, "type syntax definition") && protocolEnum(f, "syntax", "lark regex") && protocolString(f["syntax"]) && protocolString(f["definition"])
		default:
			return false
		}
	}
	return true
}

func protocolJSONSchema(object map[string]any, responses bool) bool {
	allowed := "name description schema strict"
	if responses {
		allowed += " type"
	}
	if !protocolKeys(object, allowed) || !protocolString(object["name"]) || !protocolStrings(object, "description") || !protocolBools(object, "strict") {
		return false
	}
	_, ok := object["schema"].(map[string]any)
	return ok
}

func protocolStreamOptions(value any) bool {
	object, ok := value.(map[string]any)
	return ok && protocolKeys(object, "include_usage include_obfuscation") && protocolBools(object, "include_usage include_obfuscation")
}
