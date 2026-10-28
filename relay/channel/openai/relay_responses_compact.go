package openai

import (
	"encoding/json"
	"fmt"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"io"
	"net/http"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/types"

	"github.com/gin-gonic/gin"
)

func OaiResponsesCompactionHandler(c *gin.Context, resp *http.Response, info *relaycommon.RelayInfo) (*dto.Usage, *types.NewAPIError) {
	evidence, finish := service.NativeTokenObserver(c.Request.Context(), info, dto.BillingUsageSourceOAIResponses)
	defer finish()
	defer service.CloseResponseBodyGracefully(resp)

	responseBody, err := io.ReadAll(io.LimitReader(resp.Body, (16<<20)+1))
	if err != nil || len(responseBody) > 16<<20 {
		if err == nil {
			err = fmt.Errorf("compact response exceeds 16 MiB")
		}
		return nil, types.NewOpenAIError(err, types.ErrorCodeReadResponseBodyFailed, http.StatusInternalServerError)
	}

	completeUsage := true
	if info != nil && info.PlatformMode {
		var raw map[string]any
		if common.UnmarshalUnique(responseBody, &raw) != nil {
			return nil, types.NewOpenAIError(fmt.Errorf("invalid compact JSON"), types.ErrorCodeBadResponseBody, http.StatusBadGateway)
		}
		usage, _ := raw["usage"].(map[string]any)
		inputDetails, _ := usage["input_tokens_details"].(map[string]any)
		outputDetails, _ := usage["output_tokens_details"].(map[string]any)
		completeUsage = usage["input_tokens"] != nil && usage["output_tokens"] != nil && usage["total_tokens"] != nil && inputDetails["cached_tokens"] != nil && inputDetails["cache_write_tokens"] != nil && outputDetails["reasoning_tokens"] != nil
		for key := range usage {
			switch key {
			case "input_tokens", "output_tokens", "total_tokens", "input_tokens_details", "output_tokens_details":
			default:
				completeUsage = false
			}
		}
		for key := range inputDetails {
			if key != "cached_tokens" && key != "cache_write_tokens" {
				completeUsage = false
			}
		}
		for key := range outputDetails {
			if key != "reasoning_tokens" {
				completeUsage = false
			}
		}
		responseID, _ := raw["id"].(string)
		created, createdOK := raw["created_at"].(json.Number)
		timestamp, timestampErr := created.Int64()
		items, ok := raw["output"].([]any)
		complete := responseID != "" && createdOK && timestampErr == nil && timestamp >= 0 && ok && len(items) > 0 && raw["object"] == "response.compaction" && raw["error"] == nil
		artifact := false
		for index, v := range items {
			item, ok := v.(map[string]any)
			if !ok {
				complete = false
				break
			}
			switch item["type"] {
			case "compaction":
				encrypted, _ := item["encrypted_content"].(string)
				id, _ := item["id"].(string)
				if artifact || encrypted == "" || id == "" || index != len(items)-1 {
					complete = false
				}
				artifact = true
			case "message":
				if item["role"] != "user" {
					complete = false
				}
				switch content := item["content"].(type) {
				case string:
					if content == "" {
						complete = false
					}
				case []any:
					if len(content) == 0 {
						complete = false
					}
					for _, v := range content {
						part, ok := v.(map[string]any)
						if !ok || !validCompactUserContent(part) {
							complete = false
						}
					}
				default:
					complete = false
				}
			default:
				complete = false
			}
		}
		if !complete || !artifact {
			return nil, types.NewOpenAIError(fmt.Errorf("incomplete native compaction output"), types.ErrorCodeBadResponseBody, http.StatusBadGateway)
		}
	}
	if completeUsage {
		evidence.Observe(responseBody, false)
	}
	var compactResp dto.OpenAIResponsesCompactionResponse
	if err := common.Unmarshal(responseBody, &compactResp); err != nil {
		return nil, types.NewOpenAIError(err, types.ErrorCodeBadResponseBody, http.StatusInternalServerError)
	}
	if oaiError := compactResp.GetOpenAIError(); oaiError != nil && oaiError.Type != "" {
		return nil, types.WithOpenAIError(*oaiError, resp.StatusCode)
	}

	service.IOCopyBytesGracefully(c, resp, responseBody)

	usage := dto.Usage{}
	if compactResp.Usage != nil {
		usage.PromptTokens = compactResp.Usage.InputTokens
		usage.CompletionTokens = compactResp.Usage.OutputTokens
		usage.TotalTokens = compactResp.Usage.TotalTokens
		if compactResp.Usage.InputTokensDetails != nil {
			usage.PromptTokensDetails.CachedTokens = compactResp.Usage.InputTokensDetails.CachedTokens
			usage.PromptTokensDetails.CacheWriteTokens = compactResp.Usage.InputTokensDetails.CacheWriteTokens
		}
	}

	return &usage, nil
}

// validCompactUserContent validates preserved Responses input content before the
// managed response can supply confirmed usage. Unknown nonfinancial metadata is
// retained; discriminators and content payloads must match the native union.
func validCompactUserContent(part map[string]any) bool {
	switch part["type"] {
	case "input_text":
		_, ok := part["text"].(string)
		return ok
	case "input_image":
		hasSource := false
		for _, key := range []string{"image_url", "file_id"} {
			if value := part[key]; value != nil {
				source, ok := value.(string)
				if !ok || source == "" {
					return false
				}
				hasSource = true
			}
		}
		if detail, present := part["detail"]; present {
			switch detail {
			case "auto", "low", "high", "original":
			default:
				return false
			}
		}
		return hasSource
	case "input_file":
		hasSource := false
		for _, key := range []string{"file_data", "file_url", "file_id"} {
			value, present := part[key]
			if !present || key == "file_id" && value == nil {
				continue
			}
			source, ok := value.(string)
			if !ok || source == "" {
				return false
			}
			hasSource = true
		}
		if filename, present := part["filename"]; present {
			if _, ok := filename.(string); !ok {
				return false
			}
		}
		if detail, present := part["detail"]; present {
			switch detail {
			case "auto", "low", "high":
			default:
				return false
			}
		}
		return hasSource
	default:
		return false
	}
}
