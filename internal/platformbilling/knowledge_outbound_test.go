package platformbilling

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestKnowledgeOutboundMatchesReservedRequest(t *testing.T) {
	for _, tc := range []struct{ op, body, altered string }{
		{"embedding", `{"model":"upstream-embed","input":["first","second"],"dimensions":3}`, `{"model":"upstream-embed","input":["foreign","second"],"dimensions":3}`},
		{"rerank", `{"model":"qwen3-rerank","input":{"query":"q","documents":["first","second"]},"parameters":{"top_n":1,"return_documents":true}}`, `{"model":"qwen3-rerank","input":{"query":"q","documents":["foreign","second"]},"parameters":{"top_n":1,"return_documents":true}}`},
	} {
		t.Run(tc.op, func(t *testing.T) {
			s, key, upstream := responseFixture(t, tc.op)
			makeReq := func(body string) *http.Request {
				req, err := http.NewRequest(http.MethodPost, "https://fixture.test/endpoint", bytes.NewBufferString(body))
				require.NoError(t, err)
				req.Header.Set("Authorization", "Bearer "+key)
				return req
			}
			validated := makeReq(tc.body)
			validated.Header.Set("Idempotency-Key", "secret-client-id")
			validated.Header["x-idempotency-key"] = []string{"alternate-id"}
			require.NoError(t, s.validateKnowledgeOutbound(1, upstream, key, validated))
			assert.Nil(t, validated.GetBody)
			assert.Empty(t, validated.Header.Get("Idempotency-Key"))
			assert.NotContains(t, validated.Header, "x-idempotency-key")
			actual, err := io.ReadAll(validated.Body)
			require.NoError(t, err)
			assert.Equal(t, tc.body, string(actual))
			assert.Error(t, s.validateKnowledgeOutbound(1, upstream, key, makeReq(tc.altered)))
			bad := makeReq(tc.body)
			bad.Header.Set("Authorization", "Bearer another-key")
			assert.Error(t, s.validateKnowledgeOutbound(1, upstream, key, bad))
			bad = makeReq(tc.body)
			bad.Header.Add("Authorization", "Bearer another-key")
			assert.Error(t, s.validateKnowledgeOutbound(1, upstream, key, bad))
			bad = makeReq(tc.body)
			bad.Header["authorization"] = []string{"Bearer another-key"}
			assert.Error(t, s.validateKnowledgeOutbound(1, upstream, key, bad))
			bad = makeReq(tc.body)
			bad.Method = http.MethodGet
			assert.Error(t, s.validateKnowledgeOutbound(1, upstream, key, bad))
			bad = makeReq(tc.body)
			bad.Body = io.NopCloser(bytes.NewBufferString(tc.altered))
			assert.Error(t, s.validateKnowledgeOutbound(1, upstream, key, bad), "the body sent on the connection must be checked, not just GetBody")
			assert.Error(t, s.validateKnowledgeOutbound(2, upstream, key, makeReq(tc.body)))
		})
	}
}

func TestKnowledgeOutboundConnectionFailureDoesNotReplay(t *testing.T) {
	s, key, upstream := responseFixture(t, "embedding")
	body := `{"model":"upstream-embed","input":["first","second"],"dimensions":3}`
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		attempts.Add(1)
		_, _ = io.Copy(io.Discard, r.Body)
		conn, _, err := w.(http.Hijacker).Hijack()
		if err == nil {
			_ = conn.Close()
		}
	}))
	defer server.Close()
	client := server.Client()
	warm, err := client.Get(server.URL)
	require.NoError(t, err)
	_, err = io.Copy(io.Discard, warm.Body)
	require.NoError(t, err)
	require.NoError(t, warm.Body.Close())
	req, err := http.NewRequest(http.MethodPost, server.URL, bytes.NewBufferString(body))
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Idempotency-Key", "client-call")
	require.NoError(t, s.validateKnowledgeOutbound(1, upstream, key, req))
	resp, err := client.Do(req)
	if resp != nil {
		resp.Body.Close()
	}
	require.Error(t, err)
	assert.EqualValues(t, 1, attempts.Load())
}

