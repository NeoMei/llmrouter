package mcpbridge

import (
	"bufio"
	"bytes"
	"context"
	json "encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/QuantumNous/new-api/common"
)

const (
	ProtocolVersion = "2025-06-18"
	MaxLineBytes    = 64 * 1024
	MaxPromptBytes  = 32 * 1024
	MaxOutputBytes  = 64 * 1024
)

type ChatRequest struct {
	Model           string `json:"model"`
	Prompt          string `json:"prompt"`
	MaxOutputTokens int    `json:"max_output_tokens"`
}

type Client interface {
	ListModels(context.Context) ([]string, error)
	Chat(context.Context, ChatRequest) (string, error)
}

type Server struct {
	client Client
	mu     sync.Mutex
}

func NewServer(client Client) *Server { return &Server{client: client} }

func Serve(ctx context.Context, input io.Reader, output io.Writer, server *Server) error {
	if input == nil || output == nil || server == nil {
		return fmt.Errorf("bridge stdio is not configured")
	}
	reader := bufio.NewReaderSize(input, MaxLineBytes+1)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		line, err := reader.ReadSlice('\n')
		if errors.Is(err, bufio.ErrBufferFull) {
			return fmt.Errorf("request line exceeds 64 KiB")
		}
		if err != nil && !errors.Is(err, io.EOF) {
			return fmt.Errorf("read stdio request: %w", err)
		}
		line = bytes.TrimSuffix(line, []byte{'\n'})
		line = bytes.TrimSuffix(line, []byte{'\r'})
		if len(line) > MaxLineBytes {
			return fmt.Errorf("request line exceeds 64 KiB")
		}
		if len(line) != 0 {
			response := server.Handle(ctx, line)
			if len(response) != 0 {
				if _, writeErr := output.Write(append(response, '\n')); writeErr != nil {
					return fmt.Errorf("write stdio response: %w", writeErr)
				}
			}
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
	}
}

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type rpcEnvelope struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

func (server *Server) Handle(ctx context.Context, line []byte) []byte {
	if len(line) > MaxLineBytes {
		return marshalRPCError(nil, -32600, "request line exceeds 64 KiB")
	}
	if server == nil || server.client == nil {
		return marshalRPCError(nil, -32603, "bridge is not configured")
	}
	if !server.mu.TryLock() {
		return marshalRPCError(nil, -32603, "only one request may be in flight")
	}
	defer server.mu.Unlock()

	var request rpcRequest
	if err := common.UnmarshalUseNumber(line, &request); err != nil {
		return marshalRPCError(nil, -32700, "parse error")
	}
	if request.JSONRPC != "2.0" || request.Method == "" {
		return marshalRPCError(request.ID, -32600, "invalid request")
	}

	switch request.Method {
	case "initialize":
		var params struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		if err := common.Unmarshal(request.Params, &params); err != nil || params.ProtocolVersion != ProtocolVersion {
			return marshalRPCError(request.ID, -32602, "unsupported protocol version")
		}
		return marshalRPCResult(request.ID, map[string]any{
			"protocolVersion": ProtocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "llmapi-mcp-bridge", "version": "0.1.0"},
		})
	case "notifications/initialized":
		if len(request.ID) == 0 {
			return nil
		}
		return marshalRPCResult(request.ID, map[string]any{})
	case "ping":
		return marshalRPCResult(request.ID, map[string]any{})
	case "tools/list":
		return marshalRPCResult(request.ID, map[string]any{"tools": boundedTools()})
	case "tools/call":
		return server.callTool(ctx, request)
	default:
		return marshalRPCError(request.ID, -32601, "method not found")
	}
}

