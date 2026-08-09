package capability

import (
	"fmt"
	"net/http"
	"net/url"
	"regexp"
)

type Protocol string

const (
	ProtocolOpenAI   Protocol = "openai"
	ProtocolClaude   Protocol = "claude"
	ProtocolGemini   Protocol = "gemini"
	ProtocolTask     Protocol = "task"
	ProtocolInternal Protocol = "internal"
)

type Operation string

const (
	OperationModelList            Operation = "models.list"
	OperationModelRetrieve        Operation = "models.retrieve"
	OperationModelDelete          Operation = "models.delete"
	OperationChatCreate           Operation = "chat.completions.create"
	OperationCompletionCreate     Operation = "completions.create"
	OperationResponseCreate       Operation = "responses.create"
	OperationResponseCompact      Operation = "responses.compact"
	OperationRealtimeConnect      Operation = "realtime.connect"
	OperationClaudeMessageCreate  Operation = "claude.messages.create"
	OperationEmbeddingCreate      Operation = "embeddings.create"
	OperationAudioTranscription   Operation = "audio.transcriptions.create"
	OperationAudioTranslation     Operation = "audio.translations.create"
	OperationAudioSpeech          Operation = "audio.speech.create"
	OperationImageGeneration      Operation = "images.generations.create"
	OperationImageEdit            Operation = "images.edits.create"
	OperationImageVariation       Operation = "images.variations.create"
	OperationRerankCreate         Operation = "rerank.create"
	OperationModerationCreate     Operation = "moderations.create"
	OperationFileList             Operation = "files.list"
	OperationFileCreate           Operation = "files.create"
	OperationFileRetrieve         Operation = "files.retrieve"
	OperationFileContent          Operation = "files.content"
	OperationFileDelete           Operation = "files.delete"
	OperationFineTuningCreate     Operation = "fine_tuning.jobs.create"
	OperationFineTuningList       Operation = "fine_tuning.jobs.list"
	OperationFineTuningRetrieve   Operation = "fine_tuning.jobs.retrieve"
	OperationFineTuningCancel     Operation = "fine_tuning.jobs.cancel"
	OperationFineTuningEventsList Operation = "fine_tuning.events.list"
	OperationGeminiGenerate       Operation = "gemini.generate"
	OperationGeminiEmbed          Operation = "gemini.embed"
	OperationVideoCreate          Operation = "videos.create"
	OperationVideoRetrieve        Operation = "videos.retrieve"
	OperationVideoRemix           Operation = "videos.remix"
	OperationVideoContent         Operation = "videos.content"
	OperationMidjourneySubmit     Operation = "midjourney.submit"
	OperationMidjourneyFetch      Operation = "midjourney.fetch"
	OperationSunoSubmit           Operation = "suno.submit"
	OperationSunoFetch            Operation = "suno.fetch"
)

var allOperations = []Operation{
	OperationModelList, OperationModelRetrieve, OperationModelDelete,
	OperationChatCreate, OperationCompletionCreate, OperationResponseCreate, OperationResponseCompact,
	OperationRealtimeConnect, OperationClaudeMessageCreate, OperationEmbeddingCreate,
	OperationAudioTranscription, OperationAudioTranslation, OperationAudioSpeech,
	OperationImageGeneration, OperationImageEdit, OperationImageVariation,
	OperationRerankCreate, OperationModerationCreate,
	OperationFileList, OperationFileCreate, OperationFileRetrieve, OperationFileContent, OperationFileDelete,
	OperationFineTuningCreate, OperationFineTuningList, OperationFineTuningRetrieve,
	OperationFineTuningCancel, OperationFineTuningEventsList,
	OperationGeminiGenerate, OperationGeminiEmbed,
	OperationVideoCreate, OperationVideoRetrieve, OperationVideoRemix, OperationVideoContent,
	OperationMidjourneySubmit, OperationMidjourneyFetch, OperationSunoSubmit, OperationSunoFetch,
}

func AllOperations() []Operation {
	return append([]Operation(nil), allOperations...)
}

func ProtocolForOperation(operation Operation) Protocol {
	switch operation {
	case OperationClaudeMessageCreate:
		return ProtocolClaude
	case OperationGeminiGenerate, OperationGeminiEmbed:
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
	method    string
	pattern   *regexp.Regexp
	protocol  Protocol
	operation Operation
}

func route(method, pattern string, operation Operation) routeSpec {
	return routeSpec{method: method, pattern: regexp.MustCompile(pattern), protocol: ProtocolForOperation(operation), operation: operation}
}

var publicRoutes = []routeSpec{
	route(http.MethodGet, `^/v1/models$`, OperationModelList),
	route(http.MethodGet, `^/v1/models/[^/]+$`, OperationModelRetrieve),
	route(http.MethodDelete, `^/v1/models/[^/]+$`, OperationModelDelete),
	route(http.MethodPost, `^/v1/chat/completions$`, OperationChatCreate),
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
	route(http.MethodGet, `^/v1/files$`, OperationFileList),
	route(http.MethodPost, `^/v1/files$`, OperationFileCreate),
	route(http.MethodGet, `^/v1/files/[^/]+/content$`, OperationFileContent),
	route(http.MethodGet, `^/v1/files/[^/]+$`, OperationFileRetrieve),
	route(http.MethodDelete, `^/v1/files/[^/]+$`, OperationFileDelete),
	route(http.MethodPost, `^/v1/(fine-tunes|fine_tuning/jobs)$`, OperationFineTuningCreate),
	route(http.MethodGet, `^/v1/(fine-tunes|fine_tuning/jobs)$`, OperationFineTuningList),
	route(http.MethodPost, `^/v1/(fine-tunes|fine_tuning/jobs)/[^/]+/cancel$`, OperationFineTuningCancel),
	route(http.MethodGet, `^/v1/(fine-tunes|fine_tuning/jobs)/[^/]+/events$`, OperationFineTuningEventsList),
	route(http.MethodGet, `^/v1/(fine-tunes|fine_tuning/jobs)/[^/]+$`, OperationFineTuningRetrieve),
	route(http.MethodPost, `^/v1beta/models/[^/]+:(generateContent|streamGenerateContent|countTokens)$`, OperationGeminiGenerate),
	route(http.MethodPost, `^/v1beta/models/[^/]+:(embedContent|batchEmbedContents)$`, OperationGeminiEmbed),
	route(http.MethodPost, `^/v1/(video/generations|videos)$`, OperationVideoCreate),
	route(http.MethodGet, `^/v1/(video/generations|videos)/[^/]+$`, OperationVideoRetrieve),
	route(http.MethodPost, `^/v1/videos/[^/]+/remix$`, OperationVideoRemix),
	route(http.MethodGet, `^/v1/videos/[^/]+/content$`, OperationVideoContent),
	route(http.MethodPost, `^/(?:[^/]+/)?mj/submit/[^/]+$`, OperationMidjourneySubmit),
	route(http.MethodGet, `^/(?:[^/]+/)?mj/task/[^/]+/(fetch|image-seed)$`, OperationMidjourneyFetch),
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
