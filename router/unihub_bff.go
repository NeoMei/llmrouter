package router

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/controller"
	"github.com/QuantumNous/new-api/internal/platformaccount"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
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
	{"GET", regexp.MustCompile(`^/v1/products/[A-Za-z0-9_-]+/(billing-profile|model-consistency)$`)},
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
		if (!config.Enabled && !platformaccount.RuntimeEnabled()) || !ok || !session {
			c.Status(401)
			return
		}
		c.Header("Cache-Control", "no-store")
		user, e := model.GetUserById(identity.UserID, false)
		if e != nil {
			c.Status(503)
			return
		}
		user.Role = middleware.RoleFromAccess(p.Access)
		if e = model.RefreshProductBalance(c.Request.Context(), user); e != nil {
			c.Status(503)
			return
		}
		userData := controller.CurrentProductUserData(c, user)
		userData["permissions"] = gin.H{"admin_permissions": authz.RequestCapabilities(c)}
		financeAccess := gin.H{"available": false}
		current, _, e := service.ValidateLoginSession(identity)
		if e == nil && current.LoginMethod == "unihub" && config.Enabled {
			oidc, _, resolveErr := service.ResolveUniHubSession(c.Request.Context(), identity)
			if resolveErr == nil && oidc.Access.ExpiresAt > time.Now().Unix() && oidc.Access.Subject == p.Access.Subject && oidc.Access.Issuer == p.Access.Issuer && oidc.Access.ProductID == p.Access.ProductID && oidc.Access.InstanceID == p.Access.InstanceID {
				financeAccess = gin.H{"available": true, "transport": "oidc", "central_user_id": oidc.Access.Subject, "expires_at": min(oidc.Access.ExpiresAt, p.Access.ExpiresAt, current.ExpiresAt)}
			}
		} else if e == nil && platformaccount.FinanceEnabled() {
			proof, proofErr := runtimeFinanceActor(c.Request.Context(), identity, p, config)
			client, clientErr := platformaccount.FinanceClient(config)
			if proofErr == nil && clientErr == nil {
				expires := min(proof.ActorExpiresAt, proof.Access.ExpiresAt, p.Access.ExpiresAt, current.ExpiresAt)
				if current.LoginMethod == "product_broker" {
					bound, _, sourceErr := model.OpenBrokerSourceProof(identity.SessionID, identity.UserID, p.Access.Subject, config)
					if sourceErr != nil {
						proofErr = sourceErr
					} else {
						expires = min(expires, bound.ExpiresAt)
					}
				}
				if proofErr == nil {
					result, readErr := client.Do(c.Request.Context(), http.MethodGet, "/me", nil, proof.ActorToken, nil, "")
					var central struct {
						ID string `json:"id"`
					}
					_, _, sessionErr := service.ValidateLoginSession(identity)
					if readErr == nil && result.StatusCode == http.StatusOK && common.UnmarshalUnique(result.Body, &central) == nil && central.ID == p.Access.Subject && expires > time.Now().Unix() && sessionErr == nil {
						financeAccess = gin.H{"available": true, "transport": "runtime_actor", "central_user_id": central.ID, "expires_at": expires}
						// Read readiness does not imply write authority: the center reports
						// current service capability separately from registered client scopes.
						capsResult, capsErr := client.Do(c.Request.Context(), http.MethodGet, "/capabilities", nil, proof.ActorToken, nil, "")
						var caps struct {
							Kind         string   `json:"kind"`
							ProductID    string   `json:"product_id"`
							InstanceID   string   `json:"instance_id"`
							ClientID     string   `json:"client_id"`
							FinanceWrite bool     `json:"finance_write"`
							Scopes       []string `json:"scopes"`
						}
						writeScopes := []string{}
						if capsErr == nil && capsResult.StatusCode == http.StatusOK && common.UnmarshalUnique(capsResult.Body, &caps) == nil && caps.Kind == "product" && caps.ProductID == config.ProductID && caps.InstanceID == config.InstanceID && caps.ClientID != "" && caps.FinanceWrite {
							for _, scope := range caps.Scopes {
								if scope == "model-credentials:write" || scope == "settings:write" || scope == "recharges:write" {
									writeScopes = append(writeScopes, scope)
								}
							}
						}
						financeAccess["write_scopes"] = writeScopes
					}
				}
			}
		}
		if _, _, err := service.ValidateLoginSession(identity); err != nil || p.Access.ExpiresAt <= time.Now().Unix() {
			c.Status(http.StatusUnauthorized)
			return
		}
		if err := service.VerifyBrokerSource(c.Request.Context(), identity); err != nil {
			if errors.Is(err, service.ErrLoginSessionRevoked) {
				c.Status(http.StatusUnauthorized)
			} else {
				c.Status(http.StatusServiceUnavailable)
			}
			return
		}
		if expires, ok := financeAccess["expires_at"].(int64); ok && expires <= time.Now().Unix() {
			financeAccess = gin.H{"available": false}
		}
		c.JSON(200, gin.H{"user": userData, "access": p.Access, "permissions": gin.H{"admin_permissions": authz.RequestCapabilities(c)}, "csrf_token": uniHubCSRF(identity.SessionID), "finance_access": financeAccess})
	})
	r.Any("/api/unihub/bff/*path", middleware.UserAuth(), func(c *gin.Context) {
		c.Header("Cache-Control", "no-store")
		identity, ok := middleware.GetSessionAuthIdentity(c)
		if !ok {
			c.Status(401)
			return
		}
		// Reject ambiguity rather than selecting a convenient header value.
		for _, h := range []string{"Authorization", "Content-Type", "Idempotency-Key", "Origin", "X-UniHub-CSRF", "X-UniHub-Actor-Authorization"} {
			if len(c.Request.Header.Values(h)) > 1 {
				c.Status(400)
				return
			}
		}
		if len(c.Request.Header.Values("X-UniHub-Actor-Authorization")) != 0 {
			c.Status(403)
			return
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
		body, e := io.ReadAll(io.LimitReader(c.Request.Body, (1<<20)+1))
		if e != nil || len(body) > 1<<20 {
			c.Status(413)
			return
		}
		session, _, e := service.ValidateLoginSession(identity)
		if e != nil {
			c.Status(http.StatusUnauthorized)
			return
		}
		if session.LoginMethod != "unihub" {
			if !platformaccount.FinanceEnabled() || !runtimeFinanceRoute(c.Request.Method, path) {
				c.Status(http.StatusForbidden)
				return
			}
			principal, present := authz.ProductPrincipal(c)
			if !present {
				c.Status(http.StatusUnauthorized)
				return
			}
			proof, proofErr := runtimeFinanceActor(c.Request.Context(), identity, principal, config)
			client, clientErr := platformaccount.FinanceClient(config)
			if proofErr != nil || clientErr != nil {
				c.Status(http.StatusServiceUnavailable)
				return
			}
			query, queryErr := url.ParseQuery(c.Request.URL.RawQuery)
			if queryErr != nil {
				c.Status(http.StatusBadRequest)
				return
			}
			result, sendErr := client.Do(c.Request.Context(), c.Request.Method, strings.TrimPrefix(path, "/v1"), query, proof.ActorToken, body, c.GetHeader("Idempotency-Key"))
			if sendErr != nil {
				c.Status(http.StatusBadGateway)
				return
			}
			_, _, sessionErr := service.ValidateLoginSession(identity)
			sourceErr := service.VerifyBrokerSource(c.Request.Context(), identity)
			if sessionErr != nil || sourceErr != nil || proof.ActorExpiresAt <= time.Now().Unix() || proof.Access.ExpiresAt <= time.Now().Unix() || model.VerifyUniHubIdentityBinding(identity.UserID, proof.Access.Issuer, proof.Access.Subject) != nil {
				if c.Request.Method == http.MethodGet {
					if sourceErr != nil && !errors.Is(sourceErr, service.ErrLoginSessionRevoked) {
						c.Status(http.StatusServiceUnavailable)
					} else {
						c.Status(http.StatusUnauthorized)
					}
				} else {
					// The central operation may already have committed. Never claim
					// it failed or expose a secret after the session was revoked.
					c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{"error": "finance_result_unavailable"})
				}
				return
			}
			c.Data(result.StatusCode, "application/json", result.Body)
			return
		}
		if !config.Enabled {
			c.Status(http.StatusUnauthorized)
			return
		}
		// OIDC sessions retain the original user-bearer authorization lane.
		_, bearer, e := service.ResolveUniHubSession(c.Request.Context(), identity)
		if e != nil {
			c.Status(http.StatusUnauthorized)
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
