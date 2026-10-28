package capability

import (
	"fmt"
	"net/http"
	"net/url"
	"regexp"
)

type Protocol string

const (
	ProtocolDashScope  Protocol = "dashscope"
	ProtocolBailian    Protocol = "bailian"
	ProtocolElevenLabs Protocol = "elevenlabs"
	ProtocolPaddleOCR  Protocol = "paddleocr_jobs"
	ProtocolOpenAI     Protocol = "openai"
	ProtocolClaude     Protocol = "claude"
	ProtocolGemini     Protocol = "gemini"
	ProtocolTask       Protocol = "task"
	ProtocolInternal   Protocol = "internal"
)

type Operation string

const (
	OperationGeminiImage                 Operation = "gemini.images.generate"
	OperationQwenImage                   Operation = "dashscope.images.generate"
	OperationQwenOCR                     Operation = "dashscope.ocr.recognitions.create"
	OperationBailianSpeech               Operation = "bailian.audio.speech.create"
	OperationElevenSpeech                Operation = "elevenlabs.audio.speech.create"
	OperationDashScopeAudioTranscription Operation = "dashscope.audio.transcriptions.create"
	OperationOCRCreate                   Operation = "ocr.jobs.create"
	OperationOCRRetrieve                 Operation = "ocr.jobs.retrieve"
	OperationOCRResource                 Operation = "ocr.resources.retrieve"
	OperationModelList                   Operation = "models.list"
	OperationModelRetrieve               Operation = "models.retrieve"
	OperationModelDelete                 Operation = "models.delete"
	OperationChatCreate                  Operation = "chat.completions.create"
	OperationCompletionCreate            Operation = "completions.create"
	OperationResponseCreate              Operation = "responses.create"
	OperationResponseCompact             Operation = "responses.compact"
	OperationRealtimeConnect             Operation = "realtime.connect"
	OperationClaudeMessageCreate         Operation = "claude.messages.create"
	OperationMultimodalEmbedding         Operation = "multimodal.embeddings.create"
	OperationEmbeddingCreate             Operation = "embeddings.create"
	OperationAudioTranscription          Operation = "audio.transcriptions.create"
	OperationAudioTranslation            Operation = "audio.translations.create"
	OperationAudioSpeech                 Operation = "audio.speech.create"
	OperationImageGeneration             Operation = "images.generations.create"
	OperationImageEdit                   Operation = "images.edits.create"
	OperationImageVariation              Operation = "images.variations.create"
	OperationRerankCreate                Operation = "rerank.create"
	OperationModerationCreate            Operation = "moderations.create"
	OperationGeminiGenerate              Operation = "gemini.generate"
	OperationGeminiEmbed                 Operation = "gemini.embed"
	OperationVideoCreate                 Operation = "videos.create"
	OperationVideoRetrieve               Operation = "videos.retrieve"
	OperationVideoRemix                  Operation = "videos.remix"
	OperationVideoContent                Operation = "videos.content"
	OperationMidjourneySubmit            Operation = "midjourney.submit"
	OperationMidjourneyFetch             Operation = "midjourney.fetch"
	OperationSunoSubmit                  Operation = "suno.submit"
	OperationSunoFetch                   Operation = "suno.fetch"
)

var allOperations = []Operation{
	OperationGeminiImage, OperationQwenImage, OperationQwenOCR, OperationBailianSpeech, OperationElevenSpeech, OperationDashScopeAudioTranscription,
	OperationOCRCreate, OperationOCRRetrieve, OperationOCRResource,
	OperationModelList, OperationModelRetrieve, OperationModelDelete,
	OperationChatCreate, OperationCompletionCreate, OperationResponseCreate, OperationResponseCompact,
	OperationRealtimeConnect, OperationClaudeMessageCreate, OperationEmbeddingCreate, OperationMultimodalEmbedding,
	OperationAudioTranscription, OperationAudioTranslation, OperationAudioSpeech,
	OperationImageGeneration, OperationImageEdit, OperationImageVariation,
	OperationRerankCreate, OperationModerationCreate,
	OperationGeminiGenerate, OperationGeminiEmbed,
	OperationVideoCreate, OperationVideoRetrieve, OperationVideoRemix, OperationVideoContent,
	OperationMidjourneySubmit, OperationMidjourneyFetch, OperationSunoSubmit, OperationSunoFetch,
}

