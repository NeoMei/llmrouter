package compat

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/QuantumNous/new-api/common"
)

// NormalizedTools is the provider-neutral result of preparing Responses tools
// for a downstream request. It keeps the filtered input, the combined tool
// declarations, the set of custom wire names that need response rewriting, and
// a flag indicating whether deferred tool_search is required.
type NormalizedTools struct {
	Input              json.RawMessage
	Tools              json.RawMessage
	CustomWireNames    map[string]struct{}
	RequiresToolSearch bool
}

// NormalizeResponsesTools centralizes Responses tool handling. It accepts the
// raw input array and the raw tools array, promotes additional_tools groups,
// flattens namespace members to "namespace.name", converts custom/freeform
// tools into function declarations, and records deferred tool_search
// requirements. Duplicate wire names return an error.
func NormalizeResponsesTools(input, tools json.RawMessage) (NormalizedTools, error) {
	result := NormalizedTools{CustomWireNames: make(map[string]struct{})}

	promotedInput, filteredInput, err := promoteAdditionalTools(input)
	if err != nil {
		return NormalizedTools{}, err
	}
	result.Input = filteredInput

	existingTools, toolSearch, err := parseExistingTools(tools)
	if err != nil {
		return NormalizedTools{}, err
	}
	result.RequiresToolSearch = toolSearch

	combined := append(existingTools, promotedInput...)
	if err := validateAndCollectCustomNames(combined, result.CustomWireNames); err != nil {
		return NormalizedTools{}, err
	}

	if len(combined) > 0 {
		encoded, err := common.Marshal(combined)
		if err != nil {
			return NormalizedTools{}, fmt.Errorf("encode combined tools: %w", err)
		}
		result.Tools = encoded
	} else {
		result.Tools = json.RawMessage("null")
	}

	return result, nil
}

func promoteAdditionalTools(input json.RawMessage) ([]map[string]any, json.RawMessage, error) {
	if len(input) == 0 || common.GetJsonType(input) != "array" {
		return nil, input, nil
	}
	var items []map[string]any
	if err := common.UnmarshalUnique(input, &items); err != nil {
		return nil, nil, fmt.Errorf("invalid Responses input: %w", err)
	}

	filtered := make([]map[string]any, 0, len(items))
	promoted := make([]map[string]any, 0)
	for _, item := range items {
		if common.Interface2String(item["type"]) != "additional_tools" {
			filtered = append(filtered, item)
			continue
		}
		rawTools, ok := item["tools"].([]any)
		if !ok {
			continue
		}
		for _, raw := range rawTools {
			tool, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			converted, err := normalizeTool(tool)
			if err != nil {
				return nil, nil, err
			}
			promoted = append(promoted, converted...)
		}
	}

	filteredRaw, err := common.Marshal(filtered)
	if err != nil {
		return nil, nil, fmt.Errorf("encode filtered input: %w", err)
	}
	return promoted, filteredRaw, nil
}

func normalizeTool(tool map[string]any) ([]map[string]any, error) {
	toolType := common.Interface2String(tool["type"])
	if toolType == "namespace" {
		return normalizeNamespaceTool(tool)
	}
	if toolType == "tool_search" {
		return []map[string]any{normalizeToolSearch(tool)}, nil
	}
	if toolType == "custom" || toolType == "freeform" {
		name := common.Interface2String(tool["name"])
		if name == "" {
			return nil, errors.New("custom tool missing name")
		}
		return []map[string]any{convertCustomTool(tool)}, nil
	}
	if toolType == "function" {
		return []map[string]any{tool}, nil
	}
	return nil, fmt.Errorf("unsupported Responses tool type %q", toolType)
}

