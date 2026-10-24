package helper

import (
	"fmt"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/relay/capability"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/gin-gonic/gin"
)

// ValidateModelAdmission runs before billing and selection. Content is inspected
// in its original typed shape, including video_url objects and input_audio.data;
// no URL is fetched and no capability is guessed from a model name.
func ValidateModelAdmission(c *gin.Context, info *relaycommon.RelayInfo, m dto.ModelCapabilities, operation capability.Operation) error {
	facts, mediaErr := InspectRequestMedia(info.Request)
	if _, audio := info.Request.(*dto.AudioRequest); audio && c.GetString(DomesticASRDataKey) != "" {
		fact, err := DecodeMediaFact("audio", c.GetString(DomesticASRDataKey))
		if err != nil {
			return err
		}
		facts = append(facts, fact)
	}
	if mediaErr != nil {
		return mediaErr
	}
	if m.EmbeddingSpace != nil && operation != capability.OperationEmbeddingCreate {
		return fmt.Errorf("fixed embedding_space requires the validated embeddings.create response contract")
	}
	if len(m.Operations) > 0 && !containsCapability(m.Operations, string(operation)) {
		return fmt.Errorf("model does not declare operation %s", operation)
	}
	if m.Streaming != nil && !*m.Streaming && info.IsStream {
		return fmt.Errorf("model does not support streaming")
	}
	// These operations create asynchronous resources rather than a ready response.
	isAsync := operation == capability.OperationVideoCreate || operation == capability.OperationMidjourneySubmit || operation == capability.OperationSunoSubmit
	if request, ok := info.Request.(*dto.OpenAIResponsesRequest); ok && request.Background != nil && *request.Background {
		isAsync = true
	}
	if m.Async != nil && isAsync && !*m.Async {
		return fmt.Errorf("model synchronous/asynchronous contract does not support operation %s", operation)
	}
	if len(m.InputModalities) == 0 && len(m.OutputModalities) == 0 && m.MediaLimits == nil && m.EmbeddingSpace == nil {
		return nil
	}
	raw, err := common.Marshal(info.Request)
	if err != nil {
		return err
	}
	var body any
	if err = common.Unmarshal(raw, &body); err != nil {
		return err
	}
	modalities := map[string]bool{"text": true}
	counts := map[string]int{}
	switch operation {
	case capability.OperationAudioTranscription, capability.OperationAudioTranslation:
		modalities = map[string]bool{"audio": true}
	case capability.OperationImageEdit, capability.OperationImageVariation:
		modalities["image"] = true
	}
	outputs := []string{"text"}
	switch operation {
	case capability.OperationEmbeddingCreate, capability.OperationGeminiEmbed:
		outputs = []string{"embedding"}
	case capability.OperationImageGeneration, capability.OperationImageEdit, capability.OperationImageVariation:
		outputs = []string{"image"}
	case capability.OperationAudioSpeech:
		outputs = []string{"audio"}
	case capability.OperationVideoCreate, capability.OperationVideoRemix:
		outputs = []string{"video"}
	}
	if object, ok := body.(map[string]any); ok {
		if values, ok := object["modalities"].([]any); ok {
			outputs = nil
			for _, value := range values {
				name, ok := value.(string)
				if !ok {
					return fmt.Errorf("invalid output modality")
				}
				outputs = append(outputs, name)
			}
		}
	}
	hostedImageOutput := false
	if request, ok := info.Request.(*dto.OpenAIResponsesRequest); ok && len(request.Tools) > 0 {
		// Only recognized hosted tool declarations imply model-produced media.
		// Function schemas, descriptions and call arguments remain opaque user data.
		var tools []struct {
			Type string `json:"type"`
		}
		if err := common.Unmarshal(request.Tools, &tools); err != nil {
			return fmt.Errorf("invalid Responses tools: %w", err)
		}
		for _, tool := range tools {
			if tool.Type == "image_generation" {
				hostedImageOutput = true
				outputs = append(outputs, "image")
				break
			}
		}
	}

	for _, output := range outputs {
		if len(m.OutputModalities) > 0 && !containsCapability(m.OutputModalities, output) {
			return fmt.Errorf("model does not support %s output", output)
		}
	}
	inspectModelMedia(body, modalities, counts)
	for modality := range modalities {
		if len(m.InputModalities) > 0 && !containsCapability(m.InputModalities, modality) {
			return fmt.Errorf("model does not accept %s input", modality)
		}
	}
	l := m.MediaLimits
	if l != nil {
		requestBytes := c.Request.ContentLength
		if storage, ok := c.Get(common.KeyBodyStorage); ok {
			if bodyStorage, ok := storage.(common.BodyStorage); ok {
				requestBytes = bodyStorage.Size()
			}
		}
		if l.MaxRequestBytes != nil && (requestBytes < 0 || requestBytes > *l.MaxRequestBytes) {
			return fmt.Errorf("request exceeds declared max_request_bytes or has unknown length")
		}
		if req, ok := info.Request.(*dto.ImageRequest); ok && l.MaxImages != nil {
			n := uint(1)
			if req.N != nil {
				n = *req.N
			}
			if n > uint(*l.MaxImages) {
				return fmt.Errorf("image output count exceeds declared max_images")
			}
		}
		if l.MaxImages != nil && hostedImageOutput {
			return fmt.Errorf("declared hosted-image count requires verified media metadata")
		}
		if l.MaxImages != nil && (operation == capability.OperationImageEdit || operation == capability.OperationImageVariation) {
			return fmt.Errorf("declared reference-image count requires verified media metadata")
		}
		if l.MaxImages != nil && counts["image"] > *l.MaxImages {
			return fmt.Errorf("image count exceeds declared max_images")
		}
		if (l.MaxPages != nil && modalities["document"]) || (l.MaxPixels != nil && containsCapability(outputs, "image")) || (l.MaxAudioSeconds != nil && containsCapability(outputs, "audio")) || (l.MaxVideoSeconds != nil && containsCapability(outputs, "video")) {
			return fmt.Errorf("declared output/document bounds require verified media metadata")
		}
		if len(l.Formats) > 0 && (modalities["document"] || containsCapability(outputs, "image") || containsCapability(outputs, "audio") || containsCapability(outputs, "video")) {
			return fmt.Errorf("declared output/document format requires verified media metadata")
		}
		verifiedInputs := map[string]int{}
		for _, fact := range facts {
			verifiedInputs[fact.Modality]++
		}
		for _, modality := range []string{"image", "audio", "video"} {
			requiresFacts := len(l.Formats) > 0 || (modality == "image" && l.MaxPixels != nil) || (modality == "audio" && l.MaxAudioSeconds != nil) || (modality == "video" && l.MaxVideoSeconds != nil)
			if requiresFacts && (counts[modality] > verifiedInputs[modality] || (modalities[modality] && verifiedInputs[modality] == 0)) {
				return fmt.Errorf("declared media bounds require verified media metadata")
			}
		}
		for _, fact := range facts {
			required := len(l.Formats) > 0 || (fact.Modality == "image" && l.MaxPixels != nil) || (fact.Modality == "audio" && l.MaxAudioSeconds != nil) || (fact.Modality == "video" && l.MaxVideoSeconds != nil)
			if required && fact.Format == "" {
				return fmt.Errorf("declared media bounds require verified media metadata")
			}
			if len(l.Formats) > 0 && !containsCapability(l.Formats, fact.Format) {
				return fmt.Errorf("decoded format is not declared")
			}
			if fact.Modality == "image" && l.MaxPixels != nil && fact.Pixels > *l.MaxPixels {
				return fmt.Errorf("image exceeds declared max_pixels")
			}
			if fact.Modality == "audio" && l.MaxAudioSeconds != nil && fact.Seconds > *l.MaxAudioSeconds {
				return fmt.Errorf("audio exceeds declared max_audio_seconds")
			}
		}
		if l.VideoAudio != nil && !*l.VideoAudio && modalities["video"] {
			return fmt.Errorf("video audio-track exclusion requires verified media metadata")
		}
	}
	if req, ok := info.Request.(*dto.EmbeddingRequest); ok && m.EmbeddingSpace != nil {
		if req.Dimensions != nil && *req.Dimensions != m.EmbeddingSpace.Dimensions {
			return fmt.Errorf("dimensions differ from fixed embedding_space")
		}
		if req.EncodingFormat != "" && req.EncodingFormat != "float" {
			return fmt.Errorf("fixed embedding_space requires numeric float encoding")
		}
		if _, err := req.InputCount(); err != nil {
			return err
		}
	}
	return nil
}

