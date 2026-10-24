package relay

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/service/accountpool"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
)

const maxMultimodalVectorResponse = 64 << 20

func invalidMultimodalResponse() *types.NewAPIError {
	return types.NewErrorWithStatusCode(fmt.Errorf("invalid or incomplete Qwen multimodal embedding response"), types.ErrorCodeBadResponseBody, http.StatusBadGateway, types.ErrOptionWithSkipRetry())
}

// Validate the full batch and actual model-specific usage before any output or
// settlement. Nullable scalars and number tokens prevent missing/null=zero.
func normalizeMultimodalEmbedding(raw []byte, s dto.EmbeddingSpace, count, visualCap int, native bool, publicModel string) ([]byte, *dto.Usage, error) {
	var result struct {
		Model  string          `json:"model"`
		Code   string          `json:"code"`
		Error  json.RawMessage `json:"error"`
		Output *struct {
			Embeddings []json.RawMessage `json:"embeddings"`
		} `json:"output"`
		Usage json.RawMessage `json:"usage"`
	}
	if common.UnmarshalUnique(raw, &result) != nil || result.Code != "" || (len(result.Error) > 0 && string(result.Error) != "null") || (result.Model != "" && result.Model != s.Model) || result.Output == nil || len(result.Output.Embeddings) != count {
		return nil, nil, fmt.Errorf("invalid vector envelope")
	}
	var fieldsTop map[string]json.RawMessage
	if common.Unmarshal(raw, &fieldsTop) != nil {
		return nil, nil, fmt.Errorf("invalid response envelope")
	}
	if _, present := fieldsTop["model"]; present && result.Model != s.Model {
		return nil, nil, fmt.Errorf("unexpected response model")
	}
	ordered := make([]json.RawMessage, count)
	canonical := make([]dto.EmbeddingResponseItem, count)
	for _, rawItem := range result.Output.Embeddings {
		var item struct {
			Index     *int          `json:"index"`
			Embedding []json.Number `json:"embedding"`
			Type      string        `json:"type"`
		}
		if common.UnmarshalUnique(rawItem, &item) != nil || item.Index == nil || *item.Index < 0 || *item.Index >= count || ordered[*item.Index] != nil || item.Type != "vl" || len(item.Embedding) != s.Dimensions {
			return nil, nil, fmt.Errorf("invalid vector identity or dimensions")
		}
		var numeric struct {
			Embedding []json.RawMessage `json:"embedding"`
		}
		if common.Unmarshal(rawItem, &numeric) != nil {
			return nil, nil, fmt.Errorf("invalid vector encoding")
		}
		for _, number := range numeric.Embedding {
			if len(number) == 0 || number[0] == '"' || string(number) == "null" {
				return nil, nil, fmt.Errorf("vector requires numeric values")
			}
		}
		vector := make([]float64, s.Dimensions)
		for i, n := range item.Embedding {
			v, err := n.Float64()
			if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
				return nil, nil, fmt.Errorf("invalid vector number")
			}
			vector[i] = v
		}
		// Only documented output fields leave this boundary: vendor echoes cannot
		// disclose internal capability/source URLs through arbitrary extension data.
		clean, err := common.Marshal(struct {
			Index     int           `json:"index"`
			Embedding []json.Number `json:"embedding"`
			Type      string        `json:"type"`
		}{*item.Index, item.Embedding, item.Type})
		if err != nil {
			return nil, nil, err
		}
		ordered[*item.Index] = clean
		canonical[*item.Index] = dto.EmbeddingResponseItem{Object: "embedding", Index: *item.Index, Embedding: vector}
	}
	var u struct {
		Text   *int64 `json:"input_tokens"`
		Visual *int64 `json:"image_tokens"`
		Total  *int64 `json:"total_tokens"`
	}
	var fields map[string]json.RawMessage
	if common.UnmarshalUnique(result.Usage, &fields) != nil || len(fields) != 3 || common.Unmarshal(result.Usage, &u) != nil || u.Text == nil || u.Visual == nil || u.Total == nil || *u.Text < 0 || *u.Text > helper.MultimodalTextReserve || *u.Visual < 0 || *u.Visual > int64(visualCap) || *u.Total != *u.Text+*u.Visual {
		return nil, nil, fmt.Errorf("invalid or out-of-policy actual Qwen usage")
	}
	usage := &dto.Usage{PromptTokens: int(*u.Total), TotalTokens: int(*u.Total)}
	usage.PromptTokensDetails.TextTokens = int(*u.Text)
	usage.PromptTokensDetails.ImageTokens = int(*u.Visual)
	var response any
	if native {
		response = struct {
			Output any                `json:"output"`
			Usage  json.RawMessage    `json:"usage"`
			Space  dto.EmbeddingSpace `json:"embedding_space"`
		}{struct {
			Embeddings []json.RawMessage `json:"embeddings"`
		}{ordered}, result.Usage, s}
	} else {
		response = struct {
			Object string                      `json:"object"`
			Model  string                      `json:"model"`
			Data   []dto.EmbeddingResponseItem `json:"data"`
			Usage  *dto.Usage                  `json:"usage"`
			Space  dto.EmbeddingSpace          `json:"embedding_space"`
		}{"list", publicModel, canonical, usage, s}
	}
	encoded, err := common.Marshal(response)
	return encoded, usage, err
}
func MultimodalEmbeddingHelper(c *gin.Context, info *relaycommon.RelayInfo) *types.NewAPIError {
	info.InitChannelMeta(c)
	r, ok := info.Request.(*dto.MultimodalEmbeddingRequest)
	if !ok {
		return invalidMultimodalResponse()
	}
	value, ok := c.Get(helper.MultimodalPreparedKey)
	if !ok {
		return invalidMultimodalResponse()
	}
	prepared := value.(*helper.PreparedMultimodal)
	space := info.ModelCapabilities.EmbeddingSpace
	if helper.ValidateMultimodalEmbeddingSpace(r, space) != nil {
		return invalidMultimodalResponse()
	}
	request := dto.QwenMultimodalEmbeddingRequest{Model: info.ResolvedModelNameOrOrigin()}
	if request.Model != space.Model {
		return invalidMultimodalResponse()
	}
	info.UpstreamModelName = request.Model
	request.Input.Contents = prepared.Contents
	dimension := space.Dimensions
	fps := 1.0
	request.Parameters.Dimension = &dimension
	request.Parameters.FPS = &fps
	request.Parameters.EnableFusion = r.EnableFusion
	raw, err := common.Marshal(request)
	if err != nil {
		return invalidMultimodalResponse()
	}
	adaptor := GetAdaptor(info.ApiType)
	if adaptor == nil {
		return invalidMultimodalResponse()
	}
	adaptor.Init(info)
	accountpool.DeferAllStatusNativeOutcome(c)
	info.UpstreamRequestBodySize = int64(len(raw))
	info.UpstreamRequestGetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(raw)), nil }
	response, err := adaptor.DoRequest(c, info, bytes.NewReader(raw))
	if err != nil {
		return types.NewErrorWithStatusCode(fmt.Errorf("multimodal upstream transport failed"), types.ErrorCodeDoRequestFailed, http.StatusBadGateway)
	}
	resp, ok := response.(*http.Response)
	if !ok || resp == nil || resp.Body == nil {
		return invalidMultimodalResponse()
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxMultimodalVectorResponse+1))
	if err != nil || len(body) > maxMultimodalVectorResponse || c.Request.Context().Err() != nil {
		return invalidMultimodalResponse()
	}
	failure, code := accountpool.AliFailure(resp.StatusCode, resp.Header.Get("Retry-After"), body)
	if resp.StatusCode != 200 || code != "" {
		if accountpool.CompleteNativeOutcome(c, &failure) != nil {
			return invalidMultimodalResponse()
		}
		status := resp.StatusCode
		if failure.Refusal == accountpool.RefusalQuotaExhausted {
			status = 429
		}
		if status < 400 {
			status = 502
		}
		return types.NewErrorWithStatusCode(fmt.Errorf("Qwen multimodal upstream refused (HTTP %d)", resp.StatusCode), types.ErrorCodeBadResponse, status)
	}
	encoded, usage, err := normalizeMultimodalEmbedding(body, *space, len(r.Input), prepared.VisualReserve, strings.HasPrefix(c.Request.URL.Path, "/api/"), info.OriginModelName)
	if err != nil {
		return invalidMultimodalResponse()
	}
	if accountpool.CompleteNativeOutcome(c, nil) != nil {
		return invalidMultimodalResponse()
	}
	info.ActualUsageValidated = true
	c.Header("Content-Type", "application/json")
	if n, err := c.Writer.Write(encoded); err != nil || n != len(encoded) {
		return invalidMultimodalResponse()
	}
	service.PostTextConsumeQuota(c, info, usage, nil)
	return nil
}
