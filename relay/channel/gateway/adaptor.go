package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"regexp"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relay/channel"
	"github.com/QuantumNous/new-api/relay/channel/openai"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/profile"
	quirkpkg "github.com/QuantumNous/new-api/relay/quirk"
	"github.com/QuantumNous/new-api/relay/toolpolicy"
	"github.com/QuantumNous/new-api/service/accountpool"
	"github.com/QuantumNous/new-api/service/relayconvert"
	"github.com/QuantumNous/new-api/types"

	"github.com/gin-gonic/gin"
)

// Adaptor 装饰 openai.Adaptor，注入 quirk + profile + key 选择。
// 所有请求统一路由到 profile.BaseURL + /v1/chat/completions。
type Adaptor struct {
	Inner  channel.Adaptor // 通常是 *openai.Adaptor
	Loader *profile.Loader
	KeySel *KeySelector
}

func (a *Adaptor) Init(info *relaycommon.RelayInfo) { a.Inner.Init(info) }

// ─── 请求转换 ───

func (a *Adaptor) ConvertOpenAIRequest(c *gin.Context, info *relaycommon.RelayInfo, req *dto.GeneralOpenAIRequest) (any, error) {
	if a.Loader == nil {
		return a.Inner.ConvertOpenAIRequest(c, info, req)
	}
	p, err := a.Loader.Get(c.Request.Context(), info.UpstreamModelName)
	if err != nil {
		return nil, err
	}
	quirkpkg.NeutralizeIdentity(req)
	quirkpkg.GuardEmptyAssistant(req)
	profile.ApplyRules(req, p)
	return a.Inner.ConvertOpenAIRequest(c, info, req)
}

func (a *Adaptor) ConvertOpenAIResponsesRequest(c *gin.Context, info *relaycommon.RelayInfo, req dto.OpenAIResponsesRequest) (any, error) {
	// Convert Responses → Chat Completions format so we can apply quirks
	// and send to providers that only support /v1/chat/completions.
	result, err := relayconvert.ConvertRequest(c, info, types.RelayFormatOpenAI, &req)
	if err != nil {
		// fallback: let inner adaptor handle as-is
		return a.Inner.ConvertOpenAIResponsesRequest(c, info, req)
	}
	openaiReq, ok := result.Value.(*dto.GeneralOpenAIRequest)
	if !ok {
		return a.Inner.ConvertOpenAIResponsesRequest(c, info, req)
	}
	if a.Loader != nil {
		// Apply quirk + profile on the Chat Completions format request.
		p, pErr := a.Loader.Get(c.Request.Context(), info.UpstreamModelName)
		quirkpkg.NeutralizeIdentity(openaiReq)
		quirkpkg.GuardEmptyAssistant(openaiReq)
		if pErr == nil && p != nil {
			profile.ApplyRules(openaiReq, p)
		}
	}
	// Mark that response needs Chat→Responses conversion
	c.Set("gateway_responses_via_chat", true)
	return a.Inner.ConvertOpenAIRequest(c, info, openaiReq)
}

func (a *Adaptor) ConvertClaudeRequest(c *gin.Context, info *relaycommon.RelayInfo, req *dto.ClaudeRequest) (any, error) {
	result, err := a.Inner.ConvertClaudeRequest(c, info, req)
	if err != nil {
		return nil, err
	}
	openaiReq, ok := result.(*dto.GeneralOpenAIRequest)
	if !ok {
		return result, nil
	}
	if a.Loader == nil {
		return openaiReq, nil
	}
	p, pErr := a.Loader.Get(c.Request.Context(), info.UpstreamModelName)
	quirkpkg.NeutralizeIdentity(openaiReq)
	quirkpkg.GuardEmptyAssistant(openaiReq)
	if pErr == nil && p != nil {
		profile.ApplyRules(openaiReq, p)
	}
	return openaiReq, nil
}

// ─── URL 构建 ───

func (a *Adaptor) GetRequestURL(info *relaycommon.RelayInfo) (string, error) {
	if a.Loader == nil {
		return a.Inner.GetRequestURL(info)
	}
	p, err := a.Loader.Get(context.Background(), info.UpstreamModelName)
	if err != nil {
		return "", err
	}
	if p.QuirkRules.BracketStrip {
		info.UpstreamModelName = stripBracketSuffix(info.UpstreamModelName)
	}
	// ponytail: base URLs already include version path (e.g. /v1, /v4, /coding/v1).
	// Append /chat/completions, not /v1/chat/completions — matches opencodex behavior.
	return p.BaseURL + "/chat/completions", nil
}

