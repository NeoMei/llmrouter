package controller

import (
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/service/authz"
	"github.com/QuantumNous/new-api/service/modelcatalog"
	"github.com/gin-gonic/gin"
)

const routeDecisionTraceTTL = 5 * time.Minute
const routeDecisionMaxEntries = 10000

type traceEntry struct {
	Trace      modelcatalog.RouteDecisionTrace
	RecordedAt time.Time
}

var (
	traceStoreMu sync.RWMutex
	traceStore   = make(map[string]traceEntry)
)

// BuildRouteDecisionTrace builds a bounded trace from resolved model metadata.
// It intentionally omits secrets such as API keys, headers, and authorization.
func BuildRouteDecisionTrace(relayInfo *relaycommon.RelayInfo) modelcatalog.RouteDecisionTrace {
	if relayInfo == nil {
		return modelcatalog.RouteDecisionTrace{}
	}
	return modelcatalog.RouteDecisionTrace{
		RequestedModel: relayInfo.OriginModelName,
		PublicModel:    relayInfo.PublicModelName,
		ResolvedModel:  relayInfo.ResolvedModelName,
		SelectedWire:   relayInfo.SelectedWire,
		RouteID:        relayInfo.ModelRouteId,
		TargetID:       relayInfo.ModelTargetId,
		ChannelID:      relayInfo.ResolvedChannelId,
	}
}

// UpdateRouteDecisionSelection refreshes the top-level identity after an
// ordered fallback selects another authoritative route target.
func UpdateRouteDecisionSelection(requestID string, relayInfo *relaycommon.RelayInfo) bool {
	if requestID == "" || relayInfo == nil {
		return false
	}
	traceStoreMu.Lock()
	defer traceStoreMu.Unlock()
	entry, ok := traceStore[requestID]
	if !ok {
		return false
	}
	entry.Trace.PublicModel = relayInfo.PublicModelName
	entry.Trace.ResolvedModel = relayInfo.ResolvedModelName
	entry.Trace.SelectedWire = relayInfo.SelectedWire
	entry.Trace.RouteID = relayInfo.ModelRouteId
	entry.Trace.TargetID = relayInfo.ModelTargetId
	entry.Trace.ChannelID = relayInfo.ResolvedChannelId
	entry.RecordedAt = time.Now()
	traceStore[requestID] = entry
	return true
}

// StartRouteDecision records the initial trace for a request ID.
func StartRouteDecision(requestID string, relayInfo *relaycommon.RelayInfo) {
	if requestID == "" || relayInfo == nil {
		return
	}
	traceStoreMu.Lock()
	defer traceStoreMu.Unlock()
	traceStore[requestID] = traceEntry{
		Trace:      BuildRouteDecisionTrace(relayInfo),
		RecordedAt: time.Now(),
	}
	cleanupTraceStoreLocked()
}

// RecordRouteDecision stores a bounded route decision trace keyed by request ID.
func RecordRouteDecision(requestID string, trace modelcatalog.RouteDecisionTrace) {
	if requestID == "" {
		return
	}
	traceStoreMu.Lock()
	defer traceStoreMu.Unlock()
	traceStore[requestID] = traceEntry{
		Trace:      cloneRouteDecisionTrace(trace),
		RecordedAt: time.Now(),
	}
	cleanupTraceStoreLocked()
}

// AppendRouteAttempt appends a single attempt to an existing trace.
func AppendRouteAttempt(requestID string, attempt modelcatalog.Attempt) {
	if requestID == "" {
		return
	}
	traceStoreMu.Lock()
	defer traceStoreMu.Unlock()
	entry, ok := traceStore[requestID]
	if !ok {
		entry = traceEntry{Trace: modelcatalog.RouteDecisionTrace{}, RecordedAt: time.Now()}
	}
	entry.Trace.Attempts = append(entry.Trace.Attempts, attempt)
	entry.RecordedAt = time.Now()
	traceStore[requestID] = entry
}

// GetRouteDecision returns the trace for a request ID, if present.
func GetRouteDecision(requestID string) (modelcatalog.RouteDecisionTrace, bool) {
	traceStoreMu.RLock()
	defer traceStoreMu.RUnlock()
	entry, ok := traceStore[requestID]
	if !ok {
		return modelcatalog.RouteDecisionTrace{}, false
	}
	if time.Since(entry.RecordedAt) > routeDecisionTraceTTL {
		return modelcatalog.RouteDecisionTrace{}, false
	}
	return cloneRouteDecisionTrace(entry.Trace), true
}

// GetRouteDecisionTrace returns the trace for a request ID as raw JSON.
func GetRouteDecisionTrace(requestID string) (json.RawMessage, bool) {
	trace, ok := GetRouteDecision(requestID)
	if !ok {
		return nil, false
	}
	data, err := common.Marshal(trace)
	if err != nil {
		return nil, false
	}
	return data, true
}

func cloneRouteDecisionTrace(trace modelcatalog.RouteDecisionTrace) modelcatalog.RouteDecisionTrace {
	trace.Attempts = append([]modelcatalog.Attempt(nil), trace.Attempts...)
	return trace
}

func GetRouteDecisionEndpoint(c *gin.Context) {
	requestID := c.Param("request_id")
	if requestID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "request_id is required"})
		return
	}
	trace, ok := GetRouteDecision(requestID)
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"success": false, "message": "route decision not found"})
		return
	}
	if _, projected := authz.ProductPrincipal(c); projected {
		for i := range trace.Attempts {
			trace.Attempts[i].Reason = ""
			trace.Attempts[i].RejectedReason = ""
		}
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": trace})
}

// GetRequestId returns the request ID for the current context.
func GetRequestId(c *gin.Context) string {
	if c == nil {
		return ""
	}
	return c.GetString(common.RequestIdKey)
}

func cleanupTraceStoreLocked() {
	if len(traceStore) == 0 {
		return
	}
	cutoff := time.Now().Add(-routeDecisionTraceTTL)
	for id, entry := range traceStore {
		if entry.RecordedAt.Before(cutoff) {
			delete(traceStore, id)
		}
	}
	if len(traceStore) > routeDecisionMaxEntries {
		// Remove oldest entries until we are back under the limit.
		type kv struct {
			id       string
			recorded time.Time
		}
		entries := make([]kv, 0, len(traceStore))
		for id, entry := range traceStore {
			entries = append(entries, kv{id: id, recorded: entry.RecordedAt})
		}
		// Sort oldest first.
		for i := 0; i < len(entries)-1; i++ {
			for j := i + 1; j < len(entries); j++ {
				if entries[j].recorded.Before(entries[i].recorded) {
					entries[i], entries[j] = entries[j], entries[i]
				}
			}
		}
		toRemove := len(entries) - routeDecisionMaxEntries
		for i := 0; i < toRemove; i++ {
			delete(traceStore, entries[i].id)
		}
	}
}

// UpdateLastAttempt updates the status and reason of the most recent attempt.
func UpdateLastAttempt(requestID string, status int, reason string) bool {
	if requestID == "" {
		return false
	}
	traceStoreMu.Lock()
	defer traceStoreMu.Unlock()
	entry, ok := traceStore[requestID]
	if !ok {
		return false
	}
	if len(entry.Trace.Attempts) == 0 {
		return false
	}
	last := &entry.Trace.Attempts[len(entry.Trace.Attempts)-1]
	last.Status = status
	last.Reason = reason
	traceStore[requestID] = entry
	return true
}
