package capability

import (
	"sync"

	"github.com/QuantumNous/new-api/constant"
)

var (
	defaultRegistryOnce sync.Once
	defaultRegistry     Registry
)

func DefaultRegistry() Registry {
	defaultRegistryOnce.Do(func() {
		registry, err := NewRegistry(constant.AllChannelTypes(), AllOperations(), defaultDeclarations())
		if err != nil {
			panic(err)
		}
		defaultRegistry = registry
	})
	return defaultRegistry
}

func defaultDeclarations() []Declaration {
	declarations := make([]Declaration, 0)
	support := func(channelTypes []int, operations ...Operation) {
		for _, channelType := range channelTypes {
			for _, operation := range operations {
				declarations = append(declarations, Declaration{
					Key: Key{
						ChannelType: channelType,
						Protocol:    ProtocolForOperation(operation),
						Operation:   operation,
					},
					Decision: Decision{Supported: true},
				})
			}
		}
	}

	allChannels := constant.AllChannelTypes()
	support(allChannels, OperationModelList, OperationModelRetrieve)

	openAICompatible := []int{
		constant.ChannelTypeOpenAI,
		constant.ChannelTypeAzure,
		constant.ChannelTypeOllama,
		constant.ChannelTypeOpenAIMax,
		constant.ChannelTypeOhMyGPT,
		constant.ChannelTypeCustom,
		constant.ChannelTypeAILS,
		constant.ChannelTypeAIProxy,
		constant.ChannelTypeAPI2GPT,
		constant.ChannelTypeAIGC2D,
		constant.ChannelTypeZhipu,
		constant.ChannelTypeAli,
		constant.ChannelType360,
		constant.ChannelTypeOpenRouter,
		constant.ChannelTypeAIProxyLibrary,
		constant.ChannelTypeFastGPT,
		constant.ChannelTypeMoonshot,
		constant.ChannelTypeZhipu_v4,
		constant.ChannelTypePerplexity,
		constant.ChannelTypeLingYiWanWu,
		constant.ChannelTypeMiniMax,
		constant.ChannelTypeSiliconFlow,
		constant.ChannelTypeMistral,
		constant.ChannelTypeDeepSeek,
		constant.ChannelTypeVolcEngine,
		constant.ChannelTypeBaiduV2,
		constant.ChannelTypeXinference,
		constant.ChannelTypeXai,
		constant.ChannelTypeSubmodel,
		constant.ChannelTypeCodex,
		constant.ChannelTypeAdvancedCustom,
		constant.ChannelTypeGateway,
	}
	support(openAICompatible,
		OperationChatCreate,
		OperationCompletionCreate,
		OperationResponseCreate,
		OperationResponseCompact,
		OperationRealtimeConnect,
		OperationModerationCreate,
	)

	claudeCompatible := []int{
		constant.ChannelTypeAnthropic,
		constant.ChannelTypeAws,
		constant.ChannelTypeVertexAi,
		constant.ChannelTypeZhipu_v4,
		constant.ChannelTypeMoonshot,
		constant.ChannelTypeAdvancedCustom,
		constant.ChannelTypeGateway,
	}
	support(claudeCompatible, OperationClaudeMessageCreate)

	geminiCompatible := []int{
		constant.ChannelTypeGemini,
		constant.ChannelTypeVertexAi,
		constant.ChannelTypeAws,
		constant.ChannelTypeAdvancedCustom,
		constant.ChannelTypeGateway,
	}
	support(geminiCompatible, OperationGeminiGenerate, OperationGeminiEmbed)

	embeddingCompatible := append(append([]int(nil), openAICompatible...),
		constant.ChannelTypeGemini,
		constant.ChannelTypeVertexAi,
		constant.ChannelTypeJina,
		constant.ChannelTypeCohere,
		constant.ChannelCloudflare,
	)
	support(embeddingCompatible, OperationEmbeddingCreate)

	support([]int{constant.ChannelTypeOpenAI, constant.ChannelTypeAzure, constant.ChannelTypeAdvancedCustom, constant.ChannelTypeGateway},
		OperationAudioTranscription, OperationAudioTranslation, OperationAudioSpeech,
		OperationImageGeneration, OperationImageEdit, OperationImageVariation,
		OperationFileList, OperationFileCreate, OperationFileRetrieve, OperationFileContent, OperationFileDelete,
	)

	// Azure fine-tuning uses a deployment/api-version-specific control plane
	// rather than the OpenAI-compatible /v1/fine_tuning/jobs contract.
	support([]int{constant.ChannelTypeOpenAI, constant.ChannelTypeAdvancedCustom, constant.ChannelTypeGateway},
		OperationFineTuningCreate, OperationFineTuningList, OperationFineTuningRetrieve,
		OperationFineTuningCancel, OperationFineTuningEventsList, OperationModelDelete,
	)

	support([]int{constant.ChannelTypeCohere, constant.ChannelTypeJina, constant.ChannelTypeSiliconFlow}, OperationRerankCreate)

	support([]int{
		constant.ChannelTypeKling,
		constant.ChannelTypeJimeng,
		constant.ChannelTypeVidu,
		constant.ChannelTypeDoubaoVideo,
		constant.ChannelTypeSora,
		constant.ChannelTypeReplicate,
		constant.ChannelTypeVertexAi,
		constant.ChannelTypeGemini,
	}, OperationVideoCreate, OperationVideoRetrieve, OperationVideoRemix, OperationVideoContent)

	support([]int{constant.ChannelTypeMidjourney, constant.ChannelTypeMidjourneyPlus}, OperationMidjourneySubmit, OperationMidjourneyFetch)
	support([]int{constant.ChannelTypeSunoAPI}, OperationSunoSubmit, OperationSunoFetch)
	return declarations
}
