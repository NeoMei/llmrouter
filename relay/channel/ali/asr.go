package ali

import (
	"bytes"
	"fmt"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"io"
	"math"
	"net/http"
)

func convertTranscription(c *gin.Context, request dto.AudioRequest) (io.Reader, error) {
	data := c.GetString(helper.DomesticASRDataKey)
	if request.Model != "qwen3-asr-flash" || data == "" {
		return nil, fmt.Errorf("unvalidated Flash ASR request")
	}
	payload := map[string]any{"model": request.Model, "stream": false, "messages": []any{map[string]any{"role": "user", "content": []any{map[string]any{"type": "input_audio", "input_audio": map[string]any{"data": data}}}}}}
	raw, err := common.Marshal(payload)
	return bytes.NewReader(raw), err
}
func transcriptionResponse(c *gin.Context, resp *http.Response, info *relaycommon.RelayInfo) (any, *types.NewAPIError) {
	fail := func() (any, *types.NewAPIError) {
		return nil, types.NewErrorWithStatusCode(fmt.Errorf("invalid Flash ASR transcript or actual usage"), types.ErrorCodeBadResponseBody, http.StatusBadGateway, types.ErrOptionWithSkipRetry())
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, (2<<20)+1))
	if err != nil || len(raw) > 2<<20 {
		return fail()
	}
	var result struct {
		Choices []struct {
			Message struct {
				Content *string `json:"content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage *struct {
			Seconds           *float64 `json:"seconds"`
			CompletionDetails *struct {
				Text *int `json:"text_tokens"`
			} `json:"completion_tokens_details"`
			Prompt     *int `json:"prompt_tokens"`
			Completion *int `json:"completion_tokens"`
			Total      *int `json:"total_tokens"`
			Details    *struct {
				Audio *int `json:"audio_tokens"`
				Text  *int `json:"text_tokens"`
			} `json:"prompt_tokens_details"`
		} `json:"usage"`
	}
	if common.UnmarshalUnique(raw, &result) != nil || len(result.Choices) != 1 || result.Choices[0].Message.Content == nil || result.Choices[0].FinishReason != "stop" {
		return fail()
	}
	u := result.Usage
	if u == nil || u.Prompt == nil || u.Completion == nil || u.Total == nil || u.Details == nil || u.Details.Audio == nil || u.Details.Text == nil || u.Seconds == nil || u.CompletionDetails == nil || u.CompletionDetails.Text == nil {
		return fail()
	}
	for _, n := range []int{*u.Prompt, *u.Completion, *u.Total, *u.Details.Audio, *u.Details.Text} {
		if n < 0 || n > math.MaxInt32 {
			return fail()
		}
	}
	if *u.CompletionDetails.Text != *u.Completion || *u.Total != *u.Prompt+*u.Completion || *u.Prompt != *u.Details.Audio+*u.Details.Text {
		return fail()
	}
	if u.Seconds != nil && (math.IsNaN(*u.Seconds) || math.IsInf(*u.Seconds, 0) || *u.Seconds < 0 || *u.Seconds > 300) {
		return fail()
	}
	usage := &dto.Usage{PromptTokens: *u.Prompt, CompletionTokens: *u.Completion, TotalTokens: *u.Total}
	usage.PromptTokensDetails.AudioTokens = *u.Details.Audio
	usage.PromptTokensDetails.TextTokens = *u.Details.Text
	usage.CompletionTokenDetails.TextTokens = *u.Completion
	info.ActualUsageValidated = true
	info.ActualAudioSeconds = u.Seconds
	info.AudioDurationSource = "provider_reported"
	transcript := *result.Choices[0].Message.Content
	var output []byte
	contentType := "text/plain; charset=utf-8"
	if info.Request.(*dto.AudioRequest).ResponseFormat == "text" {
		output = []byte(transcript)
	} else {
		output, err = common.Marshal(dto.AudioResponse{Text: transcript})
		contentType = "application/json"
	}
	if err != nil {
		return fail()
	}
	c.Header("Content-Type", contentType)
	n, err := c.Writer.Write(output)
	if err != nil || n != len(output) {
		return fail()
	}
	return usage, nil
}
