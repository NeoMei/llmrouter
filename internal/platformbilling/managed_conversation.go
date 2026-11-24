package platformbilling

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
)

func managedConversationOperation(wire string, body []byte) (string, error) {
	var imageType string
	switch wire {
	case "chat.completions.create":
		imageType = dto.ContentTypeImageURL
	case "claude.messages.create":
		imageType = "image"
	case "responses.create", "responses.compact":
		imageType = "input_image"
	case "gemini.generate":
		var request dto.GeminiChatRequest
		if err := common.Unmarshal(body, &request); err != nil {
			return "", ErrInvalid
		}
		if geminiConversationHasImage(&request) {
			return "vision", nil
		}
		return "chat", nil
	default:
		return "", nil
	}

	var request struct {
		Messages json.RawMessage `json:"messages"`
		System   json.RawMessage `json:"system"`
		Input    json.RawMessage `json:"input"`
	}
	if err := common.Unmarshal(body, &request); err != nil {
		return "", ErrInvalid
	}
	content := request.Messages
	if imageType == "input_image" {
		content = request.Input
	}
	if conversationContentHasImage(content, imageType) ||
		(wire == "claude.messages.create" && conversationContentHasImage(request.System, imageType)) {
		return "vision", nil
	}
	return "chat", nil
}

// Only protocol content is inspected. Text, tool arguments, and metadata can
// contain arbitrary user JSON and do not determine the operation.
func conversationContentHasImage(raw json.RawMessage, imageType string) bool {
	switch common.GetJsonType(raw) {
	case "array":
		var items []json.RawMessage
		if common.Unmarshal(raw, &items) == nil {
			for _, item := range items {
				if conversationContentHasImage(item, imageType) {
					return true
				}
			}
		}
	case "object":
		var part struct {
			Type    string          `json:"type"`
			Content json.RawMessage `json:"content"`
			Output  json.RawMessage `json:"output"`
		}
		if common.Unmarshal(raw, &part) == nil {
			if part.Type == imageType || conversationContentHasImage(part.Content, imageType) {
				return true
			}
			if part.Type == "function_call_output" || part.Type == "custom_tool_call_output" {
				return conversationContentHasImage(part.Output, imageType)
			}
		}
	}
	return false
}

func geminiConversationHasImage(request *dto.GeminiChatRequest) bool {
	for _, content := range request.Contents {
		if geminiPartsHaveImage(content.Parts) {
			return true
		}
	}
	if request.SystemInstructions != nil && geminiPartsHaveImage(request.SystemInstructions.Parts) {
		return true
	}
	for i := range request.Requests {
		if geminiConversationHasImage(&request.Requests[i]) {
			return true
		}
	}
	return false
}

func geminiPartsHaveImage(parts []dto.GeminiPart) bool {
	for _, part := range parts {
		if (part.InlineData != nil && strings.HasPrefix(strings.ToLower(part.InlineData.MimeType), "image/")) ||
			(part.FileData != nil && strings.HasPrefix(strings.ToLower(part.FileData.MimeType), "image/")) {
			return true
		}
		if part.FunctionResponse != nil && len(part.FunctionResponse.Parts) > 0 {
			var nested []dto.GeminiPart
			if common.Unmarshal(part.FunctionResponse.Parts, &nested) == nil && geminiPartsHaveImage(nested) {
				return true
			}
		}
	}
	return false
}

func (s *Session) verifyManagedConversation(ctx context.Context, task string) error {
	if !s.ProductCredit() || s.call.OriginProductID != "superwagie" {
		return nil
	}
	operation, err := managedConversationOperation(s.call.WireOperation, s.canonical)
	if err != nil || operation == "" {
		return err
	}
	grant, err := s.adapter.managedGrant(ctx, s.nativeCredential, s.call.Model, s.call.WireOperation, "", operation)
	if err != nil {
		return err
	}
	if err := s.adapter.validateManagedCredential(ctx, s.nativeCredential, task, s.call.Model, grant, false); err != nil {
		return err
	}
	if !s.call.matchesCreditScope(grant) || grant.ID != s.call.CredentialID || grant.ActorID != s.call.ActorID ||
		grant.AccountID != s.call.AccountID || grant.OriginProductID != s.call.OriginProductID || grant.InstanceID != s.call.InstanceID {
		return ErrConflict
	}
	s.managedOperation = operation
	return nil
}
