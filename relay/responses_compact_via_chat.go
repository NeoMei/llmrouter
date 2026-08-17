package relay

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
)

// The compaction protocol mirrors codex-rs remote compaction v1 (see opencodex
// src/responses/compaction.ts for the reference implementation this port
// follows): a summarization turn on the chat wire, then a replacement-history
// output built from retained user messages plus the handoff summary.
const compactPrompt = `You are performing a CONTEXT CHECKPOINT COMPACTION. Create a handoff summary for another LLM that will resume the task.

Include:
- Current progress and key decisions made
- Important context, constraints, or user preferences
- What remains to be done (clear next steps)
- Any critical data, examples, or references needed to continue

Be concise, structured, and focused on helping the next LLM seamlessly continue the work.`

// compactSummaryPrefix mirrors codex-rs core/templates/compact/summary_prefix.md;
// codex detects stored summaries by this exact prefix, so keep it verbatim.
const compactSummaryPrefix = "Another language model started to solve this problem and produced a summary of its thinking process. You also have access to the state of the tools that were used by that language model. Use this to build on the work that has already been done and avoid duplicating work. Here is the summary produced by the other language model, use the information in this summary to assist with your own analysis:"

// codex-rs COMPACT_USER_MESSAGE_MAX_TOKENS = 20k tokens (~4 chars/token).
const compactV1RetainedCharBudget = 20000 * 4

const compactImagePlaceholder = "[image omitted for compaction]"

// buildCompactViaChatRequest rewrites a compaction request for a chat-only
// upstream: drop the tool surface and Codex-private items, replace images with
// text placeholders, and append the summarization instruction as the final user
// message. Ported from opencodex buildRoutedCompactionBody.
func buildCompactViaChatRequest(req *dto.OpenAIResponsesRequest) (*dto.GeneralOpenAIRequest, error) {
	if req == nil {
		return nil, fmt.Errorf("request is nil")
	}
	items, err := compactInputItems(req.Input)
	if err != nil {
		return nil, err
	}
	kept := make([]map[string]any, 0, len(items)+1)
	for _, item := range items {
		if item["type"] == "compaction_trigger" || item["type"] == "additional_tools" {
			continue
		}
		compactStripImages(item)
		kept = append(kept, item)
	}
	kept = append(kept, map[string]any{
		"type": "message",
		"role": "user",
		"content": []any{map[string]any{
			"type": "input_text",
			"text": compactPrompt,
		}},
	})

	inputJSON, err := common.Marshal(kept)
	if err != nil {
		return nil, err
	}
	filtered := &dto.OpenAIResponsesRequest{
		Model:        req.Model,
		Input:        inputJSON,
		Instructions: req.Instructions,
		Stream:       common.GetPointer(false),
	}
	return service.ResponsesRequestToChatCompletionsRequest(filtered)
}

// compactInputItems normalizes the responses input field to item maps. A plain
// string input is wrapped as a single user message; anything else is an error.
func compactInputItems(input []byte) ([]map[string]any, error) {
	trimmed := strings.TrimSpace(string(input))
	if trimmed == "" || trimmed == "null" {
		return nil, fmt.Errorf("compaction input is required")
	}
	if strings.HasPrefix(trimmed, "\"") {
		return []map[string]any{{
			"type":    "message",
			"role":    "user",
			"content": []any{map[string]any{"type": "input_text", "text": strings.Trim(trimmed, `"`)}},
		}}, nil
	}
	var raw []json.RawMessage
	if err := common.Unmarshal(input, &raw); err != nil {
		return nil, fmt.Errorf("compaction input must be an array or string: %w", err)
	}
	items := make([]map[string]any, 0, len(raw))
	for _, entry := range raw {
		var item map[string]any
		if err := common.Unmarshal(entry, &item); err != nil {
			return nil, err
		}
		if item == nil {
			continue
		}
		items = append(items, item)
	}
	return items, nil
}

// compactStripImages replaces image content parts in a message item with text
// placeholders so text-only upstreams accept the summarization turn.
func compactStripImages(item map[string]any) {
	content, ok := item["content"].([]any)
	if !ok {
		return
	}
	for i, partAny := range content {
		part, ok := partAny.(map[string]any)
		if !ok || part["type"] != "input_image" {
			continue
		}
		content[i] = map[string]any{"type": "input_text", "text": compactImagePlaceholder}
	}
}

// extractCompactUserMessages pulls plain-text user messages out of the original
// responses input (used for v1 replacement-history retention).
func extractCompactUserMessages(input []byte) []string {
	items, err := compactInputItems(input)
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		if t, ok := item["type"].(string); ok && t != "message" {
			continue
		}
		if role, _ := item["role"].(string); role != "user" {
			continue
		}
		var text string
		switch content := item["content"].(type) {
		case string:
			text = content
		case []any:
			var b strings.Builder
			for _, partAny := range content {
				part, ok := partAny.(map[string]any)
				if !ok {
					continue
				}
				if t, _ := part["type"].(string); t == "input_text" || t == "text" {
					if s, ok := part["text"].(string); ok {
						b.WriteString(s)
					}
				}
			}
			text = b.String()
		}
		if strings.TrimSpace(text) != "" {
			out = append(out, text)
		}
	}
	return out
}