func AllOperations() []Operation {
	return append([]Operation(nil), allOperations...)
}

func ProtocolForOperation(operation Operation) Protocol {
	switch operation {
	case OperationQwenOCR, OperationQwenImage, OperationDashScopeAudioTranscription:
		return ProtocolDashScope
	case OperationBailianSpeech:
		return ProtocolBailian
	case OperationElevenSpeech:
		return ProtocolElevenLabs
	case OperationOCRCreate, OperationOCRRetrieve, OperationOCRResource:
		return ProtocolPaddleOCR
	case OperationClaudeMessageCreate:
		return ProtocolClaude
	case OperationGeminiGenerate, OperationGeminiEmbed, OperationGeminiImage:
		return ProtocolGemini
	case OperationMidjourneySubmit, OperationMidjourneyFetch, OperationSunoSubmit, OperationSunoFetch:
		return ProtocolTask
	case OperationModelList, OperationModelRetrieve:
		return ProtocolInternal
	default:
		return ProtocolOpenAI
	}
}

type routeSpec struct {
	method     string
	rawPattern string
	pattern    *regexp.Regexp
	protocol   Protocol
	operation  Operation
}

func route(method, pattern string, operation Operation) routeSpec {
	return routeSpec{method: method, rawPattern: pattern, pattern: regexp.MustCompile(pattern), protocol: ProtocolForOperation(operation), operation: operation}
}

type RouteManifestEntry struct {
	Method    string    `json:"method"`
	Pattern   string    `json:"pattern"`
	Protocol  Protocol  `json:"protocol"`
	Operation Operation `json:"operation"`
}

func PublicRouteManifest() []RouteManifestEntry {
	manifest := make([]RouteManifestEntry, 0, len(publicRoutes))
	for _, spec := range publicRoutes {
		manifest = append(manifest, RouteManifestEntry{
			Method: spec.method, Pattern: spec.rawPattern, Protocol: spec.protocol, Operation: spec.operation,
		})
	}
	return manifest
}

