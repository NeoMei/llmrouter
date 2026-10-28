package nativespeech

import (
	"context"
	"crypto/tls"
	"errors"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

// New accepts server-owned configuration. It cannot authorize calls: admission,
// immutable tariffs, attempt journaling and dispatch fences remain upstream.
func New(target Target) (*Producer, error) { return newProducer(target, false) }

// The loopback seam is deliberately unexported and used only by controlled tests.
func newProducer(target Target, allowLoopback bool) (*Producer, error) {
	policy, err := PolicyForModel(target.Policy.Provider, target.Policy.Model)
	if err != nil || policy != target.Policy {
		return nil, errInvalidRequest
	}
	if strings.TrimSpace(target.Credential) == "" || len(target.Credential) > 4096 || strings.ContainsAny(target.Credential, "\r\n") {
		return nil, errInvalidRequest
	}
	if target.Limits.MaxAudioBytes == 0 {
		target.Limits.MaxAudioBytes = 32 << 20
	}
	if target.Limits.MaxAudioSeconds == 0 {
		target.Limits.MaxAudioSeconds = 600
	}
	if target.Limits.TimeoutSeconds == 0 {
		target.Limits.TimeoutSeconds = 120
	}
	l := target.Limits
	if l.MaxAudioBytes < 1 || l.MaxAudioBytes > 32<<20 || l.MaxAudioSeconds < 1 || l.MaxAudioSeconds > 600 || l.TimeoutSeconds < 1 || l.TimeoutSeconds > 120 {
		return nil, errInvalidRequest
	}
	origin := "https://dashscope.aliyuncs.com"
	if policy.Provider == "elevenlabs" {
		origin = "https://api.elevenlabs.io"
	}
	if target.Origin == "" {
		target.Origin = origin
	}
	if !allowLoopback && target.Origin != origin {
		return nil, errInvalidRequest
	}
	endpoint, e := url.Parse(target.Origin)
	if e != nil || endpoint.Host == "" || endpoint.User != nil || endpoint.Path != "" || endpoint.RawQuery != "" || endpoint.Fragment != "" {
		return nil, errInvalidRequest
	}
	if allowLoopback {
		ip := net.ParseIP(endpoint.Hostname())
		if endpoint.Scheme != "http" || ip == nil || !ip.IsLoopback() {
			return nil, errInvalidRequest
		}
	} else if endpoint.Scheme != "https" {
		return nil, errInvalidRequest
	}
	if policy.Provider == "bailian" && len(target.ResourceOrigins) == 0 && !allowLoopback {
		target.ResourceOrigins = []string{"https://dashscope-result-bj.oss-cn-beijing.aliyuncs.com", "https://dashscope-result-wlcb.oss-cn-wulanchabu.aliyuncs.com"}
	}
	if len(target.ResourceOrigins) > 8 {
		return nil, errInvalidRequest
	}
	target.ResourceOrigins = append([]string(nil), target.ResourceOrigins...)
	allowed := map[string]bool{endpoint.Host: true}
	for _, raw := range target.ResourceOrigins {
		u, e := url.Parse(raw)
		if e != nil || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
			return nil, errInvalidRequest
		}
		if allowLoopback {
			ip := net.ParseIP(u.Hostname())
			if u.Scheme != "http" || ip == nil || !ip.IsLoopback() {
				return nil, errInvalidRequest
			}
		} else {
			if u.Scheme != "https" || u.Port() != "" || (u.Hostname() != "dashscope-result-bj.oss-cn-beijing.aliyuncs.com" && u.Hostname() != "dashscope-result-wlcb.oss-cn-wulanchabu.aliyuncs.com") {
				return nil, errInvalidRequest
			}
		}
		allowed[u.Host] = true
	}
	transport := &http.Transport{
		Proxy: nil, DisableKeepAlives: true, DisableCompression: true, MaxResponseHeaderBytes: 16 << 10,
		TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}, TLSHandshakeTimeout: 10 * time.Second,
		ResponseHeaderTimeout: time.Duration(l.TimeoutSeconds) * time.Second,
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			host, port, e := net.SplitHostPort(address)
			if e != nil {
				return nil, errResponse
			}
			authority := host
			if port != "443" {
				authority = net.JoinHostPort(host, port)
			}
			if !allowed[authority] {
				return nil, errResponse
			}
			ips, e := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
			if e != nil || len(ips) == 0 {
				return nil, errResponse
			}
			for _, ip := range ips {
				if !publicIP(ip) && !(allowLoopback && ip.IsLoopback()) {
					return nil, errResponse
				}
			}
			// Pin the validated address directly; DNS is not resolved again by Dialer.
			dialer := net.Dialer{Timeout: 10 * time.Second}
			return dialer.DialContext(ctx, network, net.JoinHostPort(ips[0].String(), port))
		},
	}
	return &Producer{target: target, allowLoopback: allowLoopback, client: &http.Client{Transport: transport, Timeout: time.Duration(l.TimeoutSeconds) * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("native speech redirects denied") }}}, nil
}

func publicIP(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
		return false
	}
	for _, prefix := range []string{"0.0.0.0/8", "100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4", "2001:db8::/32", "64:ff9b::/96", "64:ff9b:1::/48", "2002::/16", "2001::/32"} {
		if netip.MustParsePrefix(prefix).Contains(ip) {
			return false
		}
	}
	return true
}
func (p *Producer) resourceURL(raw string) (string, error) {
	if len(raw) > 8192 {
		return "", errResponse
	}
	u, e := url.Parse(raw)
	if e != nil || u.Host == "" || u.User != nil || u.Fragment != "" || u.Opaque != "" {
		return "", errResponse
	}
	if !p.allowLoopback && u.Scheme == "http" {
		u.Scheme = "https"
	}
	origin := u.Scheme + "://" + u.Host
	for _, allowed := range p.target.ResourceOrigins {
		if origin == allowed {
			return u.String(), nil
		}
	}
	return "", errResponse
}
func contentType(h http.Header, allowed ...string) bool {
	values := h.Values("Content-Type")
	if len(values) != 1 {
		return false
	}
	kind, _, e := mime.ParseMediaType(values[0])
	if e != nil {
		return false
	}
	for _, v := range allowed {
		if kind == v {
			return true
		}
	}
	return false
}

// NewWithTransport accepts a trusted in-process transport dependency. Target
// still passes New's exact production-origin and resource allowlist validation.
// This is not exposed through deployment JSON, request fields or environment.
func NewWithTransport(target Target, transport http.RoundTripper) (*Producer, error) {
	p, err := New(target)
	if err != nil {
		return nil, err
	}
	if transport == nil {
		return nil, errInvalidRequest
	}
	p.client.Transport = transport
	return p, nil
}