// buildCompactV1Output builds the v1 compact output array: recent user messages
// retained within the char budget (most recent first, oldest partially), then a
// single summary message. Ported from opencodex buildCompactV1Output.
func buildCompactV1Output(userMessages []string, summary string) []map[string]any {
	selected := make([]string, 0, len(userMessages))
	remaining := compactV1RetainedCharBudget
	for i := len(userMessages) - 1; i >= 0 && remaining > 0; i-- {
		msg := userMessages[i]
		if len(msg) <= remaining {
			selected = append(selected, msg)
			remaining -= len(msg)
			continue
		}
		selected = append(selected, msg[len(msg)-remaining:])
		break
	}
	for i, j := 0, len(selected)-1; i < j; i, j = i+1, j-1 {
		selected[i], selected[j] = selected[j], selected[i]
	}
	summaryText := "(no summary available)"
	if strings.TrimSpace(summary) != "" {
		summaryText = compactSummaryPrefix + "\n" + summary
	}
	output := make([]map[string]any, 0, len(selected)+1)
	for _, msg := range selected {
		output = append(output, compactUserMessageItem(msg))
	}
	return append(output, compactUserMessageItem(summaryText))
}

func compactUserMessageItem(text string) map[string]any {
	return map[string]any{
		"type":    "message",
		"role":    "user",
		"content": []any{map[string]any{"type": "input_text", "text": text}},
	}
}

// compactChatDoRequester is the subset of channel.Adaptor needed to run the
// compaction turn (kept as an interface so tests can stub it).
type compactChatDoRequester interface {
	DoRequest(c *gin.Context, info *relaycommon.RelayInfo, requestBody io.Reader) (any, error)
}

// responsesCompactViaChat runs the compaction turn on a chat-only upstream and
// renders the v1 compaction response for the client.
func responsesCompactViaChat(c *gin.Context, info *relaycommon.RelayInfo, adaptor compactChatDoRequester, request *dto.OpenAIResponsesRequest) (*dto.Usage, *types.NewAPIError) {
	originalInput := request.Input
	chatReq, err := buildCompactViaChatRequest(request)
	if err != nil {
		return nil, types.NewError(err, types.ErrorCodeConvertRequestFailed, types.ErrOptionWithSkipRetry())
	}
	jsonData, err := common.Marshal(chatReq)
	if err != nil {
		return nil, types.NewError(err, types.ErrorCodeConvertRequestFailed, types.ErrOptionWithSkipRetry())
	}
	body, size, getBody, closer, err := relaycommon.NewOutboundJSONBody(jsonData)
	if err != nil {
		return nil, types.NewError(err, types.ErrorCodeConvertRequestFailed, types.ErrOptionWithSkipRetry())
	}
	defer closer.Close()
	info.UpstreamRequestBodySize = size
	info.UpstreamRequestGetBody = getBody

	respAny, err := adaptor.DoRequest(c, info, body)
	if err != nil {
		return nil, types.NewOpenAIError(err, types.ErrorCodeDoRequestFailed, http.StatusInternalServerError)
	}
	resp, ok := respAny.(*http.Response)
	if !ok || resp == nil {
		return nil, types.NewOpenAIError(fmt.Errorf("invalid upstream response"), types.ErrorCodeBadResponse, http.StatusInternalServerError)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, service.RelayErrorHandler(c.Request.Context(), resp, false)
	}
	return compactChatResponseHandler(c, info, resp, originalInput)
}

func compactChatResponseHandler(c *gin.Context, info *relaycommon.RelayInfo, resp *http.Response, originalInput []byte) (*dto.Usage, *types.NewAPIError) {
	defer service.CloseResponseBodyGracefully(resp)
	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, types.NewOpenAIError(err, types.ErrorCodeReadResponseBodyFailed, http.StatusInternalServerError)
	}
	var chatResp dto.OpenAITextResponse
	if err := common.Unmarshal(responseBody, &chatResp); err != nil {
		return nil, types.NewOpenAIError(err, types.ErrorCodeBadResponseBody, http.StatusInternalServerError)
	}
	if oaiError := chatResp.GetOpenAIError(); oaiError != nil && oaiError.Type != "" {
		return nil, types.WithOpenAIError(*oaiError, resp.StatusCode)
	}

	var summary string
	if len(chatResp.Choices) > 0 {
		summary = chatResp.Choices[0].Message.StringContent()
	}
	if strings.TrimSpace(summary) == "" {
		return nil, types.NewOpenAIError(fmt.Errorf("compaction turn produced an empty summary"), types.ErrorCodeBadResponseBody, http.StatusBadGateway, types.ErrOptionWithSkipRetry())
	}

	output := buildCompactV1Output(extractCompactUserMessages(originalInput), summary)
	id := chatResp.Id
	if id == "" {
		id = fmt.Sprintf("compact-%d", time.Now().UnixNano())
	}
	usage := &dto.Usage{
		PromptTokens:     chatResp.Usage.PromptTokens,
		CompletionTokens: chatResp.Usage.CompletionTokens,
		TotalTokens:      chatResp.Usage.TotalTokens,
	}
	if usage.TotalTokens == 0 {
		usage = service.ResponseText2Usage(c, summary, info.UpstreamModelName, info.GetEstimatePromptTokens())
	}

	payload := map[string]any{
		"id":         id,
		"object":     "response.compaction",
		"created_at": time.Now().Unix(),
		"output":     output,
		"usage": map[string]any{
			"input_tokens":  usage.PromptTokens,
			"output_tokens": usage.CompletionTokens,
			"total_tokens":  usage.TotalTokens,
		},
	}
	encoded, err := common.Marshal(payload)
	if err != nil {
		return nil, types.NewOpenAIError(err, types.ErrorCodeJsonMarshalFailed, http.StatusInternalServerError)
	}
	service.IOCopyBytesGracefully(c, resp, encoded)
	return usage, nil
}
