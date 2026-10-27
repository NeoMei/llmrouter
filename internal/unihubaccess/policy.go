// Package unihubaccess defines the public/native boundary for LLMRouter.
package unihubaccess

import (
	"errors"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
)

type Policy struct {
	PublicEnabled       bool
	NativePublicEnabled bool
	PlatformEnabled     bool
	privateListener     string
}

func strictBool(name string, required bool) (bool, error) {
	raw, ok := os.LookupEnv(name)
	if !ok || strings.TrimSpace(raw) == "" {
		if required {
			return false, errors.New("missing " + name)
		}
		return false, nil
	}
	switch strings.TrimSpace(raw) {
	case "true":
		return true, nil
	case "false":
		return false, nil
	default:
		return false, errors.New("invalid " + name)
	}
}

// FromEnv parses the deployment boundary. Boolean values are intentionally
// strict and unknown values fail closed instead of silently changing exposure.
func FromEnv() (Policy, error) {
	product, err := strictBool("UNIHUB_PRODUCT_ENABLED", true)
	if err != nil {
		return Policy{}, err
	}
	platform, err := strictBool("UNIHUB_PLATFORM_ENABLED", true)
	if err != nil {
		return Policy{}, err
	}
	native, err := strictBool("NATIVE_PUBLIC_ACCESS", true)
	if err != nil {
		return Policy{}, err
	}
	p := Policy{PublicEnabled: product, NativePublicEnabled: native, PlatformEnabled: platform}
	// Only the transport's local socket address can designate the private entry.
	// Forwarded headers and browser origins are never authorization evidence.
	p.privateListener = strings.TrimSpace(os.Getenv("NATIVE_ADMIN_LISTEN_ADDRESS"))
	if p.privateListener != "" {
		host, port, err := net.SplitHostPort(p.privateListener)
		ip := net.ParseIP(host)
		n, portErr := strconv.Atoi(port)
		if err != nil || ip == nil || !ip.IsLoopback() || portErr != nil || n < 1 || n > 65535 {
			return Policy{}, errors.New("private management listener must be a loopback IP and explicit port")
		}
		p.privateListener = net.JoinHostPort(ip.String(), strconv.Itoa(n))
	}
	if (product || platform) && p.privateListener == "" {
		return Policy{}, errors.New("UniHub authority requires a private management listener")
	}
	return p, nil
}

// IsPrivateManagementRequest trusts only net/http's server-populated local
// socket address. The private listener must never be a public proxy upstream.
// This grants boundary access only; native login/session checks still apply.
func (p Policy) IsPrivateManagementRequest(r *http.Request) bool {
	if r == nil || p.privateListener == "" {
		return false
	}
	addr, ok := r.Context().Value(http.LocalAddrContextKey).(net.Addr)
	return ok && addr.Network() == "tcp" && addr.String() == p.privateListener
}

// PrivateListenAddress is validated and normalized for startup binding.
func (p Policy) PrivateListenAddress() string { return p.privateListener }
