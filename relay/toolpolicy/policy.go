package toolpolicy

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/gin-gonic/gin"
)

type ToolKind string

const (
	ToolKindFunction ToolKind = "function"
	ToolKindCustom   ToolKind = "custom"
)

type ChoiceMode string

const (
	ChoiceAuto     ChoiceMode = "auto"
	ChoiceNone     ChoiceMode = "none"
	ChoiceRequired ChoiceMode = "required"
	ChoiceNamed    ChoiceMode = "named"
)

var (
	ErrInvalidPolicy      = errors.New("invalid tool policy")
	ErrUndeclaredTool     = errors.New("undeclared tool")
	ErrDisallowedTool     = errors.New("disallowed tool")
	ErrMalformedToolCall  = errors.New("malformed tool call")
	ErrMalformedArguments = errors.New("malformed tool arguments")
	ErrToolKindMismatch   = errors.New("tool kind does not match declaration")
)

type Declaration struct {
	Name string
	Kind ToolKind
}

type Call struct {
	ID        string
	Name      string
	Kind      ToolKind
	Arguments json.RawMessage
}

type Policy struct {
	allowed       map[string]Declaration
	choice        ChoiceMode
	forcedName    string
	forcedKind    ToolKind
	choiceAllowed map[string]struct{}
}

type choiceSpec struct {
	mode       ChoiceMode
	forcedName string
	forcedKind ToolKind
	restricted []string
}

func BuildFromChat(tools []dto.ToolCallRequest, toolChoice any) (Policy, error) {
	declarations := make([]Declaration, 0, len(tools))
	for i, tool := range tools {
		kind := ToolKind(strings.TrimSpace(tool.Type))
		switch kind {
		case ToolKindFunction:
			declarations = append(declarations, Declaration{Name: tool.Function.Name, Kind: kind})
		case ToolKindCustom:
			var custom struct {
				Name string `json:"name"`
			}
			if !rawPresent(tool.Custom) || common.UnmarshalUnique(tool.Custom, &custom) != nil {
				return Policy{}, invalidPolicyf("chat tool %d has an invalid custom definition", i)
			}
			declarations = append(declarations, Declaration{Name: custom.Name, Kind: kind})
		default:
			return Policy{}, invalidPolicyf("chat tool %d has unsupported type %q", i, tool.Type)
		}
	}
	choice, err := parseChatChoice(toolChoice)
	if err != nil {
		return Policy{}, err
	}
	return newPolicy(declarations, choice)
}

func BuildFromResponses(tools, toolChoice json.RawMessage) (Policy, error) {
	declarations, err := parseResponsesDeclarations(tools)
	if err != nil {
		return Policy{}, err
	}
	choice, err := parseResponsesChoice(toolChoice)
	if err != nil {
		return Policy{}, err
	}
	return newPolicy(declarations, choice)
}

func BuildFromClaude(tools, toolChoice any) (Policy, error) {
	declarations, err := parseClaudeDeclarations(tools)
	if err != nil {
		return Policy{}, err
	}
	choice, err := parseClaudeChoice(toolChoice)
	if err != nil {
		return Policy{}, err
	}
	return newPolicy(declarations, choice)
}

func BuildFromGemini(tools json.RawMessage, toolConfig *dto.ToolConfig) (Policy, error) {
	declarations, err := parseGeminiDeclarations(tools)
	if err != nil {
		return Policy{}, err
	}
	choice, err := parseGeminiChoice(toolConfig)
	if err != nil {
		return Policy{}, err
	}
	return newPolicy(declarations, choice)
}

func (p Policy) HasCatalog() bool {
	return len(p.allowed) > 0
}

func (p Policy) ValidateCall(call Call) error {
	id := strings.TrimSpace(call.ID)
	name := strings.TrimSpace(call.Name)
	if id == "" || name == "" {
		return fmt.Errorf("%w: tool call id and name are required", ErrMalformedToolCall)
	}
	kind := call.Kind
	if kind == "" {
		kind = ToolKindFunction
	}
	if kind != ToolKindFunction && kind != ToolKindCustom {
		return fmt.Errorf("%w: unsupported tool kind %q", ErrMalformedToolCall, call.Kind)
	}
	declaration, ok := p.allowed[name]
	if !ok {
		return fmt.Errorf("%w: %q", ErrUndeclaredTool, name)
	}
	if declaration.Kind != kind {
		return fmt.Errorf("%w: %q is %q, call is %q", ErrToolKindMismatch, name, declaration.Kind, kind)
	}
	if p.choice == ChoiceNone {
		return fmt.Errorf("%w: tool choice is none", ErrDisallowedTool)
	}
	if p.choice == ChoiceNamed && (name != p.forcedName || kind != p.forcedKind) {
		return fmt.Errorf("%w: tool choice requires %q", ErrDisallowedTool, p.forcedName)
	}
	if len(p.choiceAllowed) > 0 {
		if _, ok := p.choiceAllowed[name]; !ok {
			return fmt.Errorf("%w: %q is outside the effective tool choice", ErrDisallowedTool, name)
		}
	}
	if !common.ValidJSON(call.Arguments) {
		return fmt.Errorf("%w: arguments are not valid JSON", ErrMalformedArguments)
	}
	argumentType := common.GetJsonType(call.Arguments)
	if kind == ToolKindFunction && argumentType != "object" {
		return fmt.Errorf("%w: function arguments must be an object", ErrMalformedArguments)
	}
	if kind == ToolKindCustom && argumentType != "object" && argumentType != "string" {
		return fmt.Errorf("%w: custom input must be a string or object", ErrMalformedArguments)
	}
	return nil
}

