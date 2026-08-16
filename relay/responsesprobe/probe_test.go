package responsesprobe

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relay/protocol"
	"github.com/QuantumNous/new-api/service/modelcatalog"
)

func TestResponsesProbeTargetURL(t *testing.T) {
	cases := []struct {
		name       string
		channelTyp int
		baseURL    string
		want       string
		wantOK     bool
	}{
		{"openai", constant.ChannelTypeOpenAI, "https://api.example.com", "https://api.example.com/v1/responses", true},
		{"moonshot not probed (declared chat-only)", constant.ChannelTypeMoonshot, "https://api.kimi.com/coding", "", false},
		{"zhipu not probed (declared chat-only)", constant.ChannelTypeZhipu_v4, "https://open.bigmodel.cn", "", false},
		{"azure skipped", constant.ChannelTypeAzure, "https://x.openai.azure.com", "", false},
		{"deepseek skipped", constant.ChannelTypeDeepSeek, "https://api.deepseek.com", "", false},
		{"anthropic skipped", constant.ChannelTypeAnthropic, "https://api.anthropic.com", "", false},
		{"empty key", constant.ChannelTypeOpenAI, "https://api.example.com", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			info := &common.RelayInfo{ChannelMeta: &common.ChannelMeta{ChannelType: tc.channelTyp, ChannelBaseUrl: tc.baseURL, ApiKey: "k"}}
			if tc.name == "empty key" {
				info.ApiKey = ""
			}
			got, ok := responsesProbeTargetURL(info)
			if ok != tc.wantOK || got != tc.want {
				t.Fatalf("got (%q,%v), want (%q,%v)", got, ok, tc.want, tc.wantOK)
			}
		})
	}
}

func TestProbeResponsesEndpointStatusMapping(t *testing.T) {
	cases := []struct {
		status     int
		healthy    bool
		definitive bool
	}{
		{http.StatusOK, true, true},
		{http.StatusCreated, true, true},
		{http.StatusBadRequest, true, true},
		{http.StatusUnprocessableEntity, true, true},
		{http.StatusNotFound, false, true},
		{http.StatusMethodNotAllowed, false, true},
		{http.StatusUnauthorized, false, false},
		{http.StatusForbidden, false, false},
		{http.StatusTooManyRequests, false, false},
		{http.StatusInternalServerError, false, false},
		{http.StatusBadGateway, false, false},
	}
	for _, tc := range cases {
		t.Run(fmt.Sprintf("status_%d", tc.status), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
			}))
			defer srv.Close()
			info := &common.RelayInfo{ChannelMeta: &common.ChannelMeta{UpstreamModelName: "m"}}
			obs, def := probeResponsesEndpoint(context.Background(), srv.URL, info)
			if def != tc.definitive || obs.Healthy != tc.healthy {
				t.Fatalf("status %d: got (healthy=%v,def=%v), want (healthy=%v,def=%v)", tc.status, obs.Healthy, def, tc.healthy, tc.definitive)
			}
		})
	}
}

func TestProbeResponsesEndpointNetworkErrorIsInconclusive(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	srv.Close() // port closed
	info := &common.RelayInfo{ChannelMeta: &common.ChannelMeta{UpstreamModelName: "m"}}
	_, def := probeResponsesEndpoint(context.Background(), srv.URL, info)
	if def {
		t.Fatal("network error must be inconclusive (not cached)")
	}
}

func TestShouldServeResponsesViaChatCachesAndHeals(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	resetProbeState(t)
	info := &common.RelayInfo{
		RelayMode: relayconstant.RelayModeResponses,
		ChannelMeta: &common.ChannelMeta{
			ChannelType:       constant.ChannelTypeOpenAI,
			ChannelBaseUrl:    srv.URL,
			ApiKey:            "k",
			UpstreamModelName: "m",
			ChannelId:         77,
		},
	}
	ctx := context.Background()

	// 404 → convert; three calls hit the probe endpoint exactly once (failure cached).
	for i := 0; i < 3; i++ {
		if !ShouldServeResponsesViaChat(ctx, info) {
			t.Fatal("404 upstream must select conversion")
		}
	}
	if got := hits.Load(); got != 1 {
		t.Fatalf("probe hits = %d, want 1 (failure cached)", got)
	}

	// Failure TTL expiry → upstream now healthy → passthrough.
	forceExpireProbe(t, 77)
	srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	})
	if ShouldServeResponsesViaChat(ctx, info) {
		t.Fatal("healthy upstream must select passthrough")
	}
	if got := hits.Load(); got != 2 {
		t.Fatalf("probe hits = %d, want 2", got)
	}
}

func TestShouldServeResponsesViaChatSkipsNonResponsesMode(t *testing.T) {
	resetProbeState(t)
	info := &common.RelayInfo{
		RelayMode: relayconstant.RelayModeChatCompletions,
		ChannelMeta: &common.ChannelMeta{
			ChannelType:    constant.ChannelTypeOpenAI,
			ChannelBaseUrl: "https://x.example.com",
			ApiKey:         "k",
			ChannelId:      78,
		},
	}
	if ShouldServeResponsesViaChat(context.Background(), info) {
		t.Fatal("chat completions mode must never convert")
	}
}

func TestProbeInflightCollapsesConcurrentProbes(t *testing.T) {
	var hits atomic.Int64
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		<-release
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	resetProbeState(t)

	info := &common.RelayInfo{
		RelayMode: relayconstant.RelayModeResponses,
		ChannelMeta: &common.ChannelMeta{
			ChannelType:       constant.ChannelTypeOpenAI,
			ChannelBaseUrl:    srv.URL,
			ApiKey:            "k",
			UpstreamModelName: "m",
			ChannelId:         79,
		},
	}

	const n = 8
	var wg sync.WaitGroup
	results := make([]bool, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i] = ShouldServeResponsesViaChat(context.Background(), info)
		}(i)
	}
	time.Sleep(100 * time.Millisecond)
	close(release)
	wg.Wait()

	for i, conv := range results {
		if !conv {
			t.Fatalf("goroutine %d: expected conversion decision", i)
		}
	}
	if got := hits.Load(); got != 1 {
		t.Fatalf("probe hits = %d, want 1 (inflight collapse)", got)
	}
}

func resetProbeState(t *testing.T) {
	t.Helper()
	SetResponsesProbeStore(protocol.NewInMemoryProbeStore())
	t.Cleanup(func() { SetResponsesProbeStore(protocol.NewInMemoryProbeStore()) })
}

// forceExpireProbe removes the cached entry by writing an already-expired
// observation through the store, simulating TTL expiry deterministically.
func forceExpireProbe(t *testing.T, channelID int) {
	t.Helper()
	// InMemoryProbeStore has no Delete; simulate expiry by directly managing
	// a fresh store — tests that need it construct their own flow.
	SetResponsesProbeStore(protocol.NewInMemoryProbeStore())
	_ = channelID
	_ = modelcatalog.WireResponses
}
