package ali

import (
	"encoding/json"
	"fmt"
	"github.com/QuantumNous/new-api/pkg/metering"
	"io"
	"net/http"
	"strconv"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/types"

	"github.com/gin-gonic/gin"
)

func ConvertRerankRequest(request dto.RerankRequest) *AliRerankRequest {
	returnDocuments := request.ReturnDocuments
	if returnDocuments == nil {
		returnDocuments = common.GetPointer(true)
	}
	return &AliRerankRequest{
		Model: request.Model,
		Input: AliRerankInput{
			Query:     request.Query,
			Documents: request.Documents,
		},
		Parameters: AliRerankParameters{
			TopN:            request.TopN,
			ReturnDocuments: returnDocuments,
		},
	}
}

const maxRerankResponseBytes = 64 << 20

func RerankHandler(c *gin.Context, resp *http.Response, info *relaycommon.RelayInfo) (*types.NewAPIError, *dto.Usage) {
	if resp == nil || resp.Body == nil {
		return types.NewOpenAIError(fmt.Errorf("missing Ali rerank response"), types.ErrorCodeBadResponseBody, http.StatusBadGateway), nil
	}
	responseBody, err := io.ReadAll(io.LimitReader(resp.Body, maxRerankResponseBytes+1))
	// Close directly through the existing helper; never drain an oversized
	// supplier body beyond the bounded read.
	service.CloseResponseBodyGracefully(resp)
	if err != nil || len(responseBody) > maxRerankResponseBytes {
		return types.NewOpenAIError(fmt.Errorf("Ali rerank response unreadable or exceeds size limit"), types.ErrorCodeBadResponseBody, http.StatusBadGateway), nil
	}

	// Inspect actual fields before scalar DTO decoding can invent zero indices,
	// scores or usage. This protects both canonical and native entrypoints.
	var wire struct {
		Output struct {
			Results []json.RawMessage `json:"results"`
		} `json:"output"`
		Usage struct {
			TotalTokens *int `json:"total_tokens"`
		} `json:"usage"`
		AliError
	}
	if err := common.UnmarshalUnique(responseBody, &wire); err != nil {
		return types.NewOpenAIError(err, types.ErrorCodeBadResponseBody, http.StatusBadGateway), nil
	}
	if wire.Code != "" {
		return types.WithOpenAIError(types.OpenAIError{
			Message: wire.Message, Type: wire.Code, Param: wire.RequestId, Code: wire.Code,
		}, resp.StatusCode), nil
	}
	request, ok := info.Request.(*dto.RerankRequest)
	if !ok || request == nil {
		return types.NewOpenAIError(fmt.Errorf("missing Ali rerank request contract"), types.ErrorCodeBadResponseBody, http.StatusBadGateway), nil
	}
	count := len(request.Documents)
	if request.TopN != nil {
		count = *request.TopN
	}
	if err := dto.ValidateRerankResults(wire.Output.Results, len(request.Documents), count); err != nil {
		return types.NewOpenAIError(err, types.ErrorCodeBadResponseBody, http.StatusBadGateway), nil
	}
	if wire.Usage.TotalTokens == nil || *wire.Usage.TotalTokens < 0 {
		return types.NewOpenAIError(fmt.Errorf("Ali rerank response requires nonnegative usage.total_tokens"), types.ErrorCodeBadResponseBody, http.StatusBadGateway), nil
	}

	if info.PlatformMode {
		for _, raw := range wire.Output.Results {
			var item struct {
				Index    int `json:"index"`
				Document *struct {
					Text string `json:"text"`
				} `json:"document"`
			}
			if common.UnmarshalUnique(raw, &item) != nil || (item.Document != nil && item.Document.Text != request.Documents[item.Index]) || (request.ReturnDocuments != nil && *request.ReturnDocuments && item.Document == nil) {
				return types.NewOpenAIError(fmt.Errorf("rerank document differs from original input"), types.ErrorCodeBadResponseBody, http.StatusBadGateway), nil
			}
		}
	}
	if info.PlatformMode {
		var envelope struct {
			Usage map[string]json.RawMessage `json:"usage"`
		}
		if common.UnmarshalUnique(responseBody, &envelope) != nil || len(envelope.Usage) != 1 || envelope.Usage["total_tokens"] == nil || *wire.Usage.TotalTokens > 120000 {
			return types.NewOpenAIError(fmt.Errorf("uncertified Ali rerank counters"), types.ErrorCodeBadResponseBody, http.StatusBadGateway), nil
		}
	}

	var aliResponse AliRerankResponse
	if err := common.Unmarshal(responseBody, &aliResponse); err != nil {
		return types.NewOpenAIError(err, types.ErrorCodeBadResponseBody, http.StatusBadGateway), nil
	}

	usage := dto.Usage{
		PromptTokens:     aliResponse.Usage.TotalTokens,
		CompletionTokens: 0,
		TotalTokens:      aliResponse.Usage.TotalTokens,
	}
	rerankResponse := dto.RerankResponse{
		Results: aliResponse.Output.Results,
		Usage:   usage,
	}

	jsonResponse, err := common.Marshal(rerankResponse)
	if err != nil {
		return types.NewError(err, types.ErrorCodeBadResponseBody), nil
	}
	if info.PlatformMode {
		if info.PlatformBilling != nil {
			cap, _ := info.PlatformBilling.ReservationBounds()
			if int64(*wire.Usage.TotalTokens) > cap {
				return types.NewOpenAIError(fmt.Errorf("Ali rerank usage exceeds certified cap"), types.ErrorCodeBadResponseBody, http.StatusBadGateway), nil
			}
		}
		actual := metering.Usage{Version: "metered_v2", Operation: "rerank", State: "confirmed", Source: "ali_rerank_actual_v1", Measurements: []metering.Measurement{{Key: "input", Unit: "token", Quantity: metering.Quantity{Value: strconv.Itoa(*wire.Usage.TotalTokens)}}}}
		if err := service.RecordNativeUsageForRelay(c.Request.Context(), info, actual); err != nil {
			return types.NewOpenAIError(err, types.ErrorCodeBadResponseBody, http.StatusBadGateway), nil
		}
	}
	c.Writer.Header().Set("Content-Type", "application/json")
	c.Writer.WriteHeader(resp.StatusCode)
	c.Writer.Write(jsonResponse)
	return nil, &usage
}