func Set(c *gin.Context, policy Policy) {
	if c == nil {
		return
	}
	c.Set(string(constant.ContextKeyToolPolicy), policy.clone())
}

func FromContext(c *gin.Context) (Policy, bool) {
	if c == nil {
		return Policy{}, false
	}
	value, ok := c.Get(string(constant.ContextKeyToolPolicy))
	if !ok {
		return Policy{}, false
	}
	policy, ok := value.(Policy)
	if !ok {
		return Policy{}, false
	}
	return policy.clone(), true
}

func Clear(c *gin.Context) {
	if c != nil {
		c.Set(string(constant.ContextKeyToolPolicy), nil)
	}
}

func (p Policy) clone() Policy {
	clone := Policy{
		allowed:       make(map[string]Declaration, len(p.allowed)),
		choice:        p.choice,
		forcedName:    p.forcedName,
		forcedKind:    p.forcedKind,
		choiceAllowed: make(map[string]struct{}, len(p.choiceAllowed)),
	}
	for name, declaration := range p.allowed {
		clone.allowed[name] = declaration
	}
	for name := range p.choiceAllowed {
		clone.choiceAllowed[name] = struct{}{}
	}
	return clone
}

func newPolicy(declarations []Declaration, choice choiceSpec) (Policy, error) {
	policy := Policy{
		allowed:       make(map[string]Declaration, len(declarations)),
		choice:        choice.mode,
		forcedName:    strings.TrimSpace(choice.forcedName),
		forcedKind:    choice.forcedKind,
		choiceAllowed: make(map[string]struct{}, len(choice.restricted)),
	}
	if policy.choice == "" {
		policy.choice = ChoiceAuto
	}
	for i, declaration := range declarations {
		declaration.Name = strings.TrimSpace(declaration.Name)
		if declaration.Name == "" {
			return Policy{}, invalidPolicyf("tool declaration %d has an empty name", i)
		}
		if declaration.Kind != ToolKindFunction && declaration.Kind != ToolKindCustom {
			return Policy{}, invalidPolicyf("tool declaration %q has unsupported kind %q", declaration.Name, declaration.Kind)
		}
		if existing, ok := policy.allowed[declaration.Name]; ok {
			if existing.Kind != declaration.Kind {
				return Policy{}, invalidPolicyf("tool %q is declared with conflicting kinds", declaration.Name)
			}
			continue
		}
		policy.allowed[declaration.Name] = declaration
	}

	switch policy.choice {
	case ChoiceAuto, ChoiceNone:
	case ChoiceRequired:
		if !policy.HasCatalog() {
			return Policy{}, invalidPolicyf("required tool choice has no tool catalog")
		}
	case ChoiceNamed:
		if policy.forcedName == "" {
			return Policy{}, invalidPolicyf("named tool choice has an empty name")
		}
		declaration, ok := policy.allowed[policy.forcedName]
		if !ok {
			return Policy{}, invalidPolicyf("named tool choice %q is not declared", policy.forcedName)
		}
		if policy.forcedKind == "" {
			policy.forcedKind = declaration.Kind
		}
		if declaration.Kind != policy.forcedKind {
			return Policy{}, invalidPolicyf("named tool choice %q is not declared with kind %q", policy.forcedName, policy.forcedKind)
		}
	default:
		return Policy{}, invalidPolicyf("unsupported tool choice %q", policy.choice)
	}

	for _, rawName := range choice.restricted {
		name := strings.TrimSpace(rawName)
		if name == "" {
			return Policy{}, invalidPolicyf("allowed tool choice contains an empty name")
		}
		declaration, ok := policy.allowed[name]
		if !ok || declaration.Kind != ToolKindFunction {
			return Policy{}, invalidPolicyf("allowed tool choice %q is not a declared function", name)
		}
		policy.choiceAllowed[name] = struct{}{}
	}
	return policy, nil
}

