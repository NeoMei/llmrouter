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
	ID     any `json:"id"`
	Result any `json:"result"`
	Error  *struct {
		Code int `json:"code"`
	} `json:"error"`
}

func TestServerInitializesAndListsOnlyBoundedTools(t *testing.T) {
	client := &fakeClient{models: []string{"model-a"}}
	server := NewServer(client)
	initialized := call(t, server, initializeCall(1))
	require.Equal(t, "2025-06-18", initialized.Result.(map[string]any)["protocolVersion"])
	require.Nil(t, server.Handle(t.Context(), []byte(`{"jsonrpc":"2.0","method":"notifications/initialized","params":{}}`)))
	listed := call(t, server, `{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`)
	require.Equal(t, []string{"llmapi_list_models", "llmapi_chat"}, toolNames(t, listed))
}

func TestChatRejectsUnknownModelAndOversizedPrompt(t *testing.T) {
	client := &fakeClient{models: []string{"model-a"}}
	server := readyServer(t, client)
	requireRPCError(t, server, chatCall(t, "model-b", "hello", 16), -32602)
	requireRPCError(t, server, chatCall(t, "model-a", strings.Repeat("x", 32769), 16), -32602)
}

func TestChatRejectsUnknownArgumentsClampsTokensAndBoundsOutput(t *testing.T) {
	client := &fakeClient{models: []string{"model-a"}, chat: "ok"}
	server := readyServer(t, client)
	requireRPCError(t, server, `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"llmapi_chat","arguments":{"model":"model-a","prompt":"hello","max_output_tokens":16,"url":"https://elsewhere.example"}}}`, -32602)
	response := call(t, server, chatCall(t, "model-a", "hello", 999))
	require.Nil(t, response.Error)
	require.Equal(t, 256, client.last.MaxOutputTokens)
	client.chat = strings.Repeat("x", MaxOutputBytes+1)
	requireRPCError(t, server, chatCall(t, "model-a", "hello", 16), -32603)
}

func TestServerAcceptsOnlyMCPMethodsAndBoundsInputLines(t *testing.T) {
	server := readyServer(t, &fakeClient{models: []string{"model-a"}})
	requireRPCError(t, server, `{"jsonrpc":"2.0","id":1,"method":"resources/list","params":{}}`, -32601)
	requireRPCError(t, server, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"llmapi_list_models","arguments":{"path":"/tmp/private"}}}`, -32602)
	requireRPCError(t, server, strings.Repeat("x", MaxLineBytes+1), -32600)
	require.Nil(t, server.Handle(t.Context(), []byte(`{"jsonrpc":"2.0","method":"notifications/initialized","params":{}}`)))
}

func TestServerAllowsOnlyOneInFlightRequest(t *testing.T) {
	client := &fakeClient{models: []string{"model-a"}, started: make(chan struct{}), release: make(chan struct{})}
	server := readyServer(t, client)
	completed := make(chan []byte, 1)
	go func() {
		completed <- server.Handle(t.Context(), []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"llmapi_list_models","arguments":{}}}`))
	}()
	<-client.started
	busy := call(t, server, `{"jsonrpc":"2.0","id":2,"method":"ping","params":{}}`)
	require.NotNil(t, busy.Error)
	require.Equal(t, -32603, busy.Error.Code)
	require.Equal(t, "2", fmt.Sprint(busy.ID))
	require.Nil(t, server.Handle(t.Context(), []byte(`{"jsonrpc":"2.0","method":"ping","params":{}}`)))
	close(client.release)
	require.NotEmpty(t, <-completed)
}