func containsCapability(values []string, value string) bool {
	for _, v := range values {
		if v == value {
			return true
		}
	}
	return false
}

func inspectModelMedia(value any, modalities map[string]bool, counts map[string]int) {
	switch v := value.(type) {
	case []any:
		for _, item := range v {
			inspectModelMedia(item, modalities, counts)
		}
	case map[string]any:
		kind, _ := v["type"].(string)
		modality := ""
		switch kind {
		case "image_url", "input_image", "image":
			modality = "image"
		case "input_audio", "audio":
			modality = "audio"
		case "video_url", "input_video", "video":
			modality = "video"
		case "file", "input_file", "document":
			modality = "document"
		}
		if modality != "" {
			modalities[modality] = true
			counts[modality]++
		}
		for key, child := range v {
			// Tool arguments and metadata are user data, not media declarations.
			if key == "metadata" || key == "tools" || key == "arguments" {
				continue
			}
			if key == "inlineData" || key == "inline_data" || key == "fileData" || key == "file_data" {
				if data, ok := child.(map[string]any); ok {
					mime, _ := data["mimeType"].(string)
					if mime == "" {
						mime, _ = data["mime_type"].(string)
					}
					for _, media := range []string{"image", "audio", "video"} {
						if strings.HasPrefix(mime, media+"/") {
							modalities[media] = true
							counts[media]++
						}
					}
				}
			}
			inspectModelMedia(child, modalities, counts)
		}
	}
}
