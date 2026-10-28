package platformbilling

import (
	"encoding/base64"
	"encoding/json"
	"math"
	"mime"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/relay/toolpolicy"
)

func parseGeminiOperation(config Config, pathModel string, body []byte) (operationRequest, error) {
	var result operationRequest
	if !nativeRequestBounds(config, body) || strings.TrimSpace(pathModel) != pathModel || pathModel == "" || len(pathModel) > 255 || strings.ContainsAny(pathModel, "/:?#%") {
		return result, ErrInvalid
	}
	var original map[string]any
	if common.UnmarshalUnique(body, &original) != nil {
		return result, ErrInvalid
	}
	// Only aliases which the current native DTO preserves are supported. The
	// normalized shallow maps are for validation; original remains the digest.
	request, ok := geminiObject(original, "contents systemInstruction tools toolConfig safetySettings generationConfig", "systemInstruction system_instruction")
	if !ok {
		return result, ErrInvalid
	}
	contents, ok := request["contents"].([]any)
	if !ok || len(contents) == 0 {
		return result, ErrInvalid
	}
	hasMedia := false
	for _, content := range contents {
		valid, media := geminiContent(content, false)
		if !valid {
			return result, ErrInvalid
		}
		hasMedia = hasMedia || media
	}
	if content, present := request["systemInstruction"]; present {
		valid, media := geminiContent(content, true)
		if !valid {
			return result, ErrInvalid
		}
		hasMedia = hasMedia || media
	}
	generation, ok := geminiObject(request["generationConfig"], "maxOutputTokens candidateCount temperature topP topK stopSequences responseMimeType responseSchema responseJsonSchema presencePenalty frequencyPenalty responseLogprobs logprobs enableEnhancedCivicAnswers mediaResolution seed responseModalities thinkingConfig", "maxOutputTokens max_output_tokens candidateCount candidate_count topP top_p topK top_k stopSequences stop_sequences responseMimeType response_mime_type responseSchema response_schema responseJsonSchema response_json_schema presencePenalty presence_penalty frequencyPenalty frequency_penalty responseLogprobs response_logprobs enableEnhancedCivicAnswers enable_enhanced_civic_answers mediaResolution media_resolution responseModalities response_modalities thinkingConfig thinking_config")
	if !ok {
		return result, ErrInvalid
	}
	output, err := confirmedCount(generation["maxOutputTokens"])
	if err != nil || output < 1 || output > config.MaxOutputTokens {
		return result, ErrInvalid
	}
	if value, present := generation["candidateCount"]; present {
		count, err := confirmedCount(value)
		if err != nil || count != 1 {
			return result, ErrInvalid
		}
	}
	if !protocolBools(generation, "responseLogprobs enableEnhancedCivicAnswers") || !protocolEnum(generation, "responseMimeType", "text/plain application/json text/x.enum") || !protocolEnum(generation, "mediaResolution", "MEDIA_RESOLUTION_UNSPECIFIED MEDIA_RESOLUTION_LOW MEDIA_RESOLUTION_MEDIUM MEDIA_RESOLUTION_HIGH") {
		return result, ErrInvalid
	}
	for _, key := range []string{"temperature", "topP", "presencePenalty", "frequencyPenalty"} {
		if value, present := generation[key]; present {
			minimum, maximum := 0.0, 1.0
			switch key {
			case "temperature":
				maximum = 2
			case "presencePenalty", "frequencyPenalty":
				minimum = -2
				maximum = 2
			}
			if !protocolNumber(value, minimum, maximum) {
				return result, ErrInvalid
			}
		}
	}
	for _, key := range []string{"topK", "logprobs"} {
		if value, present := generation[key]; present {
			count, err := confirmedCount(value)
			if err != nil || count > math.MaxInt32 || (key == "logprobs" && count > 20) {
				return result, ErrInvalid
			}
		}
	}
	if _, present := generation["logprobs"]; present && generation["responseLogprobs"] != true {
		return result, ErrInvalid
	}
	if value, present := generation["seed"]; present {
		number, ok := value.(json.Number)
		if !ok {
			return result, ErrInvalid
		}
		if _, err := strconv.ParseInt(string(number), 10, 64); err != nil {
			return result, ErrInvalid
		}
	}
	for _, key := range []string{"stopSequences", "responseModalities"} {
		if value, present := generation[key]; present {
			if !nativeStringList(value) {
				return result, ErrInvalid
			}
			if key == "responseModalities" {
				list := value.([]any)
				if len(list) != 1 || list[0] != "TEXT" {
					return result, ErrInvalid
				}
			}
		}
	}
	for _, key := range []string{"responseSchema", "responseJsonSchema"} {
		if value, present := generation[key]; present {
			schema, ok := value.(map[string]any)
			if !ok {
				return result, ErrInvalid
			}
			switch generation["responseMimeType"] {
			case "application/json":
			case "text/x.enum":
				// The native responseSchema string-enum format has an
				// official text/x.enum example. Other JSON schemas remain
				// opaque and do not gain enum-MIME support by inference.
				enum, ok := schema["enum"].([]any)
				if key != "responseSchema" || schema["type"] != "STRING" || !ok || len(enum) == 0 || !nativeStringList(enum) {
					return result, ErrInvalid
				}
			default:
				return result, ErrInvalid
			}
		}
	}
	if _, present := generation["responseSchema"]; present {
		if _, conflict := generation["responseJsonSchema"]; conflict {
			return result, ErrInvalid
		}
	}
	if value, present := generation["thinkingConfig"]; present {
		thinking, ok := geminiObject(value, "includeThoughts thinkingBudget thinkingLevel", "includeThoughts include_thoughts thinkingBudget thinking_budget thinkingLevel thinking_level")
		if !ok || !protocolBools(thinking, "includeThoughts") || !protocolEnum(thinking, "thinkingLevel", "MINIMAL LOW MEDIUM HIGH minimal low medium high") {
			return result, ErrInvalid
		}
		if budget, present := thinking["thinkingBudget"]; present {
			number, ok := budget.(json.Number)
			if !ok {
				return result, ErrInvalid
			}
			n, err := strconv.ParseInt(string(number), 10, 32)
			if err != nil || n < -1 || n > int64(output) {
				return result, ErrInvalid
			}
			if _, conflict := thinking["thinkingLevel"]; conflict {
				return result, ErrInvalid
			}
		}
	}
	if value, present := request["tools"]; present {
		tools, ok := value.([]any)
		if !ok {
			return result, ErrInvalid
		}
		for _, value := range tools {
			tool, ok := geminiObject(value, "functionDeclarations", "")
			if !ok {
				return result, ErrInvalid
			}
			declarations, ok := tool["functionDeclarations"].([]any)
			if !ok || len(declarations) == 0 {
				return result, ErrInvalid
			}
			for _, value := range declarations {
				declaration, ok := geminiObject(value, "name description parameters parametersJsonSchema response responseJsonSchema behavior", "")
				if !ok || !protocolString(declaration["name"]) || !protocolStrings(declaration, "description") || !protocolEnum(declaration, "behavior", "BLOCKING NON_BLOCKING") {
					return result, ErrInvalid
				}
				for _, key := range []string{"parameters", "parametersJsonSchema", "response", "responseJsonSchema"} {
					if schema, present := declaration[key]; present {
						if _, ok := schema.(map[string]any); !ok {
							return result, ErrInvalid
						}
					}
				}
				for _, pair := range [][2]string{{"parameters", "parametersJsonSchema"}, {"response", "responseJsonSchema"}} {
					if _, present := declaration[pair[0]]; present {
						if _, conflict := declaration[pair[1]]; conflict {
							return result, ErrInvalid
						}
					}
				}
			}
		}
	}
	if value, present := request["toolConfig"]; present {
		toolConfig, ok := geminiObject(value, "functionCallingConfig includeServerSideToolInvocations", "")
		if !ok || !protocolBools(toolConfig, "includeServerSideToolInvocations") {
			return result, ErrInvalid
		}
		if value, present := toolConfig["functionCallingConfig"]; present {
			choice, ok := geminiObject(value, "mode allowedFunctionNames", "")
			if !ok || !protocolEnum(choice, "mode", "MODE_UNSPECIFIED AUTO ANY NONE VALIDATED") {
				return result, ErrInvalid
			}
			if names, present := choice["allowedFunctionNames"]; present && !nativeStringList(names) {
				return result, ErrInvalid
			}
		}
	}
	if value, present := request["safetySettings"]; present {
		settings, ok := value.([]any)
		if !ok {
			return result, ErrInvalid
		}
		seen := map[string]bool{}
		for _, value := range settings {
			setting, ok := geminiObject(value, "category threshold", "")
			if !ok || !protocolString(setting["category"]) || !protocolString(setting["threshold"]) || !protocolEnum(setting, "category", "HARM_CATEGORY_HATE_SPEECH HARM_CATEGORY_SEXUALLY_EXPLICIT HARM_CATEGORY_DANGEROUS_CONTENT HARM_CATEGORY_HARASSMENT HARM_CATEGORY_CIVIC_INTEGRITY HARM_CATEGORY_JAILBREAK") || !protocolEnum(setting, "threshold", "HARM_BLOCK_THRESHOLD_UNSPECIFIED BLOCK_LOW_AND_ABOVE BLOCK_MEDIUM_AND_ABOVE BLOCK_ONLY_HIGH BLOCK_NONE OFF") {
				return result, ErrInvalid
			}
			category := setting["category"].(string)
			if seen[category] {
				return result, ErrInvalid
			}
			seen[category] = true
		}
	}
	canonical, err := common.Marshal(original)
	if err != nil {
		return result, ErrInvalid
	}
	var decoded dto.GeminiChatRequest
	if common.Unmarshal(canonical, &decoded) != nil {
		return result, ErrInvalid
	}
	if _, err := toolpolicy.BuildFromGemini(decoded.Tools, decoded.ToolConfig); err != nil {
		return result, ErrInvalid
	}
	quantity, snapshot, mediaErr := nativeMediaReservation(config, "gemini.generate", pathModel, original)
	if mediaErr != nil {
		return result, mediaErr
	}
	if !hasMedia && len(body) > config.MaxInputTokens/2 {
		return result, ErrInvalid
	}
	result.Model = pathModel
	result.Output = output
	result.Quantity = quantity
	result.MediaSnapshot = snapshot
	result.Canonical = canonical
	return result, nil
}