func TestServeProcessesNewlineDelimitedRPCAndRejectsOversizedLines(t *testing.T) {
	server := NewServer(&fakeClient{models: []string{"model-a"}})
	input := strings.NewReader(initializeCall(1) + "\n{\"jsonrpc\":\"2.0\",\"method\":\"notifications/initialized\",\"params\":{}}\n{\"jsonrpc\":\"2.0\",\"id\":2,\"method\":\"tools/list\",\"params\":{}}\n")
	var output bytes.Buffer
	require.NoError(t, Serve(t.Context(), input, &output, server))
	require.Len(t, strings.Split(strings.TrimSpace(output.String()), "\n"), 2)

	output.Reset()
	err := Serve(t.Context(), strings.NewReader(strings.Repeat("x", MaxLineBytes+1)+"\n"), &output, server)
	require.Error(t, err)
	require.Empty(t, output.String())
}

func TestLifecycleRequiresCompleteInitializeAndInitializedNotification(t *testing.T) {
	server := NewServer(&fakeClient{models: []string{"model-a"}})
	requireRPCError(t, server, `{"jsonrpc":"2.0","id":1,"method":"ping","params":{}}`, -32600)
	requireRPCError(t, server, `{"jsonrpc":"2.0","id":2,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`, -32602)
	initialized := call(t, server, initializeCall("init-1"))
	require.Nil(t, initialized.Error)
	requireRPCError(t, server, initializeCall(3), -32600)
	requireRPCError(t, server, `{"jsonrpc":"2.0","id":4,"method":"tools/list","params":{}}`, -32600)
	requireRPCError(t, server, `{"jsonrpc":"2.0","id":5,"method":"notifications/initialized","params":{}}`, -32600)
	require.Nil(t, server.Handle(t.Context(), []byte(`{"jsonrpc":"2.0","method":"notifications/initialized","params":{}}`)))
	require.Nil(t, call(t, server, `{"jsonrpc":"2.0","id":6,"method":"ping","params":{}}`).Error)
	require.Nil(t, call(t, server, `{"jsonrpc":"2.0","id":7,"method":"tools/list","params":{}}`).Error)
}

func TestInitializedNotificationMayOmitParams(t *testing.T) {
	server := NewServer(&fakeClient{models: []string{"model-a"}})
	require.Nil(t, call(t, server, initializeCall(1)).Error)
	require.Nil(t, server.Handle(t.Context(), []byte(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)))
	require.Nil(t, call(t, server, `{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`).Error)
}

func TestInitializedNotificationTransitionsWhileRequestLockIsBusy(t *testing.T) {
	server := NewServer(&fakeClient{models: []string{"model-a"}})
	require.Nil(t, call(t, server, initializeCall(1)).Error)
	server.mu.Lock()
	require.Nil(t, server.Handle(t.Context(), []byte(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)))
	server.mu.Unlock()
	require.Nil(t, call(t, server, `{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`).Error)
}

