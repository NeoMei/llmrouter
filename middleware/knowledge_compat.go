package middleware

import (
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
)

// AliNativeRerank canonicalizes the native envelope before any relay parser,
// admission or rate limiter. The client URL stays intact for operation/audit.
func AliNativeRerank() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !strings.HasPrefix(c.GetHeader("Content-Type"), "application/json") {
			abortWithOpenAiMessage(c, http.StatusBadRequest, "native rerank requires application/json", types.ErrorCodeInvalidRequest)
			return
		}
		var native struct {
			Model string `json:"model"`
			Input *struct {
				Query     string   `json:"query"`
				Documents []string `json:"documents"`
			} `json:"input"`
			Parameters *struct {
				TopN            *int  `json:"top_n,omitempty"`
				ReturnDocuments *bool `json:"return_documents,omitempty"`
			} `json:"parameters"`
		}
		if err := common.UnmarshalBodyReusable(c, &native); err != nil {
			abortWithOpenAiMessage(c, http.StatusBadRequest, fmt.Sprintf("invalid native rerank request: %v", err), types.ErrorCodeInvalidRequest)
			return
		}
		if native.Model == "" || native.Input == nil || strings.TrimSpace(native.Input.Query) == "" || len(native.Input.Documents) == 0 {
			abortWithOpenAiMessage(c, http.StatusBadRequest, "native rerank requires model, input.query and input.documents", types.ErrorCodeInvalidRequest)
			return
		}
		canonical := dto.RerankRequest{Model: native.Model, Query: native.Input.Query, Documents: make([]any, len(native.Input.Documents))}
		for i, document := range native.Input.Documents {
			if strings.TrimSpace(document) == "" {
				abortWithOpenAiMessage(c, http.StatusBadRequest, "rerank documents must be nonempty strings", types.ErrorCodeInvalidRequest)
				return
			}
			canonical.Documents[i] = document
		}
		if native.Parameters != nil {
			canonical.TopN = native.Parameters.TopN
			canonical.ReturnDocuments = native.Parameters.ReturnDocuments
		}
		if canonical.TopN != nil && (*canonical.TopN <= 0 || *canonical.TopN > len(canonical.Documents)) {
			abortWithOpenAiMessage(c, http.StatusBadRequest, "top_n must be between 1 and the document count", types.ErrorCodeInvalidRequest)
			return
		}
		raw, err := common.Marshal(canonical)
		if err != nil {
			abortWithOpenAiMessage(c, http.StatusBadRequest, err.Error(), types.ErrorCodeInvalidRequest)
			return
		}
		storage, err := common.CreateBodyStorage(raw)
		if err != nil {
			abortWithOpenAiMessage(c, http.StatusInternalServerError, err.Error(), types.ErrorCodeReadRequestBodyFailed)
			return
		}
		common.CleanupBodyStorage(c)
		c.Set(common.KeyRequestBody, nil)
		c.Set(common.KeyBodyStorage, storage)
		c.Request.Body = io.NopCloser(common.ReaderOnly(storage))
		c.Request.ContentLength = storage.Size()
		c.Request.GetBody = storage.NewReader
		common.SetContextKey(c, constant.ContextKeyAliNativeRerank, true)
		c.Next()
	}
}
