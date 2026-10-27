// Package unihubaccess defines the public/native boundary for LLMRouter.
package unihubaccess

import (
	"crypto/subtle"
	"errors"
	"net/http"
	"net/url"
	"os"
	"strings"
)

type Policy struct {
	PublicEnabled       bool
	NativePublicEnabled bool
	privateOrigins      []string
	managementToken     string
	trustedProxy        string
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

func normalizeOrigin(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme == "" || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("invalid private management origin")
	}
	return strings.ToLower(u.Scheme + "://" + u.Host), nil
}

// FromEnv parses the deployment boundary. Boolean values are intentionally
// strict and unknown values fail closed instead of silently changing exposure.
func FromEnv() (Policy, error) {
	product, err := strictBool("UNIHUB_PRODUCT_ENABLED", false)
	if err != nil {
		return Policy{}, err
	}
	platform, err := strictBool("UNIHUB_PLATFORM_ENABLED", product)
	if err != nil {
		return Policy{}, err
	}
	native, err := strictBool("NATIVE_PUBLIC_ACCESS", product)
	if err != nil {
		return Policy{}, err
	}
	p := Policy{PublicEnabled: product, NativePublicEnabled: native}
	_ = platform // parsed strictly because platform enablement is part of the deployment contract
	rawOrigins := os.Getenv("NATIVE_ADMIN_ORIGIN")
	if strings.TrimSpace(rawOrigins) != "" {
		for _, raw := range strings.Split(rawOrigins, ",") {
			origin, e := normalizeOrigin(raw)
			if e != nil {
				return Policy{}, e
			}
			p.privateOrigins = append(p.privateOrigins, origin)
		}
	}
	p.managementToken = strings.TrimSpace(os.Getenv("MANAGEMENT_TOKEN"))
	p.trustedProxy = strings.TrimSpace(os.Getenv("NATIVE_ADMIN_PROXY"))
	if p.PublicEnabled && len(p.privateOrigins) == 0 && p.managementToken == "" {
		// A public deployment must retain an explicit private escape hatch.
		return Policy{}, errors.New("private management policy is not configured")
	}
	return p, nil
}

func (p Policy) IsPrivateManagementRequest(r *http.Request) bool {
	if r == nil {
		return false
	}
	if p.managementToken != "" {
		parts := strings.Fields(r.Header.Get("Authorization"))
		if len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") && subtle.ConstantTimeCompare([]byte(parts[1]), []byte(p.managementToken)) == 1 {
			return true
		}
	}
	if len(p.privateOrigins) == 0 {
		return false
	}
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		if ref := strings.TrimSpace(r.Header.Get("Referer")); ref != "" {
			if u, err := url.Parse(ref); err == nil {
				origin = u.Scheme + "://" + u.Host
			}
		}
	}
	normalized, err := normalizeOrigin(origin)
	if err != nil {
		return false
	}
	for _, trusted := range p.privateOrigins {
		if subtle.ConstantTimeCompare([]byte(normalized), []byte(trusted)) == 1 {
			if p.trustedProxy == "" || subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Forwarded-Host")), []byte(p.trustedProxy)) == 1 {
				return true
			}
		}
	}
	return false
}
