package ali

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
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

func syncImageError() *types.NewAPIError {
	return types.NewErrorWithStatusCode(errors.New("invalid or incomplete Ali image response"), types.ErrorCodeBadResponseBody, http.StatusBadGateway, types.ErrOptionWithSkipRetry())
}

// qwenImageHandler emits only a fully validated synchronous terminal result.
// Actual URLs are returned as URLs; no provider URL is fetched for conversion.
func qwenImageHandler(c *gin.Context, resp *http.Response, info *relaycommon.RelayInfo) (*types.NewAPIError, *dto.Usage) {
	if resp == nil || resp.Body == nil {
		return syncImageError(), nil
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10+1))
	if err != nil || len(raw) > 64<<10 || c.Request.Context().Err() != nil {
		return syncImageError(), nil
	}
	failure, code := accountpool.AliFailure(resp.StatusCode, resp.Header.Get("Retry-After"), raw)
	if resp.StatusCode != http.StatusOK || code != "" {
		if err := accountpool.CompleteNativeOutcome(c, &failure); err != nil {
			return syncImageError(), nil
		}
		status := resp.StatusCode
		if failure.Refusal == accountpool.RefusalQuotaExhausted {
			status = http.StatusTooManyRequests
		}
		if status < 400 {
			status = http.StatusBadGateway
		}
		// Never expose untrusted native messages, raw bodies or arbitrary code strings.
		return types.NewErrorWithStatusCode(fmt.Errorf("Ali image upstream refused (HTTP %d)", resp.StatusCode), types.ErrorCodeBadResponse, status), nil
	}
	var result struct {
		Code   string          `json:"code"`
		Error  json.RawMessage `json:"error"`
		Output *struct {
			Choices []struct {
				FinishReason string `json:"finish_reason"`
				Message      *struct {
					Role    string `json:"role"`
					Content []struct {
						Image string `json:"image"`
					} `json:"content"`
				} `json:"message"`
			} `json:"choices"`
		} `json:"output"`
		Usage *struct {
			ImageCount *int   `json:"image_count"`
			Width      *int64 `json:"width"`
			Height     *int64 `json:"height"`
		} `json:"usage"`
	}
	if common.UnmarshalUnique(raw, &result) != nil || result.Code != "" || (len(result.Error) > 0 && string(result.Error) != "null") || result.Output == nil || result.Usage == nil || result.Usage.ImageCount == nil || result.Usage.Width == nil || result.Usage.Height == nil {
		return syncImageError(), nil
	}
	expected, ok := c.Get(helper.DomesticImageRequestKey)
	if !ok {
		return syncImageError(), nil
	}
	request := expected.(helper.DomesticImageRequest)
	n := *result.Usage.ImageCount
	w, h := *result.Usage.Width, *result.Usage.Height
	if n < 0 || n > 6 || (info.NativeBilling == nil && n != int(request.Parameters.N)) || w <= 0 || h <= 0 || w > 4194304 || h > 4194304 || w*h < 262144 || w*h > 4194304 {
		return syncImageError(), nil
	}
	if info.NativeBilling == nil && request.Parameters.Size != "" {
		width, height, _ := strings.Cut(request.Parameters.Size, "*")
		ew, _ := strconv.ParseInt(width, 10, 64)
		eh, _ := strconv.ParseInt(height, 10, 64)
		if w != ew || h != eh {
			return syncImageError(), nil
		}
	}
	response := dto.ImageResponse{Created: info.StartTime.Unix(), Data: []dto.ImageData{}}
	if len(result.Output.Choices) != 1 {
		return syncImageError(), nil
	}
	choice := result.Output.Choices[0]
	if choice.Message == nil || choice.Message.Role != "assistant" || choice.FinishReason != "stop" || len(choice.Message.Content) != n {
		return syncImageError(), nil
	}
	seen := map[string]bool{}
	for _, item := range choice.Message.Content {
		u, err := url.ParseRequestURI(item.Image)
		if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || seen[item.Image] {
			return syncImageError(), nil
		}
		seen[item.Image] = true
		response.Data = append(response.Data, dto.ImageData{Url: item.Image})
	}
	encoded, err := common.Marshal(response)
	if err != nil {
		return syncImageError(), nil
	}
	if err := accountpool.CompleteNativeOutcome(c, nil); err != nil {
		return syncImageError(), nil
	}
	c.Header("Content-Type", "application/json")
	written, err := c.Writer.Write(encoded)
	if err != nil || written != len(encoded) || c.Request.Context().Err() != nil {
		return syncImageError(), nil
	}
	if info.NativeBilling != nil {
		outs := []service.NativeImageOutput{}
		for _, o := range response.Data {
			outs = append(outs, service.NativeImageOutput{ID: o.Url, Width: int(w), Height: int(h)})
		}
		if err := service.RecordNativeUsageForRelay(c.Request.Context(), info, service.NativeImageUsage(info.NativeBilling.Price, outs, true)); err != nil {
			return syncImageError(), nil
		}
	}
	info.NativeUsageImages = &n
	info.ActualUsageValidated = true
	info.PriceData.AddOtherRatio("n", float64(n))
	return nil, &dto.Usage{}
}
