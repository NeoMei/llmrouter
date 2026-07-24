package gateway

import (
	"context"
	"io"
	"net/http"
	"regexp"
	"time"

	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/relay/channel"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/profile"
	quirkpkg "github.com/QuantumNous/new-api/relay/quirk"
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
	p, err := a.Loader.Get(c.Request.Context(), info.UpstreamModelName)
	if err != nil {
		return nil, err
	}
	quirkpkg.NeutralizeIdentity(req)
	quirkpkg.RepairDanglingToolCalls(req)
	quirkpkg.GuardEmptyAssistant(req)
	profile.ApplyRules(req, p)
	return a.Inner.ConvertOpenAIRequest(c, info, req)
}

func (a *Adaptor) ConvertOpenAIResponsesRequest(c *gin.Context, info *relaycommon.RelayInfo, req dto.OpenAIResponsesRequest) (any, error) {
	// ponytail: Responses 格式的消息结构（json.RawMessage input/instructions）与
	// Chat Completions 不同，quirk/profile 的 ApplyRules 仅适配 GeneralOpenAIRequest。
	// 这里只加载 profile（供后续 URL/key 选择使用）， Responses 专用 quirk 待后续补齐。
	if a.Loader != nil {
		_, _ = a.Loader.Get(c.Request.Context(), info.UpstreamModelName)
	}
	return a.Inner.ConvertOpenAIResponsesRequest(c, info, req)
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
	p, pErr := a.Loader.Get(c.Request.Context(), info.UpstreamModelName)
	quirkpkg.NeutralizeIdentity(openaiReq)
	quirkpkg.RepairDanglingToolCalls(openaiReq)
	quirkpkg.GuardEmptyAssistant(openaiReq)
	if pErr == nil && p != nil {
		profile.ApplyRules(openaiReq, p)
	}
	return openaiReq, nil
}

// ─── URL 构建 ───

func (a *Adaptor) GetRequestURL(info *relaycommon.RelayInfo) (string, error) {
	p, err := a.Loader.Get(context.Background(), info.UpstreamModelName)
	if err != nil {
		return "", err
	}
	if p.QuirkRules.BracketStrip {
		info.UpstreamModelName = stripBracketSuffix(info.UpstreamModelName)
	}
	// 首批 provider 都是 Chat Completions 兼容，统一走 /v1/chat/completions
	return p.BaseURL + "/v1/chat/completions", nil
}

// ─── Header 设置（BYOK / 平台 key）───

func (a *Adaptor) SetupRequestHeader(c *gin.Context, header *http.Header, info *relaycommon.RelayInfo) error {
	if err := a.Inner.SetupRequestHeader(c, header, info); err != nil {
		return err
	}
	if a.KeySel == nil {
		return nil
	}

	ctx := c.Request.Context()
	p, err := a.Loader.Get(ctx, info.UpstreamModelName)
	if err != nil {
		return nil // profile 加载失败时保留 inner 设置的默认 key
	}
	profileID := p.ProfileID

	// BYOK 优先
	if byokKey, err := a.KeySel.GetBYOKKey(ctx, info.UserId, profileID); err == nil && byokKey != "" {
		header.Set("Authorization", "Bearer "+byokKey)
		c.Set("key_source", "byok")
		return nil
	}

	// 平台 key
	keyID, poolKey, err := a.KeySel.SelectPlatformKey(ctx, profileID)
	if err != nil {
		return err
	}
	header.Set("Authorization", "Bearer "+poolKey)
	c.Set("key_source", "platform")
	c.Set("pool_key_id", keyID)
	return nil
}

// ─── 响应处理 ───

func (a *Adaptor) DoResponse(c *gin.Context, resp *http.Response, info *relaycommon.RelayInfo) (usage any, err *types.NewAPIError) {
	if resp.StatusCode >= 400 {
		a.handleUpstreamError(c, resp)
	}
	return a.Inner.DoResponse(c, resp, info)
}

func (a *Adaptor) handleUpstreamError(c *gin.Context, resp *http.Response) {
	if a.KeySel == nil || c.GetString("key_source") != "platform" {
		return // BYOK 错误不归因到池
	}
	keyID := c.GetInt("pool_key_id")
	ctx := c.Request.Context()
	switch resp.StatusCode {
	case 429:
		a.KeySel.MarkCooldown(ctx, keyID, 5*time.Minute)
	case 401, 403:
		a.KeySel.MarkDisabled(ctx, keyID, "auth failed")
	}
}

// ─── 纯委托方法 ───

func (a *Adaptor) DoRequest(c *gin.Context, info *relaycommon.RelayInfo, body io.Reader) (any, error) {
	return a.Inner.DoRequest(c, info, body)
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

// ─── helpers ───

var bracketSuffixRe = regexp.MustCompile(`\[[^\]]*\]\s*$`)

// stripBracketSuffix removes a trailing [xxx] suffix (e.g. model[xhigh] → model).
func stripBracketSuffix(modelID string) string {
	return bracketSuffixRe.ReplaceAllString(modelID, "")
}