// ─── Header 设置（BYOK / 平台 key）───

func (a *Adaptor) SetupRequestHeader(c *gin.Context, header *http.Header, info *relaycommon.RelayInfo) error {
	if err := a.Inner.SetupRequestHeader(c, header, info); err != nil {
		return err
	}
	if a.KeySel == nil || a.Loader == nil {
		return nil
	}

	ctx := c.Request.Context()
	p, err := a.Loader.Get(ctx, info.UpstreamModelName)
	if err != nil {
		return err
	}
	profileID := p.ProfileID

	// BYOK ownership is sticky across attempts, including provider rejection.
	byokKey, err := a.KeySel.GetBYOKKey(ctx, info.UserId, profileID)
	if err != nil {
		accountpool.State(c).Veto = true
		return err
	}
	if byokKey != "" {
		if c.GetBool("gateway_byok_sent") {
			return fmt.Errorf("BYOK request already attempted")
		}
		accountpool.ResetSelection(c)
		header.Set("Authorization", "Bearer "+byokKey)
		c.Set("key_source", "byok")
		c.Set("gateway_byok_sent", true)
		return nil
	}
	scope := accountpool.RequestScope(c, info.UpstreamModelName)
	state := accountpool.State(c)
	refs, keys, owners, err := a.KeySel.PlatformAccounts(ctx, profileID, info.ChannelId)
	if err != nil {
		return err
	}
	var selected model.UpstreamAccountSelection
	prepared := false
	if state.Selected != nil && !state.Excluded[state.Selected.Ref.MemberID] && state.Scope == scope {
		for _, ref := range refs {
			if ref.MemberID == state.Selected.Ref.MemberID {
				selected = *state.Selected
				prepared = true
				break
			}
		}
	}
	if !prepared {
		selected, err = model.SelectUpstreamAccount(ctx, a.KeySel.db, refs, scope, state.Excluded, time.Now())
		if err != nil {
			accountpool.SetUnavailableHeader(c, err)
			return types.NewErrorWithStatusCode(err, types.ErrorCodeChannelNoAvailableKey, http.StatusServiceUnavailable, types.ErrOptionWithSkipRetry())
		}
	}
	accountpool.Prepare(c, a.KeySel.db, selected, scope)
	state.Candidates = refs
	owner := owners[selected.KeyIndex]
	state.Owner = &owner
	header.Set("Authorization", "Bearer "+keys[selected.KeyIndex])
	c.Set("key_source", "platform")
	c.Set("pool_key_id", selected.KeyIndex)
	return nil
}

// ─── 响应处理 ───

func (a *Adaptor) DoResponse(c *gin.Context, resp *http.Response, info *relaycommon.RelayInfo) (usage any, err *types.NewAPIError) {
	// When we converted Responses→Chat for the upstream, the response comes back
	// in Chat Completions format but the client expects Responses format.
	// Route through the Chat→Responses conversion handler.
	if c.GetBool("gateway_responses_via_chat") {
		if info.IsStream {
			return openai.OaiChatToResponsesStreamHandler(c, info, resp)
		}
		return openai.OaiChatToResponsesHandler(c, info, resp)
	}
	return a.Inner.DoResponse(c, resp, info)
}

// ─── 纯委托方法 ───

func (a *Adaptor) DoRequest(c *gin.Context, info *relaycommon.RelayInfo, body io.Reader) (any, error) {
	// CRITICAL: must call DoApiRequest with `a` (self), not a.Inner.
	// DoApiRequest calls a.GetRequestURL() internally — if we delegate to Inner,
	// Inner's GetRequestURL uses the channel's empty base_url instead of our profile's base_url.
	return channel.DoApiRequest(a, c, info, body)
}
func (a *Adaptor) GetModelList() []string { return a.Inner.GetModelList() }
func (a *Adaptor) GetChannelName() string { return ChannelName }
func (a *Adaptor) ConvertGeminiRequest(c *gin.Context, info *relaycommon.RelayInfo, req *dto.GeminiChatRequest) (any, error) {
	return a.Inner.ConvertGeminiRequest(c, info, req)
}
func (a *Adaptor) ConvertImageRequest(c *gin.Context, info *relaycommon.RelayInfo, req dto.ImageRequest) (any, error) {
	return a.Inner.ConvertImageRequest(c, info, req)
}
func (a *Adaptor) ConvertAudioRequest(c *gin.Context, info *relaycommon.RelayInfo, req dto.AudioRequest) (io.Reader, error) {
	return a.Inner.ConvertAudioRequest(c, info, req)
}
func (a *Adaptor) ConvertRerankRequest(c *gin.Context, relayMode int, req dto.RerankRequest) (any, error) {
	return a.Inner.ConvertRerankRequest(c, relayMode, req)
}
func (a *Adaptor) ConvertEmbeddingRequest(c *gin.Context, info *relaycommon.RelayInfo, req dto.EmbeddingRequest) (any, error) {
	return a.Inner.ConvertEmbeddingRequest(c, info, req)
}

