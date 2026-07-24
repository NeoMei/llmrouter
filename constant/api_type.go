package constant

const (
	APITypeOpenAI = iota
	APITypeAnthropic
	APITypePaLM
	APITypeBaidu
	APITypeZhipu
	APITypeAli
	APITypeXunfei
	APITypeAIProxyLibrary
	APITypeTencent
	APITypeGemini
	APITypeZhipuV4
	APITypeOllama
	APITypePerplexity
	APITypeAws
	APITypeCohere
	APITypeDify
	APITypeJina
	APITypeCloudflare
	APITypeSiliconFlow
	APITypeVertexAi
	APITypeMistral
	APITypeDeepSeek
	APITypeMokaAI
	APITypeVolcEngine
	APITypeBaiduV2
	APITypeOpenRouter
	APITypeXinference
	APITypeXai
	APITypeCoze
	APITypeJimeng
	APITypeMoonshot
	APITypeSubmodel
	APITypeMiniMax
	APITypeReplicate
	APITypeCodex
	APITypeAdvancedCustom
	APITypeDummy // this one is only for count, do not add any channel after this
)

// APITypeGateway is a decorator adaptor (not a standalone channel type): it
// wraps an inner adaptor (default OpenAI) to inject provider profiles, platform
// key-pool selection, and model quirk fixes for domestic model providers.
// Deliberately set outside the iota range so the iota-based count sentinel
// (APITypeDummy) and the controller/model.go startup loop (< APITypeDummy) are
// unaffected.
const APITypeGateway = 999
