package service

import (
	"net/http/httptest"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/billingexpr"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClaudeCacheOnlyUsageAccounting(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		name  string
		usage dto.ClaudeUsage
		want  int
	}{
		{"one-hour write", dto.ClaudeUsage{CacheCreationInputTokens: 20000, CacheCreation: &dto.ClaudeCacheCreationUsage{Ephemeral1hInputTokens: 20000}}, 100000},
		{"five-minute write", dto.ClaudeUsage{CacheCreationInputTokens: 100, CacheCreation: &dto.ClaudeCacheCreationUsage{Ephemeral5mInputTokens: 100}}, 313},
		{"cache read", dto.ClaudeUsage{CacheReadInputTokens: 100}, 25},
		{"no usage", dto.ClaudeUsage{}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			truncate(t)
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest("POST", "/v1/messages", nil)
			seedUser(t, 451, 200000)
			seedToken(t, 452, 451, "cache-only", 200000)
			require.NoError(t, model.DB.Create(&model.Channel{Id: 453, Name: "cache-only"}).Error)
			info := &relaycommon.RelayInfo{
				UserId: 451, TokenId: 452, TokenKey: "cache-only",
				OriginModelName: "claude-test", RelayFormat: types.RelayFormatClaude,
				ChannelMeta: &relaycommon.ChannelMeta{ChannelId: 453}, StartTime: time.Now(),
				ForcePreConsume: true, UserSetting: dto.UserSetting{BillingPreference: "wallet_only"},
				PriceData: types.PriceData{
					ModelRatio: 2.5, CompletionRatio: 5, CacheRatio: 0.1,
					CacheCreationRatio: 1.25, CacheCreation5mRatio: 1.25, CacheCreation1hRatio: 2,
					GroupRatioInfo: types.GroupRatioInfo{GroupRatio: 1},
				},
			}
			usage := &dto.Usage{UsageSemantic: "anthropic", BillingUsage: dto.NewClaudeMessagesBillingUsage(&tc.usage)}
			summary := calculateTextQuotaSummary(c, info, effectiveBillingUsage(usage))
			assert.Equal(t, tc.want, summary.Quota)
			require.Nil(t, PreConsumeBilling(c, 1000, info))
			PostTextConsumeQuota(c, info, usage, nil)
			var user model.User
			var token model.Token
			var channel model.Channel
			require.NoError(t, model.DB.First(&user, 451).Error)
			require.NoError(t, model.DB.First(&token, 452).Error)
			require.NoError(t, model.DB.First(&channel, 453).Error)
			assert.Equal(t, 200000-tc.want, user.Quota)
			assert.Equal(t, tc.want, user.UsedQuota)
			assert.Equal(t, int64(tc.want), channel.UsedQuota)
			assert.Equal(t, tc.want, token.UsedQuota)
			assert.Equal(t, 200000-tc.want, token.RemainQuota)
			if tc.want > 0 {
				assert.Equal(t, 1, user.RequestCount)
			}
		})
	}
}

func TestBuildTieredTokenParamsClaudeUnsplitCacheWrites(t *testing.T) {
	const expr = `tier("base", p * 3 + c * 15 + cr * 0.3 + cc * 3.75 + cc1h * 6)`
	for _, tc := range []struct {
		name       string
		five, hour int
		wantFive   float64
		wantQuota  int
	}{
		{"total without split", 0, 0, 10000, 19275},
		{"partial split", 0, 4000, 6000, 23775},
	} {
		t.Run(tc.name, func(t *testing.T) {
			usage := &dto.Usage{PromptTokens: 100, CompletionTokens: 50, UsageSemantic: "anthropic", PromptTokensDetails: dto.InputTokenDetails{CachedCreationTokens: 10000}, ClaudeCacheCreation5mTokens: tc.five, ClaudeCacheCreation1hTokens: tc.hour}
			params := BuildTieredTokenParams(usage, true, billingexpr.UsedVars(expr))
			assert.Equal(t, tc.wantFive, params.CC)
			assert.Equal(t, float64(tc.hour), params.CC1h)
			assert.Equal(t, 10100.0, params.Len)
			ok, quota, _ := TryTieredSettle(makeRelayInfo(expr, 1, 100, 50), params)
			require.True(t, ok)
			// (100*3 + 50*15 + 5m*3.75 + 1h*6) * 500000/1000000.
			assert.Equal(t, tc.wantQuota, quota)
		})
	}
}
