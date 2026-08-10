package middleware

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
)

const trustedOriginsEnv = "TRUSTED_ORIGINS"

// trustedOrigins parses a comma-separated list of origins, trimming whitespace
// and ignoring empty entries.
func trustedOrigins(raw string) []string {
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		out = append(out, p)
	}
	return out
}

// ValidateCORSConfig validates the configured trusted origins. In production
// it requires at least one HTTPS origin and rejects wildcard credentials.
func ValidateCORSConfig() error {
	origins := trustedOrigins(os.Getenv(trustedOriginsEnv))
	isProduction := strings.EqualFold(os.Getenv("RUN_MODE"), "production")
	if isProduction && len(origins) == 0 {
		return errors.New("TRUSTED_ORIGINS must contain at least one HTTPS origin in production")
	}
	for _, origin := range origins {
		if origin == "*" {
			if isProduction {
				return errors.New("TRUSTED_ORIGINS must not be wildcard in production when credentials are enabled")
			}
			continue
		}
		u, err := url.Parse(origin)
		if err != nil {
			return fmt.Errorf("invalid trusted origin %q: %w", origin, err)
		}
		if u.Scheme != "https" && u.Scheme != "http" {
			return fmt.Errorf("invalid trusted origin %q: scheme must be https or http", origin)
		}
		if u.Host == "" {
			return fmt.Errorf("invalid trusted origin %q: missing host", origin)
		}
		if u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
			return fmt.Errorf("invalid trusted origin %q: must contain only scheme and host", origin)
		}
		if isProduction && u.Scheme != "https" {
			return fmt.Errorf("production TRUSTED_ORIGINS origin %q must use HTTPS", origin)
		}
	}
	return nil
}

// CORS returns a CORS middleware that restricts credentialed requests to the
// exact origins configured in TRUSTED_ORIGINS. When no origins are configured it
// falls back to the previous permissive behaviour for local development only.
func CORS() gin.HandlerFunc {
	origins := trustedOrigins(os.Getenv(trustedOriginsEnv))
	if len(origins) == 0 {
		config := cors.DefaultConfig()
		config.AllowAllOrigins = true
		config.AllowCredentials = true
		config.AllowMethods = []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"}
		config.AllowHeaders = []string{"*"}
		return cors.New(config)
	}

	config := cors.DefaultConfig()
	config.AllowOrigins = origins
	config.AllowCredentials = true
	config.AllowMethods = []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"}
	config.AllowHeaders = []string{
		"Authorization",
		"Content-Type",
		"Anthropic-Version",
		"Anthropic-Beta",
		"OpenAI-Beta",
		"X-Request-Id",
	}
	return cors.New(config)
}

func Version() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("X-New-Api-Version", common.Version)
		c.Next()
	}
}
