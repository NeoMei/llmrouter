package router

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/internal/platformaccount"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/service/authz"
	"github.com/gin-gonic/gin"
)

var uniHubBFFRoutes = []struct {
	method string
	path   *regexp.Regexp
}{
	{"GET", regexp.MustCompile(`^/v1/(me|accounts|capabilities|invitations|products|catalog|billing-presets)$`)},
	{"POST", regexp.MustCompile(`^/v1/accounts$`)},
	{"GET", regexp.MustCompile(`^/v1/products/[A-Za-z0-9_-]+/billing-profile$`)},
	{"POST", regexp.MustCompile(`^/v1/invitations/[A-Za-z0-9_-]+/accept$`)},
	{"GET", regexp.MustCompile(`^/v1/accounts/[A-Za-z0-9_-]+/(members|invitations|subscriptions|wallets|recharges|operations|orders|settings|resources|calls|usage/summary|model-pool/calls|model-pool/usage/summary)$`)},
	{"GET", regexp.MustCompile(`^/v1/accounts/[A-Za-z0-9_-]+/(api-keys|usage-budget)$`)},
	{"POST", regexp.MustCompile(`^/v1/accounts/[A-Za-z0-9_-]+/api-keys$`)},
	{"DELETE", regexp.MustCompile(`^/v1/accounts/[A-Za-z0-9_-]+/api-keys/[A-Za-z0-9_-]+$`)},
	{"PUT", regexp.MustCompile(`^/v1/accounts/[A-Za-z0-9_-]+/usage-budget$`)},
	{"GET", regexp.MustCompile(`^/v1/accounts/[A-Za-z0-9_-]+/(recharges|operations|orders|calls|model-pool/calls)/[A-Za-z0-9_.:-]+$`)},
	{"POST", regexp.MustCompile(`^/v1/accounts/[A-Za-z0-9_-]+/(invitations|recharges|transfers|orders|quotes|model-credentials)$`)},
	{"POST", regexp.MustCompile(`^/v1/accounts/[A-Za-z0-9_-]+/(quotes/[A-Za-z0-9_-]+/accept|orders/[A-Za-z0-9_-]+/refund|recharges/[A-Za-z0-9_-]+/sandbox-settle)$`)},
	{"PATCH", regexp.MustCompile(`^/v1/accounts/[A-Za-z0-9_-]+/members/[A-Za-z0-9_-]+$`)},
	{"DELETE", regexp.MustCompile(`^/v1/accounts/[A-Za-z0-9_-]+/(members|model-credentials)/[A-Za-z0-9_-]+$`)},
	{"PUT", regexp.MustCompile(`^/v1/accounts/[A-Za-z0-9_-]+/settings$`)},
}

func uniHubCSRF(sid string) string {
	h := hmac.New(sha256.New, []byte(common.SessionSecret))
	h.Write([]byte("llmrouter/unihub/bff-csrf/v1\x00" + sid))
	return base64.RawURLEncoding.EncodeToString(h.Sum(nil))
}
func registerUniHubBFF(r *gin.Engine, config platformaccount.Config) {
	r.GET("/api/unihub/access", middleware.UserAuth(), func(c *gin.Context) {
		p, ok := authz.ProductPrincipal(c)
		identity, session := middleware.GetSessionAuthIdentity(c)
		if !config.Enabled || !ok || !session {
			c.Status(401)
			return
		}
		c.Header("Cache-Control", "no-store")
		c.JSON(200, gin.H{"access": p.Access, "permissions": gin.H{"admin_permissions": authz.RequestCapabilities(c)}, "csrf_token": uniHubCSRF(identity.SessionID)})
	})
	r.Any("/api/unihub/bff/*path", middleware.UserAuth(), func(c *gin.Context) {
		c.Header("Cache-Control", "no-store")
		identity, ok := middleware.GetSessionAuthIdentity(c)
		if !config.Enabled || !ok {
			c.Status(401)
			return
		}
		// Reject ambiguity rather than selecting a convenient header value.
		for _, h := range []string{"Authorization", "Content-Type", "Idempotency-Key", "Origin", "X-UniHub-CSRF"} {
			if len(c.Request.Header.Values(h)) > 1 {
				c.Status(400)
				return
			}
		}
		path := c.Param("path")
		allowed := false
		for _, route := range uniHubBFFRoutes {
			if route.method == c.Request.Method && route.path.MatchString(path) {
				allowed = true
				break
			}
		}
		if !allowed || strings.Contains(c.Request.URL.EscapedPath(), "%") || strings.Contains(path, "..") || len(c.Request.URL.RawQuery) > 4096 {
			c.Status(403)
			return
		}
		if c.Request.Method != http.MethodGet {
			expected := uniHubCSRF(identity.SessionID)
			if !hmac.Equal([]byte(c.GetHeader("X-UniHub-CSRF")), []byte(expected)) {
				c.Status(403)
				return
			}
			callback, _ := url.Parse(config.RedirectURI)
			if c.GetHeader("Origin") != callback.Scheme+"://"+callback.Host {
				c.Status(403)
				return
			}
		}
		// Authority is resolved again immediately before forwarding. A product block
		// stops account/finance proxy calls as well as native management.
		_, bearer, e := service.ResolveUniHubSession(c.Request.Context(), identity)
		if e != nil {
			c.Status(401)
			return
		}
		body, e := io.ReadAll(io.LimitReader(c.Request.Body, (1<<20)+1))
		if e != nil || len(body) > 1<<20 {
			c.Status(413)
			return
		}
		target := config.APIOrigin + path
		if c.Request.URL.RawQuery != "" {
			target += "?" + c.Request.URL.RawQuery
		}
		req, e := http.NewRequestWithContext(c.Request.Context(), c.Request.Method, target, bytes.NewReader(body))
		if e != nil {
			c.Status(400)
			return
		}
		req.Header.Set("Authorization", "Bearer "+bearer)
		req.Header.Set("Accept", "application/json")
		callback, _ := url.Parse(config.RedirectURI)
		req.Header.Set("Origin", callback.Scheme+"://"+callback.Host)
		for _, h := range []string{"Content-Type", "Idempotency-Key"} {
			if v := c.GetHeader(h); v != "" {
				if len(v) > 256 {
					c.Status(400)
					return
				}
				req.Header.Set(h, v)
			}
		}
		// No GetBody means the transport cannot replay a financial write on a stale
		// connection. Redirects and application retries are never performed.
		req.GetBody = nil
		if req.Body == nil || req.Body == http.NoBody {
			req.Body = io.NopCloser(bytes.NewReader(body))
		}
		client := http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		response, e := client.Do(req)
		if e != nil {
			c.Status(502)
			return
		}
		defer response.Body.Close()
		if response.StatusCode >= 300 && response.StatusCode < 400 {
			c.Status(502)
			return
		}
		raw, e := io.ReadAll(io.LimitReader(response.Body, (4<<20)+1))
		if e != nil || len(raw) > 4<<20 {
			c.Status(502)
			return
		}
		if v := response.Header.Get("Retry-After"); v != "" {
			c.Header("Retry-After", v)
		}
		c.Data(response.StatusCode, "application/json", raw)
	})
}
