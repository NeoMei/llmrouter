// Package platformaccount verifies product identity without creating local users or sessions.
package platformaccount

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
)

type Config struct {
	InstanceID   string   `json:"instance_id"`
	OIDCIssuer   string   `json:"-"`
	ClientSecret string   `json:"-"`
	Enabled      bool     `json:"enabled"`
	PublicOrigin string   `json:"public_origin"`
	APIOrigin    string   `json:"-"`
	ClientID     string   `json:"client_id"`
	RedirectURI  string   `json:"redirect_uri"`
	Models       []string `json:"models"`
	ProductID    string   `json:"product_id"`
}

func validateURL(raw string, origin bool) error {
	u, e := url.Parse(raw)
	if e != nil || u.Host == "" {
		return errors.New("invalid UniHub URL")
	}
	ip := net.ParseIP(u.Hostname())
	loop := u.Hostname() == "localhost" || (ip != nil && ip.IsLoopback())
	if (u.Scheme != "https" && !(u.Scheme == "http" && loop)) || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || (origin && u.Path != "" && u.Path != "/") {
		return errors.New("invalid UniHub URL")
	}
	return nil
}
func FromEnv() (Config, error) {
	c := Config{Enabled: os.Getenv("UNIHUB_PRODUCT_ENABLED") == "true", ProductID: "llmrouter", Models: []string{}}
	if !c.Enabled {
		return c, nil
	}
	c.InstanceID = os.Getenv("UNIHUB_PRODUCT_INSTANCE_ID")
	if c.InstanceID == "" {
		c.InstanceID = "default"
	}
	c.OIDCIssuer = os.Getenv("UNIHUB_PRODUCT_OIDC_ISSUER")
	c.ClientSecret = os.Getenv("UNIHUB_PRODUCT_CLIENT_SECRET")
	c.PublicOrigin = strings.TrimRight(os.Getenv("UNIHUB_PRODUCT_PUBLIC_ORIGIN"), "/")
	c.APIOrigin = strings.TrimRight(os.Getenv("UNIHUB_PRODUCT_API_ORIGIN"), "/")
	c.ClientID = os.Getenv("UNIHUB_PRODUCT_CLIENT_ID")
	c.RedirectURI = os.Getenv("UNIHUB_PRODUCT_REDIRECT_URI")
	for _, v := range []string{c.PublicOrigin, c.APIOrigin} {
		if e := validateURL(v, true); e != nil {
			return c, e
		}
	}
	if e := validateURL(c.RedirectURI, false); e != nil {
		return c, e
	}
	if c.OIDCIssuer != "" {
		if e := validateURL(c.OIDCIssuer, false); e != nil {
			return c, e
		}
		callback, _ := url.Parse(c.RedirectURI)
		if callback.Path != "/api/unihub/callback" {
			return c, errors.New("UniHub browser SSO requires /api/unihub/callback")
		}
	}
	if c.ClientID == "" {
		return c, errors.New("missing UniHub client")
	}
	if raw := os.Getenv("UNIHUB_PRODUCT_MODELS"); raw != "" {
		if e := common.UnmarshalJsonStr(raw, &c.Models); e != nil {
			return c, e
		}
	}
	if c.Models == nil {
		return c, errors.New("models must be a JSON string array")
	}
	for _, m := range c.Models {
		if strings.TrimSpace(m) == "" || strings.TrimSpace(m) != m {
			return c, errors.New("invalid model")
		}
	}
	return c, nil
}

type User struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Email string `json:"email"`
}
type Account struct {
	ID   string `json:"id"`
	Kind string `json:"kind"`
	Name string `json:"name"`
	Role string `json:"role"`
}
type Identity struct {
	Issuer   string    `json:"issuer"`
	User     User      `json:"user"`
	Accounts []Account `json:"accounts"`
}

func (c Config) Prove(ctx context.Context, token string) (Identity, error) {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	result := Identity{Issuer: c.PublicOrigin}
	client := http.Client{Timeout: 8 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	fetch := func(path string, target interface{}) error {
		req, e := http.NewRequestWithContext(ctx, "GET", c.APIOrigin+path, nil)
		if e != nil {
			return e
		}
		req.Header.Set("Authorization", "Bearer "+token)
		redirect, _ := url.Parse(c.RedirectURI)
		req.Header.Set("Origin", redirect.Scheme+"://"+redirect.Host)
		res, e := client.Do(req)
		if e != nil {
			return errors.New("UniHub proof unavailable")
		}
		defer res.Body.Close()
		if res.StatusCode != 200 {
			return fmt.Errorf("UniHub proof rejected (%d)", res.StatusCode)
		}
		b, e := io.ReadAll(io.LimitReader(res.Body, 1048577))
		if e != nil || len(b) > 1048576 {
			return errors.New("invalid proof response")
		}
		if e = common.Unmarshal(b, target); e != nil {
			return errors.New("invalid proof response")
		}
		return nil
	}
	caps := struct {
		Kind    string `json:"kind"`
		Product string `json:"product_id"`
		Client  string `json:"client_id"`
	}{}
	if e := fetch("/v1/capabilities", &caps); e != nil {
		return result, e
	}
	if caps.Kind != "product" || caps.Product != "llmrouter" || caps.Client != c.ClientID {
		return result, errors.New("wrong UniHub product proof")
	}
	if e := fetch("/v1/me", &result.User); e != nil {
		return result, e
	}
	if strings.TrimSpace(result.User.ID) == "" {
		return result, errors.New("missing immutable identity")
	}
	if e := fetch("/v1/accounts", &result.Accounts); e != nil {
		return result, e
	}
	if result.Accounts == nil {
		return result, errors.New("invalid account proof")
	}
	return result, nil
}