func parseResponsesDeclarations(raw json.RawMessage) ([]Declaration, error) {
	if !rawPresent(raw) {
		return nil, nil
	}
	var items []json.RawMessage
	if err := common.UnmarshalUnique(raw, &items); err != nil {
		return nil, invalidPolicyf("invalid responses tools: %v", err)
	}
	declarations := make([]Declaration, 0, len(items))
	for i, item := range items {
		var tool struct {
			Type string `json:"type"`
			Name string `json:"name"`
		}
		if err := common.UnmarshalUnique(item, &tool); err != nil {
			return nil, invalidPolicyf("invalid responses tool %d: %v", i, err)
		}
		kind := ToolKind(strings.TrimSpace(tool.Type))
		switch kind {
		case ToolKindFunction, ToolKindCustom:
			declarations = append(declarations, Declaration{Name: tool.Name, Kind: kind})
		case "":
			return nil, invalidPolicyf("responses tool %d has no type", i)
		default:
			name := strings.TrimSpace(tool.Name)
			if name == "" {
				name = string(kind)
			}
			declarations = append(declarations, Declaration{Name: name, Kind: ToolKindCustom})
		}
	}
	return declarations, nil
}

func parseClaudeDeclarations(tools any) ([]Declaration, error) {
	if tools == nil {
		return nil, nil
	}
	raw, err := common.Marshal(tools)
	if err != nil {
		return nil, invalidPolicyf("invalid claude tools: %v", err)
	}
	if !rawPresent(raw) {
		return nil, nil
	}
	var items []json.RawMessage
	if err := common.UnmarshalUnique(raw, &items); err != nil {
		return nil, invalidPolicyf("invalid claude tools: %v", err)
	}
	declarations := make([]Declaration, 0, len(items))
	for i, item := range items {
		var tool struct {
			Type string `json:"type"`
			Name string `json:"name"`
		}
		if err := common.UnmarshalUnique(item, &tool); err != nil {
			return nil, invalidPolicyf("invalid claude tool %d: %v", i, err)
		}
		if strings.TrimSpace(tool.Type) != "" {
			declarations = append(declarations, Declaration{Name: tool.Name, Kind: ToolKindCustom})
		} else {
			declarations = append(declarations, Declaration{Name: tool.Name, Kind: ToolKindFunction})
		}
	}
	return declarations, nil
}

func parseGeminiDeclarations(raw json.RawMessage) ([]Declaration, error) {
	if !rawPresent(raw) {
		return nil, nil
	}
	var items []json.RawMessage
	switch common.GetJsonType(raw) {
	case "array":
		if err := common.UnmarshalUnique(raw, &items); err != nil {
			return nil, invalidPolicyf("invalid gemini tools: %v", err)
		}
	case "object":
		items = []json.RawMessage{append(json.RawMessage(nil), raw...)}
	default:
		return nil, invalidPolicyf("gemini tools must be an object or array")
	}

	var declarations []Declaration
	for i, item := range items {
		var tool map[string]json.RawMessage
		if err := common.UnmarshalUnique(item, &tool); err != nil {
			return nil, invalidPolicyf("invalid gemini tool %d: %v", i, err)
		}
		rawFunctions, ok := tool["functionDeclarations"]
		if !ok && len(tool) == 0 {
			return nil, invalidPolicyf("gemini tool %d is empty", i)
		}
		if ok {
			var functions []struct {
				Name string `json:"name"`
			}
			if err := common.UnmarshalUnique(rawFunctions, &functions); err != nil {
				return nil, invalidPolicyf("invalid gemini function declarations in tool %d: %v", i, err)
			}
			for _, function := range functions {
				declarations = append(declarations, Declaration{Name: function.Name, Kind: ToolKindFunction})
			}
		}
		for name := range tool {
			if name != "functionDeclarations" {
				declarations = append(declarations, Declaration{Name: name, Kind: ToolKindCustom})
			}
		}
	}
	return declarations, nil
}

func parseChatChoice(value any) (choiceSpec, error) {
	if value == nil {
		return choiceSpec{mode: ChoiceAuto}, nil
	}
	if choice, ok := value.(string); ok {
		return parseSimpleChoice(choice)
	}
	raw, err := common.Marshal(value)
	if err != nil {
		return choiceSpec{}, invalidPolicyf("invalid chat tool choice: %v", err)
	}
	var choice struct {
		Type     string `json:"type"`
		Function *struct {
			Name string `json:"name"`
		} `json:"function"`
		Custom *struct {
			Name string `json:"name"`
		} `json:"custom"`
	}
	if err := common.UnmarshalUnique(raw, &choice); err != nil {
		return choiceSpec{}, invalidPolicyf("invalid chat tool choice: %v", err)
	}
	switch strings.TrimSpace(choice.Type) {
	case string(ToolKindFunction):
		if choice.Function == nil {
			return choiceSpec{}, invalidPolicyf("chat function choice has no function")
		}
		return namedChoice(choice.Function.Name, ToolKindFunction)
	case string(ToolKindCustom):
		if choice.Custom == nil {
			return choiceSpec{}, invalidPolicyf("chat custom choice has no custom tool")
		}
		return namedChoice(choice.Custom.Name, ToolKindCustom)
	default:
		return choiceSpec{}, invalidPolicyf("unsupported chat tool choice type %q", choice.Type)
	}
}