func (server *Server) callTool(ctx context.Context, request rpcRequest) []byte {
	var params struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := common.Unmarshal(request.Params, &params); err != nil {
		return marshalRPCError(request.ID, -32602, "invalid tool parameters")
	}
	switch params.Name {
	case "llmapi_list_models":
		var arguments map[string]json.RawMessage
		trimmed := bytes.TrimSpace(params.Arguments)
		if len(trimmed) != 0 && string(trimmed) != "null" {
			if err := common.Unmarshal(trimmed, &arguments); err != nil || arguments == nil || len(arguments) != 0 {
				return marshalRPCError(request.ID, -32602, "invalid list-models arguments")
			}
		}
		models, err := server.client.ListModels(ctx)
		if err != nil {
			return marshalRPCError(request.ID, -32603, "model listing failed")
		}
		text, err := common.Marshal(models)
		if err != nil || len(text) > MaxOutputBytes {
			return marshalRPCError(request.ID, -32603, "model listing is too large")
		}
		return marshalRPCResult(request.ID, toolResult(string(text)))
	case "llmapi_chat":
		var chat ChatRequest
		if err := unmarshalChatArguments(params.Arguments, &chat); err != nil || chat.Model == "" || chat.Prompt == "" || len(chat.Prompt) > MaxPromptBytes {
			return marshalRPCError(request.ID, -32602, "invalid chat arguments")
		}
		models, err := server.client.ListModels(ctx)
		if err != nil {
			return marshalRPCError(request.ID, -32603, "model listing failed")
		}
		allowed := false
		for _, model := range models {
			if model == chat.Model {
				allowed = true
				break
			}
		}
		if !allowed {
			return marshalRPCError(request.ID, -32602, "model is not configured")
		}
		if chat.MaxOutputTokens < 1 {
			chat.MaxOutputTokens = 1
		} else if chat.MaxOutputTokens > 256 {
			chat.MaxOutputTokens = 256
		}
		output, err := server.client.Chat(ctx, chat)
		if err != nil {
			return marshalRPCError(request.ID, -32603, "chat request failed")
		}
		if len(output) > MaxOutputBytes {
			return marshalRPCError(request.ID, -32603, "chat output exceeds 64 KiB")
		}
		return marshalRPCResult(request.ID, toolResult(output))
	default:
		return marshalRPCError(request.ID, -32602, "unknown tool")
	}
}

func unmarshalChatArguments(raw json.RawMessage, output *ChatRequest) error {
	var fields map[string]json.RawMessage
	if err := common.Unmarshal(raw, &fields); err != nil || fields == nil {
		return &rpcError{Code: -32602, Message: "invalid arguments"}
	}
	for name := range fields {
		if name != "model" && name != "prompt" && name != "max_output_tokens" {
			return &rpcError{Code: -32602, Message: "unknown argument"}
		}
	}
	if len(fields) != 3 {
		return &rpcError{Code: -32602, Message: "missing argument"}
	}
	return common.Unmarshal(raw, output)
}

func (*rpcError) Error() string { return "invalid RPC parameters" }

func boundedTools() []map[string]any {
	return []map[string]any{
		{
			"name": "llmapi_list_models", "description": "List the configured LLMAPI model allow-list.",
			"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false},
		},
		{
			"name": "llmapi_chat", "description": "Send one bounded non-streaming chat prompt to an allowed model.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"model": map[string]any{"type": "string"}, "prompt": map[string]any{"type": "string", "maxLength": MaxPromptBytes},
					"max_output_tokens": map[string]any{"type": "integer", "minimum": 1, "maximum": 256},
				},
				"required": []string{"model", "prompt", "max_output_tokens"}, "additionalProperties": false,
			},
		},
	}
}

func toolResult(text string) map[string]any {
	return map[string]any{"content": []map[string]string{{"type": "text", "text": text}}}
}

func marshalRPCResult(id json.RawMessage, result any) []byte {
	if len(id) == 0 {
		id = json.RawMessage("null")
	}
	raw, _ := common.Marshal(rpcEnvelope{JSONRPC: "2.0", ID: id, Result: result})
	return raw
}

func marshalRPCError(id json.RawMessage, code int, message string) []byte {
	if len(id) == 0 {
		id = json.RawMessage("null")
	}
	message = strings.TrimSpace(message)
	raw, _ := common.Marshal(rpcEnvelope{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: code, Message: message}})
	return raw
}
