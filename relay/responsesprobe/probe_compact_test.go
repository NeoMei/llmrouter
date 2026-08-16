package responsesprobe

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/constant"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/stretchr/testify/require"
)

func TestDecideResponsesViaChatCompactMode(t *testing.T) {
	cases := []struct {
		name       string
		channelTyp int
		want       bool
	}{
		{"moonshot always converts", constant.ChannelTypeMoonshot, true},
		{"zhipu always converts", constant.ChannelTypeZhipu_v4, true},
		{"deepseek compact converts (no native compact)", constant.ChannelTypeDeepSeek, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resetProbeState(t)
			info := &relaycommon.RelayInfo{
				RelayMode:   relayconstant.RelayModeResponsesCompact,
				ChannelMeta: &relaycommon.ChannelMeta{ChannelType: tc.channelTyp, ChannelId: 91},
			}
			DecideResponsesViaChat(context.Background(), info)
			require.Equal(t, tc.want, info.ResponsesViaChat)
		})
	}
}

func TestDecideResponsesViaChatDeepSeekResponsesStaysNative(t *testing.T) {
	resetProbeState(t)
	info := &relaycommon.RelayInfo{
		RelayMode: relayconstant.RelayModeResponses,
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelType: constant.ChannelTypeDeepSeek,
			ChannelId:   92,
		},
	}
	DecideResponsesViaChat(context.Background(), info)
	require.False(t, info.ResponsesViaChat, "deepseek has native responses; plain responses must pass through")
}

func TestDecideResponsesViaChatCompactProbesCompactEndpoint(t *testing.T) {
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	resetProbeState(t)

	info := &relaycommon.RelayInfo{
		RelayMode: relayconstant.RelayModeResponsesCompact,
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelType:    constant.ChannelTypeOpenAI,
			ChannelBaseUrl: srv.URL,
			ApiKey:         "k",
			ChannelId:      93,
		},
	}
	DecideResponsesViaChat(context.Background(), info)
	require.True(t, info.ResponsesViaChat, "404 compact endpoint must select conversion")
	require.Equal(t, []string{"/v1/responses/compact"}, paths, "probe must target the compact endpoint")
}

func TestDecideResponsesViaChatCompactCachesPerEndpoint(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	resetProbeState(t)

	base := relaycommon.ChannelMeta{
		ChannelType:    constant.ChannelTypeOpenAI,
		ChannelBaseUrl: srv.URL,
		ApiKey:         "k",
		ChannelId:      94,
	}
	for i := 0; i < 3; i++ {
		info := &relaycommon.RelayInfo{RelayMode: relayconstant.RelayModeResponsesCompact, ChannelMeta: &base}
		DecideResponsesViaChat(context.Background(), info)
		require.True(t, info.ResponsesViaChat)
	}
	require.Equal(t, 1, hits, "compact probe must be cached")
}