func parseResponsesChoice(raw json.RawMessage) (choiceSpec, error) {
	if !rawPresent(raw) {
		return choiceSpec{mode: ChoiceAuto}, nil
	}
	if common.GetJsonType(raw) == "string" {
		var choice string
		if err := common.UnmarshalUnique(raw, &choice); err != nil {
			return choiceSpec{}, invalidPolicyf("invalid responses tool choice: %v", err)
		}
		return parseSimpleChoice(choice)
	}
	var choice struct {
		Type string `json:"type"`
		Name string `json:"name"`
	}
	if err := common.UnmarshalUnique(raw, &choice); err != nil {
		return choiceSpec{}, invalidPolicyf("invalid responses tool choice: %v", err)
	}
	kind := ToolKind(strings.TrimSpace(choice.Type))
	if kind == "" {
		return choiceSpec{}, invalidPolicyf("responses tool choice has no type")
	}
	if kind != ToolKindFunction && kind != ToolKindCustom {
		name := strings.TrimSpace(choice.Name)
		if name == "" {
			name = string(kind)
		}
		return namedChoice(name, ToolKindCustom)
	}
	return namedChoice(choice.Name, kind)
}

func parseClaudeChoice(value any) (choiceSpec, error) {
	if value == nil {
		return choiceSpec{mode: ChoiceAuto}, nil
	}
	raw, err := common.Marshal(value)
	if err != nil {
		return choiceSpec{}, invalidPolicyf("invalid claude tool choice: %v", err)
	}
	var choice struct {
		Type string `json:"type"`
		Name string `json:"name"`
	}
	if err := common.UnmarshalUnique(raw, &choice); err != nil {
		return choiceSpec{}, invalidPolicyf("invalid claude tool choice: %v", err)
	}
	switch strings.TrimSpace(choice.Type) {
	case "auto":
		return choiceSpec{mode: ChoiceAuto}, nil
	case "none":
		return choiceSpec{mode: ChoiceNone}, nil
	case "any":
		return choiceSpec{mode: ChoiceRequired}, nil
	case "tool":
		return namedChoice(choice.Name, "")
	default:
		return choiceSpec{}, invalidPolicyf("unsupported claude tool choice type %q", choice.Type)
	}
}

func parseGeminiChoice(config *dto.ToolConfig) (choiceSpec, error) {
	if config == nil || config.FunctionCallingConfig == nil {
		return choiceSpec{mode: ChoiceAuto}, nil
	}
	functionConfig := config.FunctionCallingConfig
	mode := strings.TrimSpace(string(functionConfig.Mode))
	choice := choiceSpec{restricted: append([]string(nil), functionConfig.AllowedFunctionNames...)}
	switch mode {
	case "", "AUTO":
		choice.mode = ChoiceAuto
	case "NONE":
		choice.mode = ChoiceNone
	case "ANY":
		choice.mode = ChoiceRequired
	case "VALIDATED":
		choice.mode = ChoiceAuto
	default:
		return choiceSpec{}, invalidPolicyf("unsupported gemini function calling mode %q", mode)
	}
	if len(choice.restricted) > 0 && mode != "ANY" && mode != "VALIDATED" {
		return choiceSpec{}, invalidPolicyf("gemini allowedFunctionNames require ANY or VALIDATED mode")
	}
	return choice, nil
}

func parseSimpleChoice(choice string) (choiceSpec, error) {
	switch strings.TrimSpace(choice) {
	case "auto":
		return choiceSpec{mode: ChoiceAuto}, nil
	case "none":
		return choiceSpec{mode: ChoiceNone}, nil
	case "required":
		return choiceSpec{mode: ChoiceRequired}, nil
	default:
		return choiceSpec{}, invalidPolicyf("unsupported tool choice %q", choice)
	}
}

func namedChoice(name string, kind ToolKind) (choiceSpec, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return choiceSpec{}, invalidPolicyf("named tool choice has an empty name")
	}
	return choiceSpec{mode: ChoiceNamed, forcedName: name, forcedKind: kind}, nil
}

func rawPresent(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) > 0 && !bytes.Equal(trimmed, []byte("null"))
}

func invalidPolicyf(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidPolicy, fmt.Sprintf(format, args...))
}
