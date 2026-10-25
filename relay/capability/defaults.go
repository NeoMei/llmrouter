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

	support([]int{constant.ChannelTypePaddleOCR, constant.ChannelTypeGateway}, OperationOCRCreate, OperationOCRRetrieve, OperationOCRResource)
	allChannels := constant.AllChannelTypes()
	support(allChannels, OperationModelList, OperationModelRetrieve, OperationModelDelete)

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
	support([]int{constant.ChannelTypeAnthropic}, OperationChatCreate)

	claudeCompatible := []int{
		constant.ChannelTypeAnthropic,
		constant.ChannelTypeAws,
		constant.ChannelTypeVertexAi,
		constant.ChannelTypeZhipu_v4,
		constant.ChannelTypeMoonshot,
		constant.ChannelTypeDeepSeek,
		constant.ChannelTypeAdvancedCustom,
		constant.ChannelTypeGateway,
		// OpenAI-compatible upstreams serve /v1/messages via bidirectional
		// claude<->chat conversion implemented in the openai adaptor.
		constant.ChannelTypeOpenAI,
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
	// These adapters explicitly reject embedding conversion; a chat wire does
	// not imply native embedding support. Keep the registry as the sole
	// operation authority for both metadata admission and distribution.
	for _, channelType := range embeddingCompatible {
		switch channelType {
		case constant.ChannelTypeZhipu, constant.ChannelTypePerplexity, constant.ChannelTypeMistral, constant.ChannelTypeDeepSeek, constant.ChannelTypeBaiduV2, constant.ChannelTypeXai, constant.ChannelTypeSubmodel, constant.ChannelTypeCodex, constant.ChannelTypeVertexAi, constant.ChannelTypeCohere:
			continue
		default:
			support([]int{channelType}, OperationEmbeddingCreate)
		}
	}

	support([]int{constant.ChannelTypeOpenAI, constant.ChannelTypeAzure, constant.ChannelTypeAdvancedCustom, constant.ChannelTypeGateway},
		OperationAudioTranscription, OperationAudioTranslation, OperationAudioSpeech,
		OperationImageGeneration, OperationImageEdit,
	)
	support([]int{constant.ChannelTypeOpenAI}, OperationImageVariation)
	support([]int{constant.ChannelTypeAli}, OperationAudioTranscription, OperationImageGeneration, OperationImageEdit)
	support([]int{constant.ChannelTypeMiniMax}, OperationAudioSpeech)

	support([]int{constant.ChannelTypeAli}, OperationMultimodalEmbedding)
	support([]int{constant.ChannelTypeCohere, constant.ChannelTypeJina, constant.ChannelTypeSiliconFlow, constant.ChannelTypeAli}, OperationRerankCreate)

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
