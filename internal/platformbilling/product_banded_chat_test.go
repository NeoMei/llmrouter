package platformbilling

import (
	"net/http"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/constant"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBandedProductChatBindsCertifiedTextAndRejectsExplicitCache(t *testing.T) {
	const wire = "chat.completions.create"
	const model = "public-model"
	const alias = "public-alias"
	const upstream = "upstream-model"
	const endpoint = "https://coding.dashscope.aliyuncs.com/v1/chat/completions"
	policy := TextInputPolicy{Version: "full_body_bytes_v1", ChannelID: 15, ChannelType: constant.ChannelTypeOpenAI,
		UpstreamModel: upstream, Endpoint: endpoint, MaxInputTokens: 400, ContextTokens: 500, MaxOutputTokens: 50,
		ProviderReference: "https://help.aliyun.com/zh/model-studio/coding-plan"}
	adapter := &Adapter{config: Config{MaxInputTokens: 400, MaxOutputTokens: 50, MaxBodyBytes: 1024,
		ProductCreditTextInputPolicies: map[string]map[string]TextInputPolicy{wire: {model: policy}}}}
	newSession := func(body string) *Session {
		return &Session{adapter: adapter, canonical: []byte(body), call: Call{Model: alias, Status: "ADMITTED", CreditScope: ProductCreditScope, WireOperation: wire}}
	}
	const body = `{"model":"public-alias","messages":[{"role":"user","content":"short"}],"stream":true,"max_tokens":10}`
	s := newSession(body)
	input, output, err := s.BindProductBandedChat(model, 15, constant.ChannelTypeOpenAI, upstream, endpoint, 400)
	require.NoError(t, err)
	outbound, err := s.GenerationBody(constant.ChannelTypeOpenAI, "openai", upstream)
	require.NoError(t, err)
	assert.Equal(t, 2*len(outbound), input)
	assert.Equal(t, 10, output)
	assert.Contains(t, string(outbound), `"include_usage":true`)
	request, err := http.NewRequest(http.MethodPost, endpoint, strings.NewReader(string(outbound)))
	require.NoError(t, err)
	assert.NoError(t, s.ValidateProductBandedChatOutbound(15, constant.ChannelTypeOpenAI, upstream, request, false))
	request.URL.Path = "/different"
	assert.Error(t, s.ValidateProductBandedChatOutbound(15, constant.ChannelTypeOpenAI, upstream, request, false))
	for _, tt := range []struct {
		name, body string
		channel    int
		tariffCap  int64
	}{
		{"explicit cache", `{"model":"public-alias","messages":[{"role":"user","content":[{"type":"text","text":"short","cache_control":{"type":"ephemeral"}}]}],"max_tokens":10}`, 15, 400},
		{"multimodal input", `{"model":"public-alias","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"https://example.invalid/image.png"}}]}],"max_tokens":10}`, 15, 400},
		{"wrong channel", body, 16, 400},
		{"rate does not cover route cap", body, 15, 300},
		{"actual request cap exceeded", strings.Replace(body, "short", strings.Repeat("x", 201), 1), 15, 400},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s := newSession(tt.body)
			_, _, err := s.BindProductBandedChat(model, tt.channel, constant.ChannelTypeOpenAI, upstream, endpoint, tt.tariffCap)
			assert.Error(t, err)
			assert.False(t, s.ProductBandedChat())
		})
	}
}
