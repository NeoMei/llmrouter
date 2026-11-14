package constant

type EndpointType string

const (
	EndpointTypePaddleOCR             EndpointType = "paddleocr_jobs"
	EndpointTypeOpenAI                EndpointType = "openai"
	EndpointTypeOpenAIResponse        EndpointType = "openai-response"
	EndpointTypeOpenAIResponseCompact EndpointType = "openai-response-compact"
	EndpointTypeDeepSeekToolsSearch   EndpointType = "deepseek-tools-search"
	EndpointTypeDeepSeekToolsFetch    EndpointType = "deepseek-tools-fetch"
	EndpointTypeAnthropic             EndpointType = "anthropic"
	EndpointTypeGemini                EndpointType = "gemini"
	EndpointTypeJinaRerank            EndpointType = "jina-rerank"
	EndpointTypeImageGeneration       EndpointType = "image-generation"
	EndpointTypeEmbeddings            EndpointType = "embeddings"
	EndpointTypeOpenAIVideo           EndpointType = "openai-video"
	//EndpointTypeMidjourney     EndpointType = "midjourney-proxy"
	//EndpointTypeSuno           EndpointType = "suno-proxy"
	//EndpointTypeKling          EndpointType = "kling"
	//EndpointTypeJimeng         EndpointType = "jimeng"
)

// DeepSeek Search and Fetch routing names, quoted in channel model lists.
const (
	DeepSeekSearchModel = "deepseek-search"
	DeepSeekFetchModel  = "deepseek-fetch"
)
