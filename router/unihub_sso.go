package router

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	userkit "github.com/NeoMei/UniHub/packages/userkit-go"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/internal/platformaccount"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"golang.org/x/sync/singleflight"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"
)

func registerUniHubSSO(r *gin.Engine, config platformaccount.Config) {
	broker := newProductBrowserBridge(r, config)
	var initialization singleflight.Group
	var browserMu sync.RWMutex
	var browser *userkit.Browser
	type loginReturn struct {
		binding     [32]byte
		destination string
		silent      bool
		expires     time.Time
	}
	var returnsMu sync.Mutex
	returns := map[string]loginReturn{}
	fallback := func(c *gin.Context, record loginReturn) {
		productBrowserFailure(c, record.destination, record.silent)
	}

	r.GET("/api/unihub/login", func(c *gin.Context) {
		c.Header("Cache-Control", "no-store")
		destination := c.Query("return_to")
		if destination == "" {
			destination = "/dashboard"
		}
		silent := c.Query("prompt") == "none"
		record := loginReturn{destination: destination, silent: silent}
		if c.Query("prompt") != "" && !silent {
			c.Status(400)
			return
		}
		if broker.begin(c, destination) {
			return
		}
		if !config.Enabled || config.OIDCIssuer == "" {
			fallback(c, record)
			return
		}
		returnURL, returnError := url.Parse(destination)
		if !userkit.SafeReturn(destination) || returnError != nil || !middleware.IsProductApplicationPath(returnURL.Path) {
			c.Status(http.StatusBadRequest)
			return
		}
		browserMu.RLock()
		activeBrowser := browser
		browserMu.RUnlock()
		if activeBrowser == nil {
			result := initialization.DoChan("browser", func() (any, error) {
				browserMu.RLock()
				cached := browser
				browserMu.RUnlock()
				if cached != nil {
					return cached, nil
				}
				// Share one bounded discovery attempt; a failed attempt is not cached.
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				discovered, err := userkit.NewBrowser(ctx, userkit.BrowserConfig{Issuer: config.OIDCIssuer, ClientID: config.ClientID, ClientSecret: config.ClientSecret, RedirectURI: config.RedirectURI, AccessTokenType: "access-token"})
				if err != nil {
					return nil, err
				}
				browserMu.Lock()
				browser = discovered
				browserMu.Unlock()
				return discovered, nil
			})
			select {
			case initialized := <-result:
				if initialized.Err != nil {
					fallback(c, record)
					return
				}
				activeBrowser = initialized.Val.(*userkit.Browser)
			case <-c.Request.Context().Done():
				fallback(c, record)
				return
			}
		}
		binding, e := common.GenerateRandomCharsKey(48)
		if e != nil {
			c.Status(500)
			return
		}
		authorize, e := activeBrowser.Begin(binding, destination)
		if e != nil {
			c.Status(400)
			return
		}
		authorizationURL, parseError := url.Parse(authorize)
		if parseError != nil {
			c.Status(503)
			return
		}
		state := authorizationURL.Query().Get("state")
		if state == "" {
			c.Status(503)
			return
		}
		record.binding = sha256.Sum256([]byte(binding))
		record.expires = time.Now().Add(5 * time.Minute)
		returnsMu.Lock()
		for key, value := range returns {
			if time.Now().After(value.expires) {
				delete(returns, key)
			}
		}
		if len(returns) >= 4096 {
			returnsMu.Unlock()
			c.Status(429)
			return
		}
		returns[state] = record
		returnsMu.Unlock()
		if silent {
			q := authorizationURL.Query()
			q.Set("prompt", "none")
			authorizationURL.RawQuery = q.Encode()
			authorize = authorizationURL.String()
		}
		callback, _ := url.Parse(config.RedirectURI)
		http.SetCookie(c.Writer, &http.Cookie{Name: "unihub_oidc_binding", Value: binding, Path: callback.Path, MaxAge: 300, HttpOnly: true, Secure: callback.Scheme == "https", SameSite: http.SameSiteLaxMode})
		c.Redirect(303, authorize)
	})
	r.GET("/api/unihub/callback", func(c *gin.Context) {
		if broker.callback(c) {
			return
		}
		c.Header("Cache-Control", "no-store")
		c.Header("Referrer-Policy", "no-referrer")
		record := loginReturn{destination: "/dashboard", silent: true}
		observedBinding, _ := c.Cookie("unihub_oidc_binding")
		observedHash := sha256.Sum256([]byte(observedBinding))
		returnsMu.Lock()
		stored, exists := returns[c.Query("state")]
		if exists && time.Now().Before(stored.expires) && subtle.ConstantTimeCompare(stored.binding[:], observedHash[:]) == 1 {
			record = stored
			delete(returns, c.Query("state"))
		}
		returnsMu.Unlock()

		browserMu.RLock()
		activeBrowser := browser
		browserMu.RUnlock()
		if activeBrowser == nil || !config.Enabled {
			fallback(c, record)
			return
		}
		binding, e := c.Cookie("unihub_oidc_binding")
		if e != nil {
			fallback(c, record)
			return
		}
		callback, _ := url.Parse(config.RedirectURI)
		http.SetCookie(c.Writer, &http.Cookie{Name: "unihub_oidc_binding", Path: callback.Path, MaxAge: -1, HttpOnly: true, Secure: callback.Scheme == "https", SameSite: http.SameSiteLaxMode})
		// The configured origin is trusted; the observed callback path/query must match.
		if c.Request.Host != callback.Host {
			fallback(c, record)
			return
		}
		absolute := callback.Scheme + "://" + callback.Host + c.Request.URL.RequestURI()
		proof, e := activeBrowser.Complete(c.Request.Context(), binding, absolute)
		if e != nil {
			fallback(c, record)
			return
		}
		access, e := config.Resolve(c.Request.Context(), proof.AccessToken)
		if e != nil {
			fallback(c, record)
			return
		}
		if proof.ExpiresAt < access.ExpiresAt {
			access.ExpiresAt = proof.ExpiresAt
		}
		bundle, e := createUniHubSession(c, access, proof.AccessToken)
		if e != nil {
			fallback(c, record)
			return
		}
		service.WriteRefreshCookie(c, bundle.RefreshToken)
		c.Redirect(303, returnToWithSession(proof.ReturnTo, bundle.Session.SID))
	})
}
func createUniHubSession(c *gin.Context, a userkit.Access, bearer string) (*service.AuthBundle, error) {
	local, e := (model.UniHubProvisioner{}).Ensure(c.Request.Context(), a)
	if e != nil {
		logger.LogWarn(c.Request.Context(), "unihub_session_failure stage=projection")
		return nil, e
	}
	userID, e := strconv.Atoi(local.ID)
	if e != nil {
		logger.LogWarn(c.Request.Context(), "unihub_session_failure stage=projection")
		return nil, e
	}
	if platformaccount.RuntimeEnabled() {
		client, err := platformaccount.RuntimeClient()
		if err != nil {
			logger.LogWarn(c.Request.Context(), "unihub_session_failure stage=runtime_config")
			return nil, err
		}
		if _, err = client.Bind(c.Request.Context(), bearer, userkit.BindRequest{LocalUserID: local.ID, BindingKey: "oidc:" + local.ID}); err != nil {
			logger.LogWarn(c.Request.Context(), "unihub_session_failure stage=runtime_bind")
			return nil, err
		}
		owner, err := model.GetUserById(userID, false)
		if err != nil {
			return nil, err
		}
		if owner.FundingRealm != model.NativeFundingLocalOnly {
			if err = reconcileSSOUser(c, userID); err != nil {
				logger.LogWarn(c.Request.Context(), "unihub_session_failure stage=runtime_identity")
				return nil, err
			}
			if err = model.EnsureProductFunding(c.Request.Context(), userID); err != nil && !errors.Is(err, model.ErrProductFundingMigration) {
				logger.LogWarn(c.Request.Context(), "unihub_session_failure stage=runtime_funding")
				return nil, err
			}
		}
	}
	owner, e := model.GetUserById(userID, false)
	if e != nil {
		return nil, e
	}
	var bundle *service.AuthBundle
	if owner.FundingRealm == model.NativeFundingLocalOnly {
		bundle, e = service.CreateCentralSessionFromVerifiedSource(c.Request.Context(), userID, a, "unihub", a.ExpiresAt, c.ClientIP(), c.Request.UserAgent())
	} else {
		bundle, e = service.CreateUniHubLoginSession(userID, a.ExpiresAt, c.ClientIP(), c.Request.UserAgent())
	}
	if e != nil {
		logger.LogWarn(c.Request.Context(), "unihub_session_failure stage=session_create")
		return nil, e
	}
	if e = model.BindUniHubSession(bundle.Session.SID, userID, a, bearer, a.ExpiresAt); e != nil {
		logger.LogWarn(c.Request.Context(), "unihub_session_failure stage=session_bind")
		_, _ = model.RevokeUserSession(userID, bundle.Session.SID, "unihub_binding_failed")
		return nil, e
	}
	return bundle, nil
}
