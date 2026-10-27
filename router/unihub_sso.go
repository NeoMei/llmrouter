package router

import (
	"context"
	userkit "github.com/NeoMei/UniHub/packages/userkit-go"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/internal/platformaccount"
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
	var initialization singleflight.Group
	var browserMu sync.RWMutex
	var browser *userkit.Browser
	r.GET("/api/unihub/login", func(c *gin.Context) {
		c.Header("Cache-Control", "no-store")
		if !config.Enabled || config.OIDCIssuer == "" {
			c.Redirect(303, "/unihub?sso_error=1")
			return
		}
		destination := c.Query("return_to")
		if destination == "" {
			destination = "/dashboard"
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
					c.Redirect(303, "/unihub?sso_error=1")
					return
				}
				activeBrowser = initialized.Val.(*userkit.Browser)
			case <-c.Request.Context().Done():
				c.Redirect(303, "/unihub?sso_error=1")
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
		callback, _ := url.Parse(config.RedirectURI)
		http.SetCookie(c.Writer, &http.Cookie{Name: "unihub_oidc_binding", Value: binding, Path: callback.Path, MaxAge: 300, HttpOnly: true, Secure: callback.Scheme == "https", SameSite: http.SameSiteLaxMode})
		c.Redirect(303, authorize)
	})
	r.GET("/api/unihub/callback", func(c *gin.Context) {
		c.Header("Cache-Control", "no-store")
		c.Header("Referrer-Policy", "no-referrer")
		browserMu.RLock()
		activeBrowser := browser
		browserMu.RUnlock()
		if activeBrowser == nil || !config.Enabled {
			c.Redirect(303, "/unihub?sso_error=1")
			return
		}
		binding, e := c.Cookie("unihub_oidc_binding")
		if e != nil {
			c.Redirect(303, "/unihub?sso_error=1")
			return
		}
		callback, _ := url.Parse(config.RedirectURI)
		http.SetCookie(c.Writer, &http.Cookie{Name: "unihub_oidc_binding", Path: callback.Path, MaxAge: -1, HttpOnly: true, Secure: callback.Scheme == "https", SameSite: http.SameSiteLaxMode})
		// The configured origin is trusted; the observed callback path/query must match.
		if c.Request.Host != callback.Host {
			c.Redirect(303, "/unihub?sso_error=1")
			return
		}
		absolute := callback.Scheme + "://" + callback.Host + c.Request.URL.RequestURI()
		proof, e := activeBrowser.Complete(c.Request.Context(), binding, absolute)
		if e != nil {
			c.Redirect(303, "/unihub?sso_error=1")
			return
		}
		access, e := config.Resolve(c.Request.Context(), proof.AccessToken)
		if e != nil {
			c.Redirect(303, "/unihub?sso_error=1")
			return
		}
		if proof.ExpiresAt < access.ExpiresAt {
			access.ExpiresAt = proof.ExpiresAt
		}
		bundle, e := createUniHubSession(c, access, proof.AccessToken)
		if e != nil {
			c.Redirect(303, "/unihub?sso_error=1")
			return
		}
		service.WriteRefreshCookie(c, bundle.RefreshToken)
		c.Redirect(303, proof.ReturnTo)
	})
}
func createUniHubSession(c *gin.Context, a userkit.Access, bearer string) (*service.AuthBundle, error) {
	local, e := (model.UniHubProvisioner{}).Ensure(c.Request.Context(), a)
	if e != nil {
		return nil, e
	}
	userID, e := strconv.Atoi(local.ID)
	if e != nil {
		return nil, e
	}
	bundle, e := service.CreateUniHubLoginSession(userID, a.ExpiresAt, c.ClientIP(), c.Request.UserAgent())
	if e != nil {
		return nil, e
	}
	if e = model.BindUniHubSession(bundle.Session.SID, userID, a, bearer, a.ExpiresAt); e != nil {
		_, _ = model.RevokeUserSession(userID, bundle.Session.SID, "unihub_binding_failed")
		return nil, e
	}
	return bundle, nil
}
