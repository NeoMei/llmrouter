package dto

// ModelCapabilities is an optional, operator-declared model contract. Nil
// fields preserve the legacy relay behavior; explicit false remains meaningful.
// This package owns the transport types so relay/common need not import model.
type ModelCapabilities struct {
	StrictSchema     *bool           `json:"strict_schema,omitempty"`
	MaxOutputTokens  *int            `json:"max_output_tokens,omitempty"`
	InputModalities  []string        `json:"input_modalities,omitempty"`
	OutputModalities []string        `json:"output_modalities,omitempty"`
	Operations       []string        `json:"operations,omitempty"`
	Streaming        *bool           `json:"streaming,omitempty"`
	Async            *bool           `json:"async,omitempty"`
	MediaLimits      *MediaLimits    `json:"media_limits,omitempty"`
	EmbeddingSpace   *EmbeddingSpace `json:"embedding_space,omitempty"`
}

type MediaLimits struct {
	MaxRequestBytes *int64   `json:"max_request_bytes,omitempty"`
	MaxImages       *int     `json:"max_images,omitempty"`
	MaxAudioSeconds *float64 `json:"max_audio_seconds,omitempty"`
	MaxVideoSeconds *float64 `json:"max_video_seconds,omitempty"`
	MaxPages        *int     `json:"max_pages,omitempty"`
	MaxPixels       *int64   `json:"max_pixels,omitempty"`
	Formats         []string `json:"formats,omitempty"`
	VideoAudio      *bool    `json:"video_audio,omitempty"`
}

// EmbeddingSpace is a configured identity, not proof of opaque vendor weights.
// InputSemantics identifies preprocessing and query/document behavior as one
// immutable, operator-versioned contract.
type EmbeddingSpace struct {
	Provider       string `json:"provider"`
	Model          string `json:"model"`
	Version        string `json:"version"`
	Dimensions     int    `json:"dimensions"`
	InputSemantics string `json:"input_semantics"`
}

const MaxEmbeddingDimensions = 65536
