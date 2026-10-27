package main

import (
	"fmt"
	"github.com/QuantumNous/new-api/internal/unihubaccess"
	"net"
	"net/http"
	"net/http/pprof"
	"os"
	"strconv"
	"strings"
	"time"
)

func newPublicHTTPServer(handler http.Handler, port string) (*http.Server, error) {
	portNumber, err := strconv.Atoi(strings.TrimSpace(port))
	if err != nil || portNumber < 1 || portNumber > 65535 {
		return nil, fmt.Errorf("invalid server port %q", port)
	}
	host := strings.TrimSpace(os.Getenv("SERVER_BIND_ADDRESS"))
	if host != "" && net.ParseIP(host) == nil {
		return nil, fmt.Errorf("SERVER_BIND_ADDRESS must be an IP address")
	}
	return &http.Server{
		Addr:              net.JoinHostPort(host, strconv.Itoa(portNumber)),
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       5 * time.Minute,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    1 << 20,
	}, nil
}

func newPprofHTTPServer() *http.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	return &http.Server{
		Addr:              "127.0.0.1:8005",
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       30 * time.Second,
		MaxHeaderBytes:    64 << 10,
	}
}

// newPrivateHTTPServer shares the application/session handler but binds a
// separate loopback port. Never configure a public proxy to use this port.
func newPrivateHTTPServer(public *http.Server) (*http.Server, error) {
	if os.Getenv("NATIVE_ADMIN_LISTEN_ADDRESS") == "" {
		if os.Getenv("UNIHUB_PRODUCT_ENABLED") == "true" || os.Getenv("UNIHUB_PLATFORM_ENABLED") == "true" {
			return nil, fmt.Errorf("UniHub authority requires NATIVE_ADMIN_LISTEN_ADDRESS")
		}
		return nil, nil
	}
	policy, err := unihubaccess.FromEnv()
	if err != nil {
		return nil, err
	}
	_, publicPort, err := net.SplitHostPort(public.Addr)
	if err != nil {
		return nil, err
	}
	_, privatePort, _ := net.SplitHostPort(policy.PrivateListenAddress())
	if publicPort == privatePort {
		return nil, fmt.Errorf("private management must use a separate port")
	}
	return &http.Server{Addr: policy.PrivateListenAddress(), Handler: public.Handler, ReadHeaderTimeout: public.ReadHeaderTimeout, ReadTimeout: public.ReadTimeout, IdleTimeout: public.IdleTimeout, MaxHeaderBytes: public.MaxHeaderBytes}, nil
}