var publicRoutes = []routeSpec{
	route(http.MethodPost, `^/v1/native/gemini/images/models/[^/]+:generateContent$`, OperationGeminiImage),
	route(http.MethodPost, `^/v1/native/dashscope/images/generations$`, OperationQwenImage),
	route(http.MethodPost, `^/v1/native/bailian/audio/speech$`, OperationBailianSpeech),
	route(http.MethodPost, `^/v1/native/elevenlabs/audio/speech$`, OperationElevenSpeech),
	route(http.MethodPost, `^/v1/native/dashscope/audio/transcriptions$`, OperationDashScopeAudioTranscription),
	route(http.MethodPost, `^/v1/multimodal/embeddings$`, OperationMultimodalEmbedding),
	route(http.MethodPost, `^/api/v1/services/embeddings/multimodal-embedding/multimodal-embedding$`, OperationMultimodalEmbedding),
	route(http.MethodPost, `^/v1/native/dashscope/ocr/recognitions$`, OperationQwenOCR),
	route(http.MethodPost, `^/api/v2/ocr/jobs$`, OperationOCRCreate),
	route(http.MethodGet, `^/api/v2/ocr/jobs/[^/]+$`, OperationOCRRetrieve),
	route(http.MethodPost, `^/v1/ocr$`, OperationOCRCreate),
	route(http.MethodGet, `^/v1/ocr/[^/]+$`, OperationOCRRetrieve),
	route(http.MethodGet, `^/api/ocr/jobs/[^/]+/resources/[^/]+$`, OperationOCRResource),
	route(http.MethodGet, `^/v1/models$`, OperationModelList),
	route(http.MethodGet, `^/v1/models/[^/]+$`, OperationModelRetrieve),
	route(http.MethodDelete, `^/v1/models/[^/]+$`, OperationModelDelete),
	route(http.MethodPost, `^/v1/chat/completions$`, OperationChatCreate),
	route(http.MethodPost, `^/pg/chat/completions$`, OperationChatCreate),
	route(http.MethodPost, `^/compatible-mode/v1/chat/completions$`, OperationChatCreate),
	route(http.MethodPost, `^/compatible-mode/v1/embeddings$`, OperationEmbeddingCreate),
	route(http.MethodPost, `^/api/v1/services/rerank/text-rerank/text-rerank$`, OperationRerankCreate),
	route(http.MethodPost, `^/v1/completions$`, OperationCompletionCreate),
	route(http.MethodPost, `^/v1/responses$`, OperationResponseCreate),
	route(http.MethodPost, `^/v1/responses/compact$`, OperationResponseCompact),
	route(http.MethodGet, `^/v1/realtime$`, OperationRealtimeConnect),
	route(http.MethodPost, `^/v1/messages$`, OperationClaudeMessageCreate),
	route(http.MethodPost, `^/v1/(engines/[^/]+/)?embeddings$`, OperationEmbeddingCreate),
	route(http.MethodPost, `^/v1/audio/transcriptions$`, OperationAudioTranscription),
	route(http.MethodPost, `^/v1/audio/translations$`, OperationAudioTranslation),
	route(http.MethodPost, `^/v1/audio/speech$`, OperationAudioSpeech),
	route(http.MethodPost, `^/v1/images/generations$`, OperationImageGeneration),
	route(http.MethodPost, `^/v1/(images/)?edits$`, OperationImageEdit),
	route(http.MethodPost, `^/v1/images/variations$`, OperationImageVariation),
	route(http.MethodPost, `^/v1/rerank$`, OperationRerankCreate),
	route(http.MethodPost, `^/v1/moderations$`, OperationModerationCreate),
	route(http.MethodPost, `^/v1beta/models/[^/]+:(generateContent|streamGenerateContent|countTokens)$`, OperationGeminiGenerate),
	route(http.MethodPost, `^/v1/models/[^/]+:(generateContent|streamGenerateContent|countTokens)$`, OperationGeminiGenerate),
	route(http.MethodPost, `^/v1beta/models/[^/]+:(embedContent|batchEmbedContents)$`, OperationGeminiEmbed),
	route(http.MethodPost, `^/v1/(video/generations|videos)$`, OperationVideoCreate),
	route(http.MethodGet, `^/v1/(video/generations|videos)/[^/]+$`, OperationVideoRetrieve),
	route(http.MethodPost, `^/v1/videos/[^/]+/remix$`, OperationVideoRemix),
	route(http.MethodGet, `^/v1/videos/[^/]+/content$`, OperationVideoContent),
	route(http.MethodPost, `^/(?:[^/]+/)?mj/(submit|insight-face)/[^/]+$`, OperationMidjourneySubmit),
	route(http.MethodGet, `^/(?:[^/]+/)?mj/task/[^/]+/(fetch|image-seed)$`, OperationMidjourneyFetch),
	route(http.MethodPost, `^/(?:[^/]+/)?mj/task/list-by-condition$`, OperationMidjourneyFetch),
	route(http.MethodPost, `^/suno/submit/[^/]+$`, OperationSunoSubmit),
	route(http.MethodGet, `^/suno/fetch/[^/]+$`, OperationSunoFetch),
	route(http.MethodPost, `^/suno/fetch$`, OperationSunoFetch),
}

func ResolveRequest(method, rawPath string) (Protocol, Operation, error) {
	path := rawPath
	if parsed, err := url.ParseRequestURI(rawPath); err == nil {
		path = parsed.Path
	}
	for _, spec := range publicRoutes {
		if method == spec.method && spec.pattern.MatchString(path) {
			return spec.protocol, spec.operation, nil
		}
	}
	return "", "", fmt.Errorf("unknown capability route: %s %s", method, path)
}
