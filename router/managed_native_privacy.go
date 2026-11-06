package router

import (
	"encoding/json"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/internal/platformbilling"
)

// Provider resource identifiers are not managed-key ownership proofs. Inspect
// protocol-owned fields only; text, tool arguments and JSON schemas are opaque.
func validateManagedNativeResources(body []byte, wire string) error {
	var request struct {
		Background *bool           `json:"background"`
		Input      json.RawMessage `json:"input"`
		Prompt     json.RawMessage `json:"prompt"`
		Messages   []struct {
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
		Contents []struct {
			Parts []struct {
				FileData json.RawMessage `json:"fileData"`
			} `json:"parts"`
		} `json:"contents"`
		Tools []json.RawMessage `json:"tools"`
	}
	if common.UnmarshalUnique(body, &request) != nil {
		return platformbilling.ErrInvalid
	}
	if request.Background != nil && *request.Background {
		return platformbilling.ErrInvalid
	}
	if wire == "responses.create" || wire == "responses.compact" {
		if nativePrivateReference(request.Prompt) || privateNativeContent(request.Input) {
			return platformbilling.ErrInvalid
		}
	}
	for _, message := range request.Messages {
		if privateNativeContent(message.Content) {
			return platformbilling.ErrInvalid
		}
	}
	for _, content := range request.Contents {
		for _, part := range content.Parts {
			if nativePrivateReference(part.FileData) {
				return platformbilling.ErrInvalid
			}
		}
	}
	for _, raw := range request.Tools {
		var tool struct {
			Type           string          `json:"type"`
			Container      json.RawMessage `json:"container"`
			VectorStoreIDs json.RawMessage `json:"vector_store_ids"`
			FileSearch     json.RawMessage `json:"fileSearch"`
			Retrieval      json.RawMessage `json:"retrieval"`
		}
		if common.UnmarshalUnique(raw, &tool) != nil {
			return platformbilling.ErrInvalid
		}
		if tool.Type == "file_search" && nativePrivateReference(tool.VectorStoreIDs) {
			return platformbilling.ErrInvalid
		}
		if tool.Type == "code_interpreter" && nativePrivateReference(tool.Container) {
			// An automatically created empty container has no prior user's state.
			if string(tool.Container) != `"auto"` {
				var container struct {
					Type    string          `json:"type"`
					FileIDs json.RawMessage `json:"file_ids"`
				}
				if common.UnmarshalUnique(tool.Container, &container) != nil || container.Type != "auto" || nativePrivateReference(container.FileIDs) {
					return platformbilling.ErrInvalid
				}
			}
		}
		if nativePrivateReference(tool.FileSearch) || nativePrivateReference(tool.Retrieval) {
			return platformbilling.ErrInvalid
		}
	}
	return nil
}

func privateNativeContent(raw json.RawMessage) bool {
	if !nativePrivateReference(raw) || raw[0] == '"' {
		return false
	}
	var items []json.RawMessage
	if common.UnmarshalUnique(raw, &items) != nil {
		// A singleton content object is inspected just like an array item. Its
		// overall wire validity remains the native protocol adapter's decision.
		items = []json.RawMessage{raw}
	}
	for _, item := range items {
		var block struct {
			Type   string          `json:"type"`
			FileID json.RawMessage `json:"file_id"`
			File   *struct {
				FileID json.RawMessage `json:"file_id"`
			} `json:"file"`
			Source *struct {
				FileID json.RawMessage `json:"file_id"`
			} `json:"source"`
			Content json.RawMessage `json:"content"`
		}
		if common.UnmarshalUnique(item, &block) != nil {
			continue
		}
		if block.Type == "item_reference" || nativePrivateReference(block.FileID) || block.File != nil && nativePrivateReference(block.File.FileID) || block.Source != nil && nativePrivateReference(block.Source.FileID) {
			return true
		}
		if privateNativeContent(block.Content) {
			return true
		}
	}
	return false
}
