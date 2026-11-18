package dto

import (
	"regexp"
	"strings"
)

// PublicModelMetadata describes configured model contracts. Omitted fields are
// unknown; these declarations do not prove upstream availability or behavior.
type PublicModelMetadata struct {
	ContextLength            int                 `json:"context_length,omitempty"`
	MaxOutputTokens          *int                `json:"max_output_tokens,omitempty"`
	InputModalities          []string            `json:"input_modalities,omitempty"`
	OutputModalities         []string            `json:"output_modalities,omitempty"`
	Operations               []string            `json:"operations,omitempty"`
	Endpoints                map[string]string   `json:"endpoints,omitempty"`
	Capabilities             *PublicCapabilities `json:"capabilities,omitempty"`
	AliasOf                  string              `json:"alias_of,omitempty"`
	ConfiguredUpstreamModels []string            `json:"configured_upstream_models,omitempty"`
}

type PublicCapabilities struct {
	Tools  *bool `json:"tools,omitempty"`
	Vision *bool `json:"vision,omitempty"`
}

// PublicModelEndpoints maps declared operations to the corresponding gateway
// path. Unknown operations are omitted rather than assigned a guessed endpoint.
func PublicModelEndpoints(operations []string) map[string]string {
	paths := map[string]string{
		"chat.completions.create":           "/v1/chat/completions",
		"responses.create":                  "/v1/responses",
		"responses.compact":                 "/v1/responses/compact",
		"claude.messages.create":            "/v1/messages",
		"embeddings.create":                 "/v1/embeddings",
		"multimodal.embeddings.create":      "/v1/multimodal/embeddings",
		"rerank.create":                     "/v1/rerank",
		"images.generations.create":         "/v1/images/generations",
		"images.edits.create":               "/v1/images/edits",
		"dashscope.images.generate":         "/v1/native/dashscope/images/generations",
		"audio.speech.create":               "/v1/audio/speech",
		"audio.transcriptions.create":       "/v1/audio/transcriptions",
		"bailian.audio.speech.create":       "/v1/native/bailian/audio/speech",
		"elevenlabs.audio.speech.create":    "/v1/native/elevenlabs/audio/speech",
		"videos.create":                     "/v1/videos",
		"ocr.jobs.create":                   "/v1/ocr",
		"ocr.jobs.retrieve":                 "/v1/ocr/{id}",
		"dashscope.ocr.recognitions.create": "/v1/native/dashscope/ocr/recognitions",
	}
	result := make(map[string]string)
	for _, operation := range operations {
		if path, ok := paths[operation]; ok {
			result[operation] = path
		}
	}
	return result
}

var publicConfiguredModelID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,190}$`)

// IsPublicConfiguredModelID avoids publishing URL-like or credential-like
// operator values as model IDs. Unknown formats stay undisclosed.
func IsPublicConfiguredModelID(value string) bool {
	return publicConfiguredModelID.MatchString(value) && !strings.Contains(value, "//") && !strings.HasPrefix(strings.ToLower(value), "sk-")
}
