package main

import (
	"net/http"
	"testing"
	"time"
)

func TestNewPublicHTTPServerUsesExplicitLoopbackAndDefensiveTimeouts(t *testing.T) {
	t.Setenv("SERVER_BIND_ADDRESS", "127.0.0.1")

	server, err := newPublicHTTPServer(http.NewServeMux(), "3000")
	if err != nil {
		t.Fatalf("newPublicHTTPServer() error = %v", err)
	}
	if server.Addr != "127.0.0.1:3000" {
		t.Fatalf("Addr = %q, want 127.0.0.1:3000", server.Addr)
	}
	if server.ReadHeaderTimeout != 10*time.Second {
		t.Fatalf("ReadHeaderTimeout = %s, want 10s", server.ReadHeaderTimeout)
	}
	if server.ReadTimeout != 5*time.Minute {
		t.Fatalf("ReadTimeout = %s, want 5m", server.ReadTimeout)
	}
	if server.IdleTimeout != 2*time.Minute {
		t.Fatalf("IdleTimeout = %s, want 2m", server.IdleTimeout)
	}
	if server.MaxHeaderBytes != 1<<20 {
		t.Fatalf("MaxHeaderBytes = %d, want %d", server.MaxHeaderBytes, 1<<20)
	}
	if server.WriteTimeout != 0 {
		t.Fatalf("WriteTimeout = %s, want zero for streaming responses", server.WriteTimeout)
	}
}

func TestNewPublicHTTPServerRejectsInvalidBindAddress(t *testing.T) {
	t.Setenv("SERVER_BIND_ADDRESS", "127.0.0.1:9999")
	if _, err := newPublicHTTPServer(http.NewServeMux(), "3000"); err == nil {
		t.Fatal("expected invalid bind address to be rejected")
	}
}

func TestPprofServerIsLoopbackOnly(t *testing.T) {
	server := newPprofHTTPServer()
	if server.Addr != "127.0.0.1:8005" {
		t.Fatalf("pprof Addr = %q, want 127.0.0.1:8005", server.Addr)
	}
}