// The prototype validates the bytes that a real HTTP transport will consume;
// this fixture does not activate it in the gateway's production relay.
func TestKnowledgeOutboundControlledHTTPSend(t *testing.T) {
	s, key, upstream := responseFixture(t, "embedding")
	want := `{"model":"upstream-embed","input":["first","second"],"dimensions":3}`
	var received []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var err error
		received, err = io.ReadAll(r.Body)
		assert.NoError(t, err)
		assert.Equal(t, "Bearer "+key, r.Header.Get("Authorization"))
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	req, err := http.NewRequest(http.MethodPost, server.URL, bytes.NewBufferString(want))
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+key)
	require.NoError(t, s.validateKnowledgeOutbound(1, upstream, key, req))
	resp, err := server.Client().Do(req)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	assert.Equal(t, http.StatusNoContent, resp.StatusCode)
	assert.Equal(t, want, string(received))
}

func TestAliEmbeddingV4OutboundBindsDestinationAndBytes(t *testing.T) {
	s, key, _ := responseFixture(t, "embedding")
	s.adapter.config.AllowAliEmbeddingV4 = true
	var frozen frozenKnowledge
	require.NoError(t, common.UnmarshalUnique([]byte(s.call.MediaSnapshot), &frozen))
	frozen.Policy.Version = "knowledge_ali_text_v1"
	frozen.Policy.Origin = "https://dashscope.aliyuncs.com"
	frozen.Policy.ChannelType = constant.ChannelTypeAli
	frozen.Policy.UpstreamModel = "text-embedding-v4"
	frozen.Policy.Space = &dto.EmbeddingSpace{Provider: "aliyun", Model: "text-embedding-v4", Version: "v4", Dimensions: 1024, InputSemantics: "text"}
	frozen.Body = []byte(`{"model":"public","input":["first","second"],"dimensions":1024}`)
	snapshot, err := common.Marshal(frozen)
	require.NoError(t, err)
	s.call.MediaSnapshot = string(snapshot)
	makeReq := func(raw string) *http.Request {
		r, err := http.NewRequest(http.MethodPost, "https://dashscope.aliyuncs.com/compatible-mode/v1/embeddings", bytes.NewBufferString(raw))
		require.NoError(t, err)
		r.Header.Set("Authorization", "Bearer "+key)
		return r
	}
	body := `{"model":"text-embedding-v4","input":["first","second"],"dimensions":1024}`
	req := makeReq(body)
	require.NoError(t, s.ValidateAliEmbeddingOutbound(1, "text-embedding-v4", key, req))
	actual, err := io.ReadAll(req.Body)
	require.NoError(t, err)
	assert.Equal(t, body, string(actual))
	for _, bad := range []func(*http.Request){
		func(r *http.Request) { r.URL.Host = "attacker.example" },
		func(r *http.Request) { r.URL.RawQuery = "other=1" },
		func(r *http.Request) { r.Host = "attacker.example" },
		func(r *http.Request) { r.Header.Set("X-DashScope-Plugin", "override") },
		func(r *http.Request) { r.Header.Set("Authorization", "Bearer wrong") },
		func(r *http.Request) {
			r.Body = io.NopCloser(bytes.NewBufferString(`{"model":"text-embedding-v4","input":["foreign"],"dimensions":1024}`))
		},
	} {
		r := makeReq(body)
		bad(r)
		assert.Error(t, s.ValidateAliEmbeddingOutbound(1, "text-embedding-v4", key, r))
	}
	s.adapter.config.AllowAliEmbeddingV4 = false
	assert.Error(t, s.ValidateAliEmbeddingOutbound(1, "text-embedding-v4", key, makeReq(body)))
}
