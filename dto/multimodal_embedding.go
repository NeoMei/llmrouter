package dto

import (
	"strings"

	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
)

const QwenMultimodalInputSemantics = "independent/png-jpeg/mp4-h264-noaudio/v1/fps=1"

// MultimodalEmbeddingRequest is deliberately separate from the text vector API.
type MultimodalEmbeddingRequest struct {
	Model        string                       `json:"model"`
	Input        []MultimodalEmbeddingContent `json:"input"`
	Dimensions   *int                         `json:"dimensions,omitempty"`
	FPS          *float64                     `json:"fps,omitempty"`
	EnableFusion *bool                        `json:"enable_fusion,omitempty"`
}
type MultimodalEmbeddingContent struct {
	Text  string `json:"text,omitempty"`
	Image string `json:"image,omitempty"`
	Video string `json:"video,omitempty"`
}

func (r *MultimodalEmbeddingRequest) GetTokenCountMeta() *types.TokenCountMeta {
	texts := make([]string, 0, len(r.Input))
	for _, item := range r.Input {
		if item.Text != "" {
			texts = append(texts, item.Text)
		}
	}
	return &types.TokenCountMeta{CombineText: strings.Join(texts, "\n")}
}
func (r *MultimodalEmbeddingRequest) IsStream(*gin.Context) bool { return false }
func (r *MultimodalEmbeddingRequest) SetModelName(s string) {
	if s != "" {
		r.Model = s
	}
}

type QwenMultimodalEmbeddingRequest struct {
	Model string `json:"model"`
	Input struct {
		Contents []MultimodalEmbeddingContent `json:"contents"`
	} `json:"input"`
	Parameters struct {
		Dimension    *int     `json:"dimension,omitempty"`
		FPS          *float64 `json:"fps,omitempty"`
		EnableFusion *bool    `json:"enable_fusion,omitempty"`
	} `json:"parameters"`
}