// geminiObject rejects exact-key and alias collisions, returning a normalized
// validation view without modifying the source map or any opaque child values.
func geminiObject(value any, allowed, aliases string) (map[string]any, bool) {
	object, ok := value.(map[string]any)
	if !ok || object == nil {
		return nil, false
	}
	aliasFields := strings.Fields(aliases)
	normalized := make(map[string]any, len(object))
	for key, value := range object {
		canonical := key
		for index := 0; index < len(aliasFields); index += 2 {
			if key == aliasFields[index+1] {
				canonical = aliasFields[index]
				break
			}
		}
		if _, present := normalized[canonical]; present {
			return nil, false
		}
		normalized[canonical] = value
	}
	return normalized, protocolKeys(normalized, allowed)
}

func geminiContent(value any, system bool) (bool, bool) {
	content, ok := geminiObject(value, "role parts", "")
	if !ok || !protocolEnum(content, "role", "user model") {
		return false, false
	}
	parts, ok := content["parts"].([]any)
	if !ok || len(parts) == 0 {
		return false, false
	}
	hasMedia := false
	for _, value := range parts {
		valid, media := geminiPart(value, system)
		if !valid {
			return false, false
		}
		hasMedia = hasMedia || media
	}
	return true, hasMedia
}

func geminiPart(value any, system bool) (bool, bool) {
	part, ok := geminiObject(value, "text inlineData fileData functionCall functionResponse thought thoughtSignature", "inlineData inline_data")
	if !ok || !protocolBools(part, "thought") || !protocolStrings(part, "thoughtSignature") {
		return false, false
	}
	count := 0
	kind := ""
	for _, key := range []string{"text", "inlineData", "fileData", "functionCall", "functionResponse"} {
		if _, present := part[key]; present {
			count++
			kind = key
		}
	}
	if count != 1 || (system && kind != "text") {
		return false, false
	}
	switch kind {
	case "text":
		_, ok := part["text"].(string)
		return ok, false
	case "inlineData":
		data, ok := geminiObject(part[kind], "mimeType data", "mimeType mime_type")
		if !ok || !protocolString(data["mimeType"]) || !protocolString(data["data"]) {
			return false, false
		}
		if !nativeMediaMIME(data["mimeType"]) {
			return false, false
		}
		_, err := base64.StdEncoding.DecodeString(data["data"].(string))
		return err == nil, true
	case "fileData":
		file, ok := geminiObject(part[kind], "mimeType fileUri", "")
		if !ok || !nativeMediaMIME(file["mimeType"]) || !nativeMediaURL(file["fileUri"]) {
			return false, false
		}
		return true, true
	case "functionCall":
		call, ok := geminiObject(part[kind], "name args id", "")
		if !ok || !protocolString(call["name"]) || !protocolStrings(call, "id") {
			return false, false
		}
		if args, present := call["args"]; present {
			if _, ok := args.(map[string]any); !ok {
				return false, false
			}
		}
		return true, false
	case "functionResponse":
		response, ok := geminiObject(part[kind], "name response id willContinue scheduling parts", "")
		if !ok || !protocolString(response["name"]) || !protocolStrings(response, "id") || !protocolBools(response, "willContinue") || !protocolEnum(response, "scheduling", "SCHEDULING_UNSPECIFIED SILENT WHEN_IDLE INTERRUPT") {
			return false, false
		}
		if _, ok := response["response"].(map[string]any); !ok {
			return false, false
		}
		if parts, present := response["parts"]; present {
			values, ok := parts.([]any)
			if !ok || len(values) == 0 {
				return false, false
			}
			for _, value := range values {
				part, ok := value.(map[string]any)
				if !ok || !protocolKeys(part, "inlineData") {
					return false, false
				}
				valid, media := geminiPart(value, false)
				if !valid || !media {
					return false, false
				}
			}
			return true, true
		}
		return true, false
	default:
		return false, false
	}
}

func nativeMediaMIME(value any) bool {
	raw, ok := value.(string)
	if !ok || !strings.Contains(raw, "/") {
		return false
	}
	parsed, parameters, err := mime.ParseMediaType(raw)
	return err == nil && parsed == raw && len(parameters) == 0
}
