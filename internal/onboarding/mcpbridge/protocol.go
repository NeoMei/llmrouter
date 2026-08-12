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
	"sync/atomic"

	"github.com/QuantumNous/new-api/common"
)

const (
	ProtocolVersion = "2025-06-18"
	MaxLineBytes    = 64 * 1024
	MaxPromptBytes  = 32 * 1024
	MaxOutputBytes  = 64 * 1024
	maxIDBytes      = 256
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

type serverState uint8

const (
	stateUninitialized serverState = iota
	stateInitialized
	stateReady
)

type Server struct {
	client Client
	mu     sync.Mutex
	state  atomic.Uint32
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

func (*rpcError) Error() string { return "invalid RPC parameters" }

type rpcEnvelope struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type parsedRPC struct {
	request      rpcRequest
	id           json.RawMessage
	notification bool
	errorCode    int
	errorMessage string
}

func (server *Server) Handle(ctx context.Context, line []byte) []byte {
	if len(line) > MaxLineBytes {
		var oversized map[string]json.RawMessage
		if common.ValidJSON(line) {
			if strictErr := common.UnmarshalUnique(line, &oversized); strictErr == nil && oversized != nil {
				rawID, hasID := oversized["id"]
				if !hasID {
					return nil
				}
				var idValue any
				if common.UnmarshalUseNumber(rawID, &idValue) == nil {
					if id, valid := validRPCID(idValue); valid {
						return marshalRPCError(id, -32600, "request line exceeds 64 KiB")
					}
				}
			} else {
				hasID, fieldErr := common.HasTopLevelJSONField(line, "id")
				if fieldErr == nil && !hasID {
					return nil
				}
			}
		}
		return marshalRPCError(nil, -32600, "request line exceeds 64 KiB")
	}
	parsed := parseRPC(line)
	if parsed.errorCode != 0 {
		if parsed.notification {
			return nil
		}
		return marshalRPCError(parsed.id, parsed.errorCode, parsed.errorMessage)
	}
	if parsed.notification {
		server.handleNotification(parsed.request)
		return nil
	}
	if server == nil || server.client == nil {
		return marshalRPCError(parsed.id, -32603, "bridge is not configured")
	}
	if !server.mu.TryLock() {
		return marshalRPCError(parsed.id, -32603, "only one request may be in flight")
	}
	defer server.mu.Unlock()
	return server.dispatchRequest(ctx, parsed.request)
}

func parseRPC(line []byte) parsedRPC {
	if !common.ValidJSON(line) {
		return parsedRPC{errorCode: -32700, errorMessage: "parse error"}
	}
	var loose any
	if err := common.UnmarshalUseNumber(line, &loose); err != nil {
		return parsedRPC{errorCode: -32700, errorMessage: "parse error"}
	}
	object, ok := loose.(map[string]any)
	if !ok {
		return parsedRPC{errorCode: -32600, errorMessage: "invalid request"}
	}
	idValue, hasID := object["id"]
	if !hasID {
		var request rpcRequest
		if err := common.UnmarshalUnique(line, &request); err != nil || request.JSONRPC != "2.0" || request.Method == "" {
			return parsedRPC{notification: true, errorCode: -32600, errorMessage: "invalid notification"}
		}
		return parsedRPC{request: request, notification: true}
	}
	id, validID := validRPCID(idValue)
	if !validID {
		return parsedRPC{errorCode: -32600, errorMessage: "invalid request ID"}
	}
	var request rpcRequest
	if err := common.UnmarshalUnique(line, &request); err != nil {
		var duplicate *common.DuplicateJSONKeyError
		if errors.As(err, &duplicate) && duplicate.Depth == 0 && duplicate.Key == "id" {
			id = nil
		}
		return parsedRPC{id: id, errorCode: -32600, errorMessage: "invalid request"}
	}
	if request.JSONRPC != "2.0" || request.Method == "" {
		return parsedRPC{id: id, errorCode: -32600, errorMessage: "invalid request"}
	}
	request.ID = id
	return parsedRPC{request: request, id: id}
}

func validRPCID(value any) (json.RawMessage, bool) {
	switch typed := value.(type) {
	case string:
		if len(typed) > maxIDBytes {
			return nil, false
		}
	case json.Number:
		if len(typed.String()) > maxIDBytes || strings.ContainsAny(typed.String(), ".eE") {
			return nil, false
		}
	default:
		return nil, false
	}
	raw, err := common.Marshal(value)
	return raw, err == nil
}

func (server *Server) handleNotification(request rpcRequest) {
	if server == nil || server.client == nil {
		return
	}
	if request.Method != "notifications/initialized" || !emptyObjectOrAbsent(request.Params) {
		return
	}
	server.state.CompareAndSwap(uint32(stateInitialized), uint32(stateReady))
}

func (server *Server) dispatchRequest(ctx context.Context, request rpcRequest) []byte {
	switch serverState(server.state.Load()) {
	case stateUninitialized:
		if request.Method != "initialize" {
			return marshalRPCError(request.ID, -32600, "initialize must be the first request")
		}
		return server.initialize(request)
	case stateInitialized:
		switch request.Method {
		case "initialize":
			return marshalRPCError(request.ID, -32600, "initialize was already completed")
		case "ping":
			if !emptyObjectOrAbsent(request.Params) {
				return marshalRPCError(request.ID, -32602, "invalid ping parameters")
			}
			return marshalRPCResult(request.ID, map[string]any{})
		default:
			return marshalRPCError(request.ID, -32600, "initialized notification is required")
		}
	case stateReady:
		switch request.Method {
		case "initialize":
			return marshalRPCError(request.ID, -32600, "initialize was already completed")
		case "notifications/initialized":
			return marshalRPCError(request.ID, -32600, "initialized must be a notification")
		case "ping":
			if !emptyObjectOrAbsent(request.Params) {
				return marshalRPCError(request.ID, -32602, "invalid ping parameters")
			}
			return marshalRPCResult(request.ID, map[string]any{})
		case "tools/list":
			if !emptyObjectOrAbsent(request.Params) {
				return marshalRPCError(request.ID, -32602, "invalid tools list parameters")
			}
			return marshalRPCResult(request.ID, map[string]any{"tools": boundedTools()})
		case "tools/call":
			return server.callTool(ctx, request)
		default:
			return marshalRPCError(request.ID, -32601, "method not found")
		}
	default:
		return marshalRPCError(request.ID, -32603, "invalid server state")
	}
}

func (server *Server) initialize(request rpcRequest) []byte {
	var params struct {
		ProtocolVersion string                     `json:"protocolVersion"`
		Capabilities    map[string]json.RawMessage `json:"capabilities"`
		ClientInfo      struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		} `json:"clientInfo"`
	}
	if err := common.UnmarshalUnique(request.Params, &params); err != nil || params.ProtocolVersion != ProtocolVersion || params.Capabilities == nil || params.ClientInfo.Name == "" || params.ClientInfo.Version == "" || hasControl(params.ClientInfo.Name) || hasControl(params.ClientInfo.Version) {
		return marshalRPCError(request.ID, -32602, "invalid initialize parameters")
	}
	server.state.Store(uint32(stateInitialized))
	return marshalRPCResult(request.ID, map[string]any{
		"protocolVersion": ProtocolVersion,
		"capabilities":    map[string]any{"tools": map[string]any{}},
		"serverInfo":      map[string]any{"name": "llmrouter-mcp-bridge", "version": "0.1.0"},
	})
}

func (server *Server) callTool(ctx context.Context, request rpcRequest) []byte {
	var params struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := common.UnmarshalUnique(request.Params, &params); err != nil {
		return marshalRPCError(request.ID, -32602, "invalid tool parameters")
	}
	switch params.Name {
	case "llmapi_list_models":
		if !emptyObjectOrMissing(params.Arguments) {
			return marshalRPCError(request.ID, -32602, "invalid list-models arguments")
		}
		models, err := server.client.ListModels(ctx)
		if err != nil {
			return marshalRPCError(request.ID, -32603, "model listing failed")
		}
		text, err := common.Marshal(models)
		if err != nil {
			return marshalRPCError(request.ID, -32603, "model listing failed")
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
		return marshalRPCResult(request.ID, toolResult(output))
	default:
		return marshalRPCError(request.ID, -32602, "unknown tool")
	}
}

func unmarshalChatArguments(raw json.RawMessage, output *ChatRequest) error {
	var fields map[string]json.RawMessage
	if err := common.UnmarshalUnique(raw, &fields); err != nil || fields == nil {
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
	return common.UnmarshalUnique(raw, output)
}

func emptyObject(raw json.RawMessage) bool {
	var object map[string]json.RawMessage
	return common.UnmarshalUnique(raw, &object) == nil && object != nil && len(object) == 0
}

func emptyObjectOrAbsent(raw json.RawMessage) bool {
	return len(bytes.TrimSpace(raw)) == 0 || emptyObject(raw)
}

func emptyObjectOrMissing(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) == 0 || string(trimmed) == "null" || emptyObject(trimmed)
}

func boundedTools() []map[string]any {
	return []map[string]any{
		{
			"name": "llmapi_list_models", "description": "List the configured LLMROUTER model allow-list.",
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
	return marshalBoundedEnvelope(rpcEnvelope{JSONRPC: "2.0", ID: id, Result: result}, id)
}

func marshalRPCError(id json.RawMessage, code int, message string) []byte {
	if len(id) == 0 {
		id = json.RawMessage("null")
	}
	message = strings.TrimSpace(message)
	return marshalBoundedEnvelope(rpcEnvelope{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: code, Message: message}}, id)
}

func marshalBoundedEnvelope(envelope rpcEnvelope, id json.RawMessage) []byte {
	raw, err := common.Marshal(envelope)
	if err == nil && len(raw)+1 <= MaxLineBytes {
		return raw
	}
	fallback := rpcEnvelope{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: -32603, Message: "response exceeds 64 KiB"}}
	raw, err = common.Marshal(fallback)
	if err == nil && len(raw)+1 <= MaxLineBytes {
		return raw
	}
	raw, _ = common.Marshal(rpcEnvelope{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: &rpcError{Code: -32603, Message: "response exceeds 64 KiB"}})
	return raw
}
