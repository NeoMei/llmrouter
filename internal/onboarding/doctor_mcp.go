package onboarding

import (
	"bufio"
	"context"
	json "encoding/json"
	"fmt"
	"io"
	"os/exec"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/internal/onboarding/mcpbridge"
)

const mcpDoctorTimeout = 10 * time.Second

type doctorMCPResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  map[string]any  `json:"result"`
	Error   *struct {
		Code int `json:"code"`
	} `json:"error"`
}

func probeClaudeDesktopMCP(ctx context.Context, request CheckRequest, smoke bool) (lifecycleErr, smokeErr error) {
	command, credentialPath, _, err := loadClaudeDesktopBridge(request)
	if err != nil {
		return fmt.Errorf("MCP configuration verification failed"), nil
	}
	processContext, cancel := context.WithTimeout(ctx, mcpDoctorTimeout)
	defer cancel()
	process := exec.CommandContext(processContext, command, "--config", credentialPath)
	stdin, err := process.StdinPipe()
	if err != nil {
		return fmt.Errorf("MCP process setup failed"), nil
	}
	stdout, err := process.StdoutPipe()
	if err != nil {
		return fmt.Errorf("MCP process setup failed"), nil
	}
	stderr, err := process.StderrPipe()
	if err != nil {
		return fmt.Errorf("MCP process setup failed"), nil
	}
	if err := process.Start(); err != nil {
		return fmt.Errorf("MCP process start failed"), nil
	}
	stderrDone := make(chan struct{})
	go func() {
		_, _ = io.Copy(io.Discard, stderr)
		close(stderrDone)
	}()
	waited := false
	wait := func() error {
		if waited {
			return fmt.Errorf("MCP process was already waited")
		}
		waited = true
		return process.Wait()
	}
	defer func() {
		_ = stdin.Close()
		if !waited {
			cancel()
			if process.Process != nil {
				_ = process.Process.Kill()
			}
			_ = wait()
		}
		<-stderrDone
	}()

	reader := bufio.NewReaderSize(stdout, mcpbridge.MaxLineBytes+1)
	initialize := map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "initialize",
		"params": map[string]any{
			"protocolVersion": mcpbridge.ProtocolVersion, "capabilities": map[string]any{},
			"clientInfo": map[string]any{"name": "llmrouter-onboard-doctor", "version": OnboardingVersion},
		},
	}
	initialized, err := exchangeDoctorMCP(stdin, reader, initialize, 1)
	if err != nil || initialized.Result["protocolVersion"] != mcpbridge.ProtocolVersion {
		return fmt.Errorf("MCP initialize failed"), nil
	}
	if err := writeDoctorMCP(stdin, map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized", "params": map[string]any{}}); err != nil {
		return fmt.Errorf("MCP initialized notification failed"), nil
	}
	listed, err := exchangeDoctorMCP(stdin, reader, map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/list", "params": map[string]any{}}, 2)
	if err != nil || !hasExactMCPTools(listed.Result) {
		return fmt.Errorf("MCP tools check failed"), nil
	}
	if smoke {
		if request.Model == "" {
			return nil, fmt.Errorf("MCP smoke model is unavailable")
		}
		called, callErr := exchangeDoctorMCP(stdin, reader, map[string]any{
			"jsonrpc": "2.0", "id": 3, "method": "tools/call",
			"params": map[string]any{
				"name": "llmapi_chat", "arguments": map[string]any{"model": request.Model, "prompt": "Reply with OK.", "max_output_tokens": 16},
			},
		}, 3)
		if callErr != nil || called.Result == nil {
			return nil, fmt.Errorf("MCP chat tool call failed")
		}
	}
	_ = stdin.Close()
	waitErr := wait()
	if waitErr != nil {
		if smoke {
			return nil, fmt.Errorf("MCP bridge exited unsuccessfully")
		}
		return fmt.Errorf("MCP bridge exited unsuccessfully"), nil
	}
	return nil, nil
}

func writeDoctorMCP(writer io.Writer, request any) error {
	raw, err := common.Marshal(request)
	if err != nil || len(raw)+1 > mcpbridge.MaxLineBytes {
		return fmt.Errorf("MCP request is invalid")
	}
	_, err = writer.Write(append(raw, '\n'))
	return err
}

func exchangeDoctorMCP(writer io.Writer, reader *bufio.Reader, request any, expectedID int) (doctorMCPResponse, error) {
	if err := writeDoctorMCP(writer, request); err != nil {
		return doctorMCPResponse{}, err
	}
	line, err := reader.ReadSlice('\n')
	if err != nil || len(line) > mcpbridge.MaxLineBytes {
		return doctorMCPResponse{}, fmt.Errorf("MCP response is unavailable")
	}
	var response doctorMCPResponse
	if err := common.UnmarshalUnique(line, &response); err != nil || response.JSONRPC != "2.0" || response.Error != nil || response.Result == nil {
		return doctorMCPResponse{}, fmt.Errorf("MCP response is invalid")
	}
	var id int
	if err := common.Unmarshal(response.ID, &id); err != nil || id != expectedID {
		return doctorMCPResponse{}, fmt.Errorf("MCP response ID mismatch")
	}
	return response, nil
}

func hasExactMCPTools(result map[string]any) bool {
	tools, ok := result["tools"].([]any)
	if !ok || len(tools) != 2 {
		return false
	}
	names := make([]string, 0, 2)
	for _, value := range tools {
		tool, ok := value.(map[string]any)
		if !ok {
			return false
		}
		name, ok := tool["name"].(string)
		if !ok {
			return false
		}
		names = append(names, name)
	}
	return names[0] == "llmapi_list_models" && names[1] == "llmapi_chat"
}
