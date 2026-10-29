package platformbilling

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

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
			require.NoError(t, s.validateKnowledgeOutbound(1, upstream, key, validated))
			replay, err := validated.GetBody()
			require.NoError(t, err)
			replayed, err := io.ReadAll(replay)
			require.NoError(t, err)
			require.NoError(t, replay.Close())
			actual, err := io.ReadAll(validated.Body)
			require.NoError(t, err)
			assert.Equal(t, tc.body, string(actual))
			assert.Equal(t, actual, replayed)
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