// RepairFinalChatRequest validates and repairs the exact post-override Gateway
// Chat body. Existing raw messages and unknown top-level fields are retained;
// only synthesized tool-result messages are newly encoded.
func RepairFinalChatRequest(c *gin.Context, body []byte) ([]byte, error) {
	var object map[string]json.RawMessage
	if err := common.UnmarshalUnique(body, &object); err != nil {
		return nil, fmt.Errorf("invalid final gateway chat request: %w", err)
	}
	rawMessages, exists := object["messages"]
	if !exists {
		return body, nil
	}
	if common.GetJsonType(rawMessages) != "array" {
		return nil, fmt.Errorf("invalid final gateway chat messages")
	}
	var messageFrames []json.RawMessage
	if err := common.UnmarshalUnique(rawMessages, &messageFrames); err != nil {
		return nil, fmt.Errorf("invalid final gateway chat messages: %w", err)
	}
	messages := make([]dto.Message, len(messageFrames))
	hasToolCalls := false
	for i, frame := range messageFrames {
		if err := common.UnmarshalUnique(frame, &messages[i]); err != nil {
			return nil, fmt.Errorf("invalid final gateway chat message %d: %w", i, err)
		}
		if containsToolCall(messages[i].ToolCalls) {
			hasToolCalls = true
		}
	}
	if !hasToolCalls {
		return body, nil
	}
	policy, ok := toolpolicy.FromContext(c)
	if !ok {
		return nil, fmt.Errorf("%w: gateway tool history requires a final request policy", toolpolicy.ErrMissingToolPolicy)
	}
	request := &dto.GeneralOpenAIRequest{Messages: messages}
	if err := quirkpkg.RepairDanglingToolCalls(request, policy); err != nil {
		return nil, err
	}
	if len(request.Messages) == len(messages) {
		return body, nil
	}

	repairedFrames := make([]json.RawMessage, 0, len(request.Messages))
	originalIndex := 0
	for _, message := range request.Messages {
		if originalIndex < len(messages) && reflect.DeepEqual(message, messages[originalIndex]) {
			repairedFrames = append(repairedFrames, messageFrames[originalIndex])
			originalIndex++
			continue
		}
		encoded, err := common.Marshal(message)
		if err != nil {
			return nil, fmt.Errorf("encode synthesized gateway tool result: %w", err)
		}
		repairedFrames = append(repairedFrames, encoded)
	}
	if originalIndex != len(messages) {
		return nil, fmt.Errorf("gateway tool repair lost an original message")
	}
	encodedMessages, err := common.Marshal(repairedFrames)
	if err != nil {
		return nil, fmt.Errorf("encode repaired gateway messages: %w", err)
	}
	object["messages"] = encodedMessages
	repairedBody, err := common.Marshal(object)
	if err != nil {
		return nil, fmt.Errorf("encode repaired gateway request: %w", err)
	}
	return repairedBody, nil
}

func containsToolCall(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return false
	}
	if common.GetJsonType(raw) != "array" {
		return true
	}
	var calls []json.RawMessage
	if err := common.UnmarshalUnique(raw, &calls); err != nil {
		return true
	}
	return len(calls) > 0
}

// ─── helpers ───

var bracketSuffixRe = regexp.MustCompile(`\[[^\]]*\]\s*$`)

// stripBracketSuffix removes a trailing [xxx] suffix (e.g. model[xhigh] → model).
func stripBracketSuffix(modelID string) string {
	return bracketSuffixRe.ReplaceAllString(modelID, "")
}
