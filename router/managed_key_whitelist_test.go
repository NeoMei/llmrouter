package router

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/QuantumNous/new-api/internal/platformbilling"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// These are the POST paths the dispatcher maps explicitly in
// router/managed_key.go. The contract is an explicit set, not a wildcard:
// anything absent from the branch chain falls through to the 501
// unified_operation_unsupported rejection (target == "").
// This table is a documented expectation of the current implementation;
// router/managed_key.go is not modified by this test.
var managedKeyPOSTWhitelist = []string{
	"/v1/native/dashscope/images/generations",
	"/v1/native/dashscope/ocr/recognitions",
	"/v1/native/bailian/audio/speech",
	"/v1/native/elevenlabs/audio/speech",
	"/v1/rerank",
	"/v1/multimodal/embeddings",
	"/api/v1/services/rerank/text-rerank/text-rerank",
	"/api/v1/services/embeddings/multimodal-embedding/multimodal-embedding",
	"/v1/messages",
	"/v1/chat/completions",
	"/v1/responses",
	"/v1/responses/compact",
	"/v1/embeddings",
	"/v1/audio/speech",
	"/v1/audio/transcriptions",
	"/v1/images/generations",
	"/v1/images/edits",
	"/v1/videos",
	"/api/v2/ocr/jobs",
	"/v1/ocr",
}

// Representative operations that are deliberately left outside the unified-key
// allowlist. They are rejected before any platform/native billing work.
var managedKeyUnsupportedPOSTPaths = []string{
	"/v1/completions",
	"/v1/audio/translations",
	"/v1/images/variations",
	"/v1/moderations",
	"/v1/realtime",
	"/v1/tools/search",
	"/v1/tools/fetch",
}

// newManagedKeyBoundaryEngine builds the same dispatcher as production using
// only in-process fixtures: a temporary SQLite DB, a synthetic UniHub origin
// served over httptest, and gin's recorder. No real network, gateway database
// or production configuration is used.
func newManagedKeyBoundaryEngine(t *testing.T) (*gin.Engine, *atomic.Int64) {
	t.Helper()
	enableUniHubBoundary(t)
	var hubRequests atomic.Int64
	hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hubRequests.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(hub.Close)
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "unified-key-boundary.db")), &gorm.Config{})
	require.NoError(t, err)
	adapter, err := platformbilling.New(db, platformbilling.Config{
		Enabled: true, Origin: hub.URL, ServiceToken: "synthetic-boundary-service-token",
		PrincipalID: 1, Group: "platform", MaxInputTokens: 1000, MaxOutputTokens: 100, MaxBodyBytes: 8192,
	})
	require.NoError(t, err)
	engine := gin.New()
	SetManagedKeyDispatch(engine, adapter)
	return engine, &hubRequests
}

func sendManagedKeyPOST(t *testing.T, engine *gin.Engine, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, http.NoBody)
	req.Header.Set("Authorization", "Bearer uh_key_boundary")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	engine.ServeHTTP(w, req)
	return w
}

func TestManagedKeyAllowlistIsAnExplicitSet(t *testing.T) {
	// Assert the contract table itself is a finite, de-duplicated explicit set.
	// The production dispatcher has no default allow-all branch: an unmapped
	// path must hit target == "" and be rejected with 501.
	seen := map[string]bool{}
	for _, path := range managedKeyPOSTWhitelist {
		require.False(t, seen[path], "duplicate allowlist entry %q", path)
		seen[path] = true
		assert.Equal(t, byte('/'), path[0], "allowlist entries must be absolute paths")
	}
	require.Len(t, managedKeyPOSTWhitelist, 20, "allowlist is a fixed explicit set; update this test only when the dispatcher's POST switch changes")
	for _, path := range managedKeyUnsupportedPOSTPaths {
		assert.False(t, seen[path], "unsupported path %q must not be in the allowlist", path)
	}
}

func TestManagedKeyUnsupportedOperationsReturn501(t *testing.T) {
	engine, hubRequests := newManagedKeyBoundaryEngine(t)
	for _, path := range managedKeyUnsupportedPOSTPaths {
		t.Run(path, func(t *testing.T) {
			w := sendManagedKeyPOST(t, engine, path)
			require.Equal(t, http.StatusNotImplemented, w.Code, w.Body.String())
			assert.Contains(t, w.Body.String(), "unified_operation_unsupported", w.Body.String())
			assert.Contains(t, w.Body.String(), "this operation is not available for unified API keys", w.Body.String())
		})
	}
	// A non-allowlisted representative path must be rejected before any
	// platform-billing admission (no remote authorization calls).
	assert.Zero(t, hubRequests.Load(), "501 boundary must precede billing authority")
}

func TestManagedKeyAllowlistedOperationIsNotRejectedBy501(t *testing.T) {
	engine, _ := newManagedKeyBoundaryEngine(t)
	// /v1/chat/completions is in the explicit allowlist. It must not be turned
	// away by the target == "" / unified_operation_unsupported branch. Downstream
	// admission uses a synthetic origin that returns 503, so the assertion is
	// limited to "not the 501 branch" and does not claim a successful relay.
	w := sendManagedKeyPOST(t, engine, "/v1/chat/completions")
	assert.NotEqual(t, http.StatusNotImplemented, w.Code, w.Body.String())
	assert.NotContains(t, w.Body.String(), "unified_operation_unsupported", w.Body.String())
}
