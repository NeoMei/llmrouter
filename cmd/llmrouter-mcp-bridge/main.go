package main

import (
	"context"
	"fmt"
	"os"

	"github.com/QuantumNous/new-api/internal/onboarding/mcpbridge"
)

func main() {
	if len(os.Args) != 3 || os.Args[1] != "--config" || os.Args[2] == "" {
		fmt.Fprintln(os.Stderr, "usage: llmrouter-mcp-bridge --config PATH")
		os.Exit(2)
	}
	config, err := mcpbridge.LoadConfig(os.Args[2])
	if err != nil {
		fmt.Fprintln(os.Stderr, "llmrouter-mcp-bridge: private configuration is unavailable")
		os.Exit(1)
	}
	client, err := mcpbridge.NewHTTPClient(config, nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, "llmrouter-mcp-bridge: private configuration is invalid")
		os.Exit(1)
	}
	if err := mcpbridge.Serve(context.Background(), os.Stdin, os.Stdout, mcpbridge.NewServer(client)); err != nil {
		fmt.Fprintln(os.Stderr, "llmrouter-mcp-bridge: stdio bridge stopped")
		os.Exit(1)
	}
}