func TestStrictJSONRPCClassificationIDsAndDuplicateKeys(t *testing.T) {
	server := NewServer(&fakeClient{models: []string{"model-a"}})
	requireRPCError(t, server, `{"jsonrpc":`, -32700)
	for _, invalid := range []string{
		`[]`,
		`{"jsonrpc":"1.0","id":"keep","method":"initialize","params":{}}`,
		`{"jsonrpc":"2.0","id":null,"method":"initialize","params":{}}`,
		`{"jsonrpc":"2.0","id":true,"method":"initialize","params":{}}`,
		`{"jsonrpc":"2.0","id":1.5,"method":"initialize","params":{}}`,
		`{"jsonrpc":"2.0","id":1,"id":2,"method":"initialize","params":{}}`,
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{"x":1,"x":2},"clientInfo":{"name":"test","version":"1"}}}`,
	} {
		requireRPCError(t, server, invalid, -32600)
	}
	duplicateID := call(t, server, `{"jsonrpc":"2.0","id":1,"id":2,"method":"initialize","params":{}}`)
	require.Nil(t, duplicateID.ID)
	ready := readyServer(t, &fakeClient{models: []string{"model-a"}})
	requireRPCError(t, ready, `{"jsonrpc":"2.0","id":8,"method":"ping","params":[]}`, -32602)
	requireRPCError(t, ready, `{"jsonrpc":"2.0","id":9,"method":"tools/list","params":[]}`, -32602)
	requireRPCError(t, ready, `{"jsonrpc":"2.0","id":10,"method":"tools/call","params":[]}`, -32602)
	oversizedNumericID := `{"jsonrpc":"2.0","id":` + strings.Repeat("1", maxIDBytes+1) + `,"method":"unknown","params":{}}`
	invalidNumeric := call(t, ready, oversizedNumericID)
	require.Equal(t, -32600, invalidNumeric.Error.Code)
	require.Nil(t, invalidNumeric.ID)
	unknown := call(t, ready, `{"jsonrpc":"2.0","id":"request-7","method":"resources/list","params":{}}`)
	require.NotNil(t, unknown.Error)
	require.Equal(t, "request-7", unknown.ID)
}

func TestEveryValidNotificationProducesNoBytes(t *testing.T) {
	server := NewServer(&fakeClient{models: []string{"model-a"}})
	for _, notification := range []string{
		`{"jsonrpc":"2.0","method":"unknown","params":{}}`,
		`{"jsonrpc":"1.0","method":"unknown","params":{}}`,
		`{"jsonrpc":"2.0","method":"tools/call","params":{"name":"llmapi_chat","arguments":{}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized","params":{}}`,
	} {
		require.Nil(t, server.Handle(t.Context(), []byte(notification)), notification)
	}
	oversized, err := common.Marshal(map[string]any{
		"jsonrpc": "2.0", "method": "unknown", "params": map[string]any{"padding": strings.Repeat("x", MaxLineBytes)},
	})
	require.NoError(t, err)
	require.Greater(t, len(oversized), MaxLineBytes)
	require.Nil(t, server.Handle(t.Context(), oversized))
	oversizedRequest, err := common.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": "large-id", "method": "unknown", "params": map[string]any{"padding": strings.Repeat("x", MaxLineBytes)},
	})
	require.NoError(t, err)
	response := call(t, server, string(oversizedRequest))
	require.Equal(t, "large-id", response.ID)
	require.Equal(t, -32600, response.Error.Code)
	oversizedDuplicateID := `{"jsonrpc":"2.0","id":1,"id":2,"method":"unknown","params":{"padding":"` + strings.Repeat("x", MaxLineBytes) + `"}}`
	duplicateResponse := call(t, server, oversizedDuplicateID)
	require.Equal(t, -32600, duplicateResponse.Error.Code)
	require.Nil(t, duplicateResponse.ID)
	oversizedInvalidNotification := `{"jsonrpc":"2.0","method":"unknown","method":"duplicate","params":{"padding":"` + strings.Repeat("x", MaxLineBytes) + `"}}`
	require.Nil(t, server.Handle(t.Context(), []byte(oversizedInvalidNotification)))
}

func TestFullyMarshaledResponseIncludingNewlineIsBounded(t *testing.T) {
	client := &fakeClient{models: []string{"model-a"}, chat: strings.Repeat(`"\\`, 20_000)}
	server := readyServer(t, client)
	raw := server.Handle(t.Context(), []byte(chatCall(t, "model-a", "hello", 256)))
	require.LessOrEqual(t, len(raw)+1, MaxLineBytes)
	var response rpcResponse
	require.NoError(t, common.UnmarshalUseNumber(raw, &response))
	require.NotNil(t, response.Error)
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

func initializeCall(id any) string {
	raw, _ := common.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": id, "method": "initialize",
		"params": map[string]any{
			"protocolVersion": ProtocolVersion,
			"capabilities":    map[string]any{},
			"clientInfo":      map[string]any{"name": "bridge-test", "version": "1.0"},
		},
	})
	return string(raw)
}

func readyServer(t *testing.T, client Client) *Server {
	t.Helper()
	server := NewServer(client)
	require.Nil(t, call(t, server, initializeCall(1)).Error)
	require.Nil(t, server.Handle(t.Context(), []byte(`{"jsonrpc":"2.0","method":"notifications/initialized","params":{}}`)))
	return server
}
