package main

import (
	"github.com/QuantumNous/new-api/internal/unihubaccess"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"net"
	"net/http"
	"net/http/httptest"
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

func TestPrivateHTTPServerSharesHandlerAndRequiresSeparateLoopbackPort(t *testing.T) {
	t.Setenv("UNIHUB_PRODUCT_ENABLED", "true")
	t.Setenv("UNIHUB_PLATFORM_ENABLED", "true")
	t.Setenv("NATIVE_PUBLIC_ACCESS", "false")
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })
	public := &http.Server{Addr: "127.0.0.1:3000", Handler: handler}
	for _, addr := range []string{"", "0.0.0.0:3001", "127.0.0.1:3000"} {
		t.Setenv("NATIVE_ADMIN_LISTEN_ADDRESS", addr)
		_, err := newPrivateHTTPServer(public)
		require.Error(t, err)
	}
	t.Setenv("NATIVE_ADMIN_LISTEN_ADDRESS", "127.0.0.1:3001")
	private, err := newPrivateHTTPServer(public)
	require.NoError(t, err)
	assert.Equal(t, "127.0.0.1:3001", private.Addr)
	w := httptest.NewRecorder()
	private.Handler.ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
	assert.Equal(t, 204, w.Code)
}

func TestPrivateListenerProvidesTransportAuthority(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer listener.Close()
	t.Setenv("UNIHUB_PRODUCT_ENABLED", "true")
	t.Setenv("UNIHUB_PLATFORM_ENABLED", "true")
	t.Setenv("NATIVE_PUBLIC_ACCESS", "false")
	t.Setenv("NATIVE_ADMIN_LISTEN_ADDRESS", listener.Addr().String())
	policy, err := unihubaccess.FromEnv()
	require.NoError(t, err)
	public := &http.Server{Addr: "127.0.0.1:1", Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if policy.IsPrivateManagementRequest(r) {
			w.WriteHeader(204)
		} else {
			w.WriteHeader(403)
		}
	})}
	private, err := newPrivateHTTPServer(public)
	require.NoError(t, err)
	done := make(chan error, 1)
	go func() { done <- private.Serve(listener) }()
	defer func() { require.NoError(t, private.Close()); require.ErrorIs(t, <-done, http.ErrServerClosed) }()
	response, err := http.Get("http://" + listener.Addr().String())
	require.NoError(t, err)
	defer response.Body.Close()
	assert.Equal(t, 204, response.StatusCode)
	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "http://public/", nil)
	req.Header.Set("X-Forwarded-Host", listener.Addr().String())
	public.Handler.ServeHTTP(w, req)
	assert.Equal(t, 403, w.Code)
}