func normalizeNamespaceTool(tool map[string]any) ([]map[string]any, error) {
	namespace := common.Interface2String(tool["name"])
	if namespace == "" {
		return nil, errors.New("namespace tool missing name")
	}
	rawInner, ok := tool["tools"].([]any)
	if !ok {
		return nil, fmt.Errorf("namespace %q has no tools", namespace)
	}
	result := make([]map[string]any, 0, len(rawInner))
	for _, raw := range rawInner {
		innerTool, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		name := common.Interface2String(innerTool["name"])
		if name == "" {
			return nil, errors.New("namespace member missing name")
		}
		flatName := namespace + "." + name
		innerTool["name"] = flatName
		innerTool["_namespace"] = namespace

		innerType := common.Interface2String(innerTool["type"])
		switch innerType {
		case "custom", "freeform":
			innerTool = convertCustomTool(innerTool)
		case "function":
			innerTool["type"] = "function"
		default:
			return nil, fmt.Errorf("unsupported namespace member type %q", innerType)
		}
		result = append(result, innerTool)
	}
	return result, nil
}

func normalizeToolSearch(tool map[string]any) map[string]any {
	name := common.Interface2String(tool["name"])
	if name == "" {
		name = "tool_search"
	}
	return map[string]any{
		"type":        "function",
		"name":        name,
		"description": common.Interface2String(tool["description"]),
		"parameters": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query": map[string]any{"type": "string", "description": "Tool search query"},
			},
			"required":             []string{"query"},
			"additionalProperties": false,
		},
		"_tool_search": true,
	}
}

func convertCustomTool(tool map[string]any) map[string]any {
	parameters, _ := tool["parameters"].(map[string]any)
	if parameters == nil {
		parameters = map[string]any{
			"type": "object",
			"properties": map[string]any{
				"input": map[string]any{
					"type":        "string",
					"description": "Raw input for the client-executed tool.",
				},
			},
			"required":             []string{"input"},
			"additionalProperties": false,
		}
	}
	if format, ok := tool["format"]; ok {
		parameters["_format"] = format
	}
	tool["type"] = "function"
	tool["parameters"] = parameters
	delete(tool, "format")
	return tool
}

func parseExistingTools(tools json.RawMessage) ([]map[string]any, bool, error) {
	if len(tools) == 0 || common.GetJsonType(tools) != "array" {
		return nil, false, nil
	}
	var items []map[string]any
	if err := common.UnmarshalUnique(tools, &items); err != nil {
		return nil, false, fmt.Errorf("invalid Responses tools: %w", err)
	}
	result := make([]map[string]any, 0, len(items))
	hasToolSearch := false
	for _, tool := range items {
		converted, err := normalizeTool(tool)
		if err != nil {
			return nil, false, err
		}
		for _, t := range converted {
			if common.Interface2String(t["_tool_search"]) == "true" {
				hasToolSearch = true
			}
		}
		result = append(result, converted...)
	}
	return result, hasToolSearch, nil
}

func validateAndCollectCustomNames(tools []map[string]any, customNames map[string]struct{}) error {
	seen := make(map[string]struct{}, len(tools))
	for _, tool := range tools {
		name := common.Interface2String(tool["name"])
		if name == "" {
			continue
		}
		if _, exists := seen[name]; exists {
			return fmt.Errorf("duplicate tool wire name %q", name)
		}
		seen[name] = struct{}{}
		if isCustomTool(tool) {
			customNames[name] = struct{}{}
		}
	}
	return nil
}

func isCustomTool(tool map[string]any) bool {
	if common.Interface2String(tool["type"]) != "function" {
		return false
	}
	if _, ok := tool["_format"]; ok {
		return true
	}
	parameters, _ := tool["parameters"].(map[string]any)
	if parameters == nil {
		return false
	}
	properties, _ := parameters["properties"].(map[string]any)
	if properties == nil {
		return false
	}
	_, ok := properties["input"]
	return ok
}

// IsCustomWireName reports whether the flattened wire name corresponds to a
// custom/freeform tool that needs response rewriting.
func IsCustomWireName(customNames map[string]struct{}, name string) bool {
	_, ok := customNames[name]
	return ok
}
