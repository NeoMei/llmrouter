package dto

import (
	"encoding/json"

	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
)

// DeepSeekToolsRequest carries the raw DeepSeek Search/Fetch body. Its model
// is the internal routing name because the vendor body does not choose one.
type DeepSeekToolsRequest struct {
	BaseRequest
	Model   string          `json:"model,omitempty"`
	RawBody json.RawMessage `json:"-"`
}

func (r *DeepSeekToolsRequest) GetTokenCountMeta() *types.TokenCountMeta {
	return &types.TokenCountMeta{CombineText: string(r.RawBody), TokenType: types.TokenTypeTokenizer}
}

func (r *DeepSeekToolsRequest) IsStream(_ *gin.Context) bool { return false }

func (r *DeepSeekToolsRequest) SetModelName(modelName string) {
	if modelName != "" {
		r.Model = modelName
	}
}
