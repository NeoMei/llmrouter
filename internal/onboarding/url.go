package onboarding

import (
	"fmt"
	"net"
	"net/url"
	"strings"
)

// NormalizeServerURL validates a gateway origin and removes an optional /v1 suffix.
func NormalizeServerURL(raw string) (*url.URL, error) {
	if hasControl(raw) {
		return nil, fmt.Errorf("%w: server URL contains control characters", ErrUsage)
	}
	u, err := url.ParseRequestURI(raw)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid server URL", ErrUsage)
	}
	if u.Scheme != "https" && u.Scheme != "http" {
		return nil, fmt.Errorf("%w: server URL must use HTTPS or loopback HTTP", ErrUsage)
	}
	if u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("%w: server URL must be an origin", ErrUsage)
	}
	if u.Scheme == "http" && !isLoopbackHost(u.Hostname()) {
		return nil, fmt.Errorf("%w: HTTP server URL must be loopback", ErrUsage)
	}
	if u.Path != "" && u.Path != "/" && u.Path != "/v1" && u.Path != "/v1/" {
		return nil, fmt.Errorf("%w: server URL path must be /v1", ErrUsage)
	}
	return &url.URL{Scheme: u.Scheme, Host: u.Host}, nil
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
