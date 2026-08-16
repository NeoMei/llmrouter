package responsesprobe

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relay/protocol"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/service/modelcatalog"
)

const responsesProbeTimeout = 5 * time.Second

var (
	probeStoreOnce sync.Once
	probeStoreInst protocol.ProbeStore
)

// ResponsesProbeStore returns the process-wide probe store. It is lazily
// initialized so tests can swap it before first use via SetResponsesProbeStore.
func ResponsesProbeStore() protocol.ProbeStore {
	probeStoreOnce.Do(func() {
		if probeStoreInst == nil {
			probeStoreInst = protocol.NewInMemoryProbeStore()
		}
	})
	return probeStoreInst
}

// SetResponsesProbeStore overrides the probe store (intended for tests and
// future Redis-backed implementations). It must be called before the first
// ResponsesProbeStore call.
func SetResponsesProbeStore(store protocol.ProbeStore) {
	probeStoreInst = store
}

// probeInflight collapses concurrent probes for the same key into one request.
type probeInflightGroup struct {
	mu       sync.Mutex
	inflight map[string]*probeCall
}

type probeCall struct {
	done chan struct{}
	obs  protocol.Observation
	def  bool
}

var probeInflightDefault = &probeInflightGroup{inflight: map[string]*probeCall{}}

func (g *probeInflightGroup) do(key string, fn func() (protocol.Observation, bool)) (protocol.Observation, bool) {
	g.mu.Lock()
	if call, ok := g.inflight[key]; ok {
		g.mu.Unlock()
		<-call.done
		return call.obs, call.def
	}
	call := &probeCall{done: make(chan struct{})}
	g.inflight[key] = call
	g.mu.Unlock()

	// Named returns are captured by the deferred cleanup, so waiters always
	// observe a closed done channel even if fn panics (they get the zero
	// value: inconclusive, i.e. passthrough — the safe default).
	var obs protocol.Observation
	var definitive bool
	defer func() {
		call.obs, call.def = obs, definitive
		g.mu.Lock()
		delete(g.inflight, key)
		g.mu.Unlock()
		close(call.done)
	}()
	obs, definitive = fn()
	return obs, definitive
}

// ShouldServeResponsesViaChat reports whether a /v1/responses request must be
// converted to chat completions for this channel: the upstream does not expose
// a native responses endpoint. The decision is cached per channel (success
// 24h, failure 10min) and self-heals via TTL. Probe errors are conservative:
// they never force conversion, preserving the passthrough status quo.
func ShouldServeResponsesViaChat(c context.Context, info *relaycommon.RelayInfo) bool {
	if info == nil || info.RelayMode != relayconstant.RelayModeResponses {
		return false
	}
	probeURL, ok := responsesProbeTargetURL(info)
	if !ok {
		return false
	}
	key := protocol.ProbeKey(info.ChannelId, modelcatalog.WireResponses)
	store := ResponsesProbeStore()
	if obs, found := store.Get(c, key); found {
		return !obs.Healthy
	}
	obs, definitive := probeInflightDefault.do(key, func() (protocol.Observation, bool) {
		// Re-check under the inflight slot: a concurrent probe may have
		// populated the store while we were waiting.
		if cached, found := store.Get(c, key); found {
			return cached, true
		}
		return probeResponsesEndpoint(c, probeURL, info)
	})
	if !definitive {
		return false
	}
	protocol.RecordProbe(store, info.ChannelId, modelcatalog.WireResponses, obs)
	return !obs.Healthy
}

// DecideResponsesViaChat sets info.ResponsesViaChat for a /v1/responses
// request based on channel knowledge and probe results:
//   - Moonshot / ZhipuV4 channels are declared chat-only upstreams and always
//     convert (no probe needed; explicit channel knowledge wins).
//   - OpenAI-compatible channels probe the upstream responses endpoint once
//     and cache the decision (native passthrough when supported).
//   - All other channel types keep their existing behavior (no flag set).
func DecideResponsesViaChat(ctx context.Context, info *relaycommon.RelayInfo) {
	if info == nil || info.RelayMode != relayconstant.RelayModeResponses {
		return
	}
	switch info.ChannelType {
	case constant.ChannelTypeMoonshot, constant.ChannelTypeZhipu_v4:
		info.ResponsesViaChat = true
		return
	}
	if info.ChannelType == constant.ChannelTypeAzure {
		return // Azure has its own responses URL scheme; out of scope.
	}
	if apiType, ok := common.ChannelType2APIType(info.ChannelType); ok && apiType == constant.APITypeOpenAI {
		info.ResponsesViaChat = ShouldServeResponsesViaChat(ctx, info)
	}
}

// responsesProbeTargetURL returns the upstream URL a native responses request
// would be sent to. Only channel types whose adaptor participates in automatic
// responses negotiation are probed; others keep their existing behavior.
func responsesProbeTargetURL(info *relaycommon.RelayInfo) (string, bool) {
	if strings.TrimSpace(info.ChannelBaseUrl) == "" || strings.TrimSpace(info.ApiKey) == "" {
		return "", false
	}
	if info.ChannelType == constant.ChannelTypeOpenAI {
		return relaycommon.GetFullRequestURL(info.ChannelBaseUrl, "/v1/responses", info.ChannelType), true
	}
	return "", false
}

// probeResponsesEndpoint sends a minimal responses request upstream and maps
// the status code to a capability observation. 2xx/400/422 means the endpoint
// exists (it parsed and answered in OpenAI error shape); 404/405 means it does
// not. Anything else (auth, rate limit, 5xx, network) is inconclusive and must
// not be cached.
func probeResponsesEndpoint(ctx context.Context, probeURL string, info *relaycommon.RelayInfo) (protocol.Observation, bool) {
	payload, err := common.Marshal(map[string]any{
		"model":             info.UpstreamModelName,
		"input":             "ping",
		"max_output_tokens": 1,
		"stream":            false,
	})
	if err != nil {
		return protocol.Observation{}, false
	}

	probeCtx, cancel := context.WithTimeout(ctx, responsesProbeTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(probeCtx, http.MethodPost, probeURL, bytes.NewReader(payload))
	if err != nil {
		return protocol.Observation{}, false
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+info.ApiKey)

	client := service.GetHttpClient()
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return protocol.Observation{}, false
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
		_ = resp.Body.Close()
	}()

	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300, resp.StatusCode == http.StatusBadRequest, resp.StatusCode == http.StatusUnprocessableEntity:
		return protocol.Observation{Healthy: true}, true
	case resp.StatusCode == http.StatusNotFound, resp.StatusCode == http.StatusMethodNotAllowed:
		return protocol.Observation{Healthy: false, Reason: fmt.Sprintf("upstream responses probe returned %d", resp.StatusCode)}, true
	default:
		return protocol.Observation{}, false
	}
}

// ConvertResponsesRequestToChat converts a responses request into a chat
// completions request using the shared converter, preserving the passthrough
// error contract on failure.
func ConvertResponsesRequestToChat(request dto.OpenAIResponsesRequest) (*dto.GeneralOpenAIRequest, error) {
	return service.ResponsesRequestToChatCompletionsRequest(&request)
}
