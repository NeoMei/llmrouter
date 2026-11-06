package types

// SupplementalPriceSnapshot freezes the native price inputs that are not part
// of PriceData. It travels with the admitted quote rather than consulting live
// operator configuration after provider work has started.
type SupplementalPriceSnapshot struct {
	QuotaPerUnit          float64                       `json:"quota_per_unit"`
	WebSearchPreviewPrice float64                       `json:"web_search_preview_price"`
	WebSearchPrice        float64                       `json:"web_search_price"`
	FileSearchPrice       float64                       `json:"file_search_price"`
	GeminiInputAudioPrice float64                       `json:"gemini_input_audio_price"`
	GPTImageCalls         map[string]map[string]float64 `json:"gpt_image_calls"`
}
