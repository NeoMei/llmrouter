package ali

import (
	"fmt"
	"math"
	"net/http"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/types"
)

// validateUnderstandingResponse holds the selected nonstreaming Omni response
// until its authoritative usage is valid. The adapter must not emit a usable
// answer and only then discover that its billing breakdown is inconsistent.
func validateUnderstandingResponse(resp *http.Response, info *relaycommon.RelayInfo) (common.BodyStorage, *types.NewAPIError) {
	fail := func(err error) (common.BodyStorage, *types.NewAPIError) {
		return nil, types.NewErrorWithStatusCode(err, types.ErrorCodeBadResponseBody, http.StatusBadGateway, types.ErrOptionWithSkipRetry())
	}
	defer resp.Body.Close()
	if info.IsStream {
		return fail(fmt.Errorf("selected Omni understanding path requires nonstreaming response"))
	}
	storage, err := common.CreateBodyStorageFromReader(resp.Body, resp.ContentLength, 16*1024*1024)
	if err != nil {
		return fail(err)
	}
	raw, err := storage.Bytes()
	if err != nil {
		storage.Close()
		return fail(err)
	}
	var envelope struct {
		Usage *dto.Usage `json:"usage"`
	}
	if err = common.Unmarshal(raw, &envelope); err != nil {
		storage.Close()
		return fail(err)
	}
	facts, err := helper.InspectRequestMedia(info.Request)
	if err != nil {
		storage.Close()
		return fail(err)
	}
	for _, fact := range facts {
		if fact.Modality != "audio" && fact.Modality != "video" {
			continue
		}
		var details struct {
			Usage struct {
				Prompt *struct {
					Audio *int `json:"audio_tokens"`
				} `json:"prompt_tokens_details"`
				Input *struct {
					Audio *int `json:"audio_tokens"`
				} `json:"input_tokens_details"`
			} `json:"usage"`
		}
		if err := common.Unmarshal(raw, &details); err != nil {
			storage.Close()
			return fail(err)
		}
		audioDetails := details.Usage.Prompt
		if info.RelayFormat == types.RelayFormatOpenAIResponses {
			audioDetails = details.Usage.Input
		}
		if audioDetails == nil || audioDetails.Audio == nil {
			storage.Close()
			return fail(fmt.Errorf("Omni audio/video usage requires authoritative audio token breakdown"))
		}
		break
	}
	usage := envelope.Usage
	if usage == nil {
		storage.Close()
		return fail(fmt.Errorf("selected Omni response requires actual usage"))
	}
	if info.RelayFormat == types.RelayFormatOpenAIResponses {
		usage.PromptTokens = usage.InputTokens
		usage.CompletionTokens = usage.OutputTokens
		if usage.InputTokensDetails != nil {
			usage.PromptTokensDetails = *usage.InputTokensDetails
		}
		if usage.OutputTokensDetails != nil {
			usage.CompletionTokenDetails = *usage.OutputTokensDetails
		}
	}
	counts := []int{usage.PromptTokens, usage.CompletionTokens, usage.TotalTokens, usage.PromptTokensDetails.TextTokens, usage.PromptTokensDetails.AudioTokens, usage.PromptTokensDetails.ImageTokens, usage.PromptTokensDetails.VideoTokens, usage.PromptTokensDetails.CachedTokens, usage.PromptTokensDetails.CachedCreationTokens, usage.PromptTokensDetails.CacheWriteTokens, usage.CompletionTokenDetails.TextTokens, usage.CompletionTokenDetails.AudioTokens, usage.CompletionTokenDetails.ImageTokens, usage.CompletionTokenDetails.ReasoningTokens}
	for _, count := range counts {
		if count < 0 || int64(count) > math.MaxInt32 {
			storage.Close()
			return fail(fmt.Errorf("Omni usage count is outside supported bounds"))
		}
	}
	prompt := usage.PromptTokensDetails
	completion := usage.CompletionTokenDetails
	if usage.PromptTokens == 0 || usage.TotalTokens == 0 || int64(usage.PromptTokens)+int64(usage.CompletionTokens) != int64(usage.TotalTokens) || int64(prompt.TextTokens)+int64(prompt.AudioTokens)+int64(prompt.ImageTokens)+int64(prompt.VideoTokens) > int64(usage.PromptTokens) || prompt.CachedTokens > usage.PromptTokens || int64(completion.TextTokens)+int64(completion.AudioTokens)+int64(completion.ImageTokens)+int64(completion.ReasoningTokens) > int64(usage.CompletionTokens) {
		storage.Close()
		return fail(fmt.Errorf("inconsistent Omni actual usage"))
	}
	// This selected path emits text only; undeclared generated media cannot be
	// priced by the understanding contract.
	if completion.AudioTokens != 0 || completion.ImageTokens != 0 {
		storage.Close()
		return fail(fmt.Errorf("Omni understanding returned unsupported media output usage"))
	}
	reader, err := storage.NewReader()
	if err != nil {
		storage.Close()
		return fail(err)
	}
	resp.Body = reader
	return storage, nil
}
