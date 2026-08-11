package mcpbridge

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/require"
)

type fakeClient struct {
	models  []string
	chat    string
	err     error
	last    ChatRequest
	started chan struct{}
	release chan struct{}
}

func (client *fakeClient) ListModels(context.Context) ([]string, error) {
	if client.started != nil {
		close(client.started)
		<-client.release
	}
	return append([]string(nil), client.models...), client.err
}

func (client *fakeClient) Chat(_ context.Context, request ChatRequest) (string, error) {
	client.last = request
	return client.chat, client.err
}

type rpcResponse struct {
	Result any `json:"result"`
	Error  *struct {
		Code int `json:"code"`
	} `json:"error"`
}

func TestServerInitializesAndListsOnlyBoundedTools(t *testing.T) {
	client := &fakeClient{models: []string{"model-a"}}
	server := NewServer(client)
	initialized := call(t, server, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`)
	require.Equal(t, "2025-06-18", initialized.Result.(map[string]any)["protocolVersion"])
	listed := call(t, server, `{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`)
	require.Equal(t, []string{"llmapi_list_models", "llmapi_chat"}, toolNames(t, listed))
}

func TestChatRejectsUnknownModelAndOversizedPrompt(t *testing.T) {
	client := &fakeClient{models: []string{"model-a"}}
	server := NewServer(client)
	requireRPCError(t, server, chatCall(t, "model-b", "hello", 16), -32602)
	requireRPCError(t, server, chatCall(t, "model-a", strings.Repeat("x", 32769), 16), -32602)
}

func TestChatRejectsUnknownArgumentsClampsTokensAndBoundsOutput(t *testing.T) {
	client := &fakeClient{models: []string{"model-a"}, chat: "ok"}
	server := NewServer(client)
	requireRPCError(t, server, `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"llmapi_chat","arguments":{"model":"model-a","prompt":"hello","max_output_tokens":16,"url":"https://elsewhere.example"}}}`, -32602)
	response := call(t, server, chatCall(t, "model-a", "hello", 999))
	require.Nil(t, response.Error)
	require.Equal(t, 256, client.last.MaxOutputTokens)
	client.chat = strings.Repeat("x", MaxOutputBytes+1)
	requireRPCError(t, server, chatCall(t, "model-a", "hello", 16), -32603)
}

func TestServerAcceptsOnlyMCPMethodsAndBoundsInputLines(t *testing.T) {
	server := NewServer(&fakeClient{models: []string{"model-a"}})
	requireRPCError(t, server, `{"jsonrpc":"2.0","id":1,"method":"resources/list","params":{}}`, -32601)
	requireRPCError(t, server, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"llmapi_list_models","arguments":{"path":"/tmp/private"}}}`, -32602)
	requireRPCError(t, server, strings.Repeat("x", MaxLineBytes+1), -32600)
	require.Nil(t, server.Handle(t.Context(), []byte(`{"jsonrpc":"2.0","method":"notifications/initialized","params":{}}`)))
}

func TestServerAllowsOnlyOneInFlightRequest(t *testing.T) {
	client := &fakeClient{models: []string{"model-a"}, started: make(chan struct{}), release: make(chan struct{})}
	server := NewServer(client)
	completed := make(chan []byte, 1)
	go func() {
		completed <- server.Handle(t.Context(), []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"llmapi_list_models","arguments":{}}}`))
	}()
	<-client.started
	requireRPCError(t, server, `{"jsonrpc":"2.0","id":2,"method":"ping","params":{}}`, -32603)
	close(client.release)
	require.NotEmpty(t, <-completed)
}

func TestServeProcessesNewlineDelimitedRPCAndRejectsOversizedLines(t *testing.T) {
	server := NewServer(&fakeClient{models: []string{"model-a"}})
	input := strings.NewReader("{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"ping\",\"params\":{}}\n{\"jsonrpc\":\"2.0\",\"id\":2,\"method\":\"tools/list\",\"params\":{}}\n")
	var output bytes.Buffer
	require.NoError(t, Serve(t.Context(), input, &output, server))
	require.Len(t, strings.Split(strings.TrimSpace(output.String()), "\n"), 2)

	output.Reset()
	err := Serve(t.Context(), strings.NewReader(strings.Repeat("x", MaxLineBytes+1)+"\n"), &output, server)
	require.Error(t, err)
	require.Empty(t, output.String())
}

func call(t *testing.T, server *Server, line string) rpcResponse {
	t.Helper()
	raw := server.Handle(t.Context(), []byte(line))
	require.NotEmpty(t, raw)
	var response rpcResponse
	require.NoError(t, common.UnmarshalUseNumber(raw, &response))
	return response
}

func chatCall(t *testing.T, model, prompt string, maxOutputTokens int) string {
	t.Helper()
	raw, err := common.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 3, "method": "tools/call",
		"params": map[string]any{
			"name":      "llmapi_chat",
			"arguments": map[string]any{"model": model, "prompt": prompt, "max_output_tokens": maxOutputTokens},
		},
	})
	require.NoError(t, err)
	return string(raw)
}

func toolNames(t *testing.T, response rpcResponse) []string {
	t.Helper()
	result, ok := response.Result.(map[string]any)
	require.True(t, ok)
	tools, ok := result["tools"].([]any)
	require.True(t, ok)
	names := make([]string, 0, len(tools))
	for _, raw := range tools {
		tool, ok := raw.(map[string]any)
		require.True(t, ok)
		name, ok := tool["name"].(string)
		require.True(t, ok)
		names = append(names, name)
	}
	return names
}

func requireRPCError(t *testing.T, server *Server, line string, code int) {
	t.Helper()
	response := call(t, server, line)
	require.Nil(t, response.Result)
	require.NotNil(t, response.Error, fmt.Sprintf("response: %s", server.Handle(t.Context(), []byte(line))))
	require.Equal(t, code, response.Error.Code)
}
