package router

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	userkit "github.com/NeoMei/UniHub/packages/userkit-go"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/internal/platformaccount"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
)

const productHandoffPurpose = "product_handoff"
const productBrokerPurpose = "product_broker"
const productHandoffCookie = "llmrouter_unihub_handoff"
const productBrokerCookie = "llmrouter_unihub_broker"

type productBrowserBridge struct{ config platformaccount.Config }
type productBrowserState struct {
	ReturnTo string `json:"return_to"`
	Verifier string `json:"verifier,omitempty"`
	Nonce    string `json:"nonce,omitempty"`
	Approved bool   `json:"approved,omitempty"`
}

func newProductBrowserBridge(r *gin.Engine, c platformaccount.Config) *productBrowserBridge {
	b := &productBrowserBridge{c}
	r.GET("/api/user/auth/unihub-handoff", b.startHandoff)
	return b
}
func safeProductBrowserReturn(raw string) (string, bool) {
	if raw == "" {
		raw = "/dashboard"
	}
	u, e := url.Parse(raw)
	return raw, e == nil && userkit.SafeReturn(raw) && middleware.IsProductApplicationPath(u.Path)
}
func (b *productBrowserBridge) origin() (string, error) {
	raw := strings.TrimRight(os.Getenv("UNIHUB_ORIGIN"), "/")
	u, e := url.Parse(raw)
	if e != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" {
		return "", userkit.ErrInvalid
	}
	if _, e = platformaccount.RuntimeClient(); e != nil {
		return "", e
	}
	return raw, nil
}
func (b *productBrowserBridge) validCallback(c *gin.Context) bool {
	u, e := url.Parse(b.config.RedirectURI)
	return e == nil && u.User == nil && u.Host != "" && u.Path == "/api/unihub/callback" && c.Request.Host == u.Host
}
func (b *productBrowserBridge) cookie(c *gin.Context, name, value string, maxAge int) {
	u, _ := url.Parse(b.config.RedirectURI)
	http.SetCookie(c.Writer, &http.Cookie{Name: name, Value: value, Path: "/api/unihub/callback", HttpOnly: true, Secure: u.Scheme == "https", SameSite: http.SameSiteLaxMode, MaxAge: maxAge})
}
func browserStateCookie(c *gin.Context, name string) bool {
	if len(c.Request.URL.Query()["state"]) != 1 {
		return false
	}
	state := c.Query("state")
	cookie, e := c.Cookie(name)
	return e == nil && len(state) == 43 && len(cookie) == 43 && subtle.ConstantTimeCompare([]byte(state), []byte(cookie)) == 1
}
func productBrowserFailure(c *gin.Context, target string) {
	q := url.Values{"unihub_silent": {"done"}}
	if safe, ok := safeProductBrowserReturn(target); ok {
		q.Set("redirect", safe)
	}
	c.Redirect(303, "/sign-in?"+q.Encode())
}

// SameSite cookies also cross sibling subdomains. The source start is a
// same-origin browser navigation, so a sibling may not silently switch the
// central account by initiating a handoff with another product's cookies.
func sameOriginProductBrowserStart(c *gin.Context, callback string) bool {
	if values := c.Request.Header.Values("Sec-Fetch-Site"); len(values) == 1 {
		return values[0] == "same-origin"
	}
	expected, e := url.Parse(callback)
	if e != nil {
		return false
	}
	for _, header := range []string{"Origin", "Referer"} {
		values := c.Request.Header.Values(header)
		if len(values) != 1 {
			continue
		}
		value, e := url.Parse(values[0])
		if e == nil && value.User == nil && value.Scheme == expected.Scheme && value.Host == expected.Host {
			return true
		}
	}
	return false
}

func (b *productBrowserBridge) startHandoff(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	c.Header("Referrer-Policy", "no-referrer")
	if !platformaccount.RuntimeEnabled() {
		c.Status(404)
		return
	}
	target, ok := safeProductBrowserReturn(c.Query("return_to"))
	if !ok {
		c.Status(400)
		return
	}
	origin, e := b.origin()
	if e != nil || b.config.ClientID == "" || !b.validCallbackHost(c) {
		c.Status(503)
		return
	}
	if !sameOriginProductBrowserStart(c, b.config.RedirectURI) {
		c.Status(403)
		return
	}
	// SameSite=Strict refresh credentials are sent only on this same-origin start.
	// Rotating the refresh secret proves the original browser; an SID alone cannot.
	raw, e := c.Cookie(service.RefreshCookieName)
	if e != nil {
		c.Status(401)
		return
	}
	bundle, user, e := service.RefreshLoginSession(raw, "", c.ClientIP(), c.Request.UserAgent())
	if e != nil {
		c.Status(401)
		return
	}
	service.WriteRefreshCookie(c, bundle.RefreshToken)
	if e = model.SyncProductAuthVersions(c.Request.Context(), user.Id); e != nil {
		c.Status(503)
		return
	}
	if _, e = platformaccount.AuthenticateLocal(c.Request.Context(), strconv.Itoa(user.Id), user.AuthVersion, "session"); e != nil {
		c.Status(401)
		return
	}
	state, e := common.Marshal(productBrowserState{ReturnTo: target})
	if e != nil {
		c.Status(500)
		return
	}
	token, _, e := model.CreateAuthFlow(model.AuthFlowCreate{Purpose: productHandoffPurpose, UserId: user.Id, SessionId: bundle.Session.SID, Payload: string(state), ExpiresAt: time.Now().Add(120 * time.Second)})
	if e != nil {
		c.Status(503)
		return
	}
	b.cookie(c, productHandoffCookie, token, 120)
	c.Redirect(303, origin+"/auth/product/start?"+url.Values{"client_id": {b.config.ClientID}, "state": {token}}.Encode())
}
func (b *productBrowserBridge) validCallbackHost(c *gin.Context) bool {
	u, e := url.Parse(b.config.RedirectURI)
	return e == nil && u.Host == c.Request.Host && u.Path == "/api/unihub/callback" && u.User == nil
}
func (b *productBrowserBridge) begin(c *gin.Context, target string) bool {
	if !platformaccount.RuntimeEnabled() || c.Query("broker_fallback") == "1" {
		return false
	}
	destination, ok := safeProductBrowserReturn(target)
	if !ok {
		c.Status(400)
		return true
	}
	origin, e := b.origin()
	if e != nil || !b.validCallbackHost(c) || b.config.ClientID == "" {
		productBrowserFailure(c, destination)
		return true
	}
	verifier, e := common.GenerateRandomCharsKey(64)
	if e != nil {
		c.Status(500)
		return true
	}
	nonce, e := common.GenerateRandomCharsKey(48)
	if e != nil {
		c.Status(500)
		return true
	}
	payload, e := common.Marshal(productBrowserState{ReturnTo: destination, Verifier: verifier, Nonce: nonce})
	if e != nil {
		c.Status(500)
		return true
	}
	token, _, e := model.CreateAuthFlow(model.AuthFlowCreate{Purpose: productBrokerPurpose, Payload: string(payload), ExpiresAt: time.Now().Add(120 * time.Second)})
	if e != nil {
		c.Status(503)
		return true
	}
	sum := sha256.Sum256([]byte(verifier))
	b.cookie(c, productBrokerCookie, token, 120)
	q := url.Values{"client_id": {b.config.ClientID}, "redirect_uri": {b.config.RedirectURI}, "state": {token}, "nonce": {nonce}, "code_challenge_method": {"S256"}, "code_challenge": {base64.RawURLEncoding.EncodeToString(sum[:])}}
	c.Redirect(303, origin+"/auth/product/authorize?"+q.Encode())
	return true
}
func (b *productBrowserBridge) callback(c *gin.Context) bool {
	if c.Query("handoff") == "" && c.Query("handoff_done") == "" && c.Query("broker") == "" {
		return false
	}
	c.Header("Cache-Control", "no-store")
	c.Header("Referrer-Policy", "no-referrer")
	if !platformaccount.RuntimeEnabled() || !b.validCallback(c) {
		c.Status(401)
		return true
	}
	for _, vs := range c.Request.URL.Query() {
		if len(vs) != 1 {
			c.Status(400)
			return true
		}
	}
	if c.Query("broker") == "1" {
		b.completeBroker(c)
		return true
	}
	if !browserStateCookie(c, productHandoffCookie) {
		c.Status(401)
		return true
	}
	token := c.Query("state")
	flow, e := model.GetAuthFlow(token, model.AuthFlowMatch{Purpose: productHandoffPurpose})
	if e != nil {
		c.Status(401)
		return true
	}
	var state productBrowserState
	if common.UnmarshalJsonStr(flow.Payload, &state) != nil {
		c.Status(401)
		return true
	}
	if _, e = service.ValidateSessionReference(flow.UserId, flow.SessionId); e != nil {
		c.Status(401)
		return true
	}
	if c.Query("handoff_done") == "1" {
		if !state.Approved {
			c.Status(401)
			return true
		}
		if _, e = model.ConsumeAuthFlow(token, model.AuthFlowMatch{Purpose: productHandoffPurpose, UserId: flow.UserId, SessionId: flow.SessionId}); e != nil {
			c.Status(401)
			return true
		}
		b.cookie(c, productHandoffCookie, "", -1)
		c.Redirect(303, state.ReturnTo)
		return true
	}
	if state.Approved || c.Query("handoff") == "" {
		c.Status(401)
		return true
	}
	if e = model.SyncProductAuthVersions(c.Request.Context(), flow.UserId); e != nil {
		c.Status(503)
		return true
	}
	user, e := model.GetUserById(flow.UserId, false)
	if e != nil {
		c.Status(401)
		return true
	}
	actor, e := platformaccount.AuthenticateLocal(c.Request.Context(), strconv.Itoa(user.Id), user.AuthVersion, "session")
	if e != nil {
		c.Status(401)
		return true
	}
	client, e := platformaccount.RuntimeClient()
	if e != nil {
		c.Status(503)
		return true
	}
	receipt, e := client.ApproveProductBrowserHandoff(c.Request.Context(), actor.ActorToken, userkit.ProductBrowserApproval{ClientID: b.config.ClientID, HandoffID: c.Query("handoff"), State: token})
	if e != nil {
		c.Status(401)
		return true
	}
	state.Approved = true
	payload, _ := common.Marshal(state)
	result := model.DB.Model(&model.AuthFlow{}).Where("id = ? AND consumed_at IS NULL AND payload = ?", flow.Id, flow.Payload).Update("payload", string(payload))
	if result.Error != nil || result.RowsAffected != 1 {
		c.Status(401)
		return true
	}
	c.Redirect(303, receipt.FinishURL)
	return true
}
func (b *productBrowserBridge) completeBroker(c *gin.Context) {
	if !browserStateCookie(c, productBrokerCookie) {
		c.Status(401)
		return
	}
	flow, e := model.ConsumeAuthFlow(c.Query("state"), model.AuthFlowMatch{Purpose: productBrokerPurpose})
	if e != nil {
		c.Status(401)
		return
	}
	b.cookie(c, productBrokerCookie, "", -1)
	var state productBrowserState
	if common.UnmarshalJsonStr(flow.Payload, &state) != nil {
		c.Status(401)
		return
	}
	if c.Query("error") == "login_required" {
		q := url.Values{"prompt": {"none"}, "return_to": {state.ReturnTo}, "broker_fallback": {"1"}}
		c.Redirect(303, "/api/unihub/login?"+q.Encode())
		return
	}
	if c.Query("error") != "" || c.Query("code") == "" {
		productBrowserFailure(c, state.ReturnTo)
		return
	}
	client, e := platformaccount.RuntimeClient()
	if e != nil {
		productBrowserFailure(c, state.ReturnTo)
		return
	}
	grant, e := client.ExchangeProductBrowser(c.Request.Context(), userkit.ProductBrowserExchange{ClientID: b.config.ClientID, Code: c.Query("code"), Verifier: state.Verifier, Nonce: state.Nonce, RedirectURI: b.config.RedirectURI})
	if e != nil {
		productBrowserFailure(c, state.ReturnTo)
		return
	}
	local, e := (model.UniHubProvisioner{}).Ensure(c.Request.Context(), grant.Access)
	if e != nil {
		productBrowserFailure(c, state.ReturnTo)
		return
	}
	if _, e = client.Bind(c.Request.Context(), grant.ProofToken, userkit.BindRequest{LocalUserID: local.ID, BindingKey: "browser:" + local.ID}); e != nil {
		productBrowserFailure(c, state.ReturnTo)
		return
	}
	userID, e := strconv.Atoi(local.ID)
	if e != nil {
		productBrowserFailure(c, state.ReturnTo)
		return
	}
	if e = reconcileSSOUser(c, userID); e != nil {
		productBrowserFailure(c, state.ReturnTo)
		return
	}
	if e = model.EnsureProductFunding(c.Request.Context(), userID); e != nil && !errors.Is(e, model.ErrProductFundingMigration) {
		productBrowserFailure(c, state.ReturnTo)
		return
	}
	bundle, e := service.CreateLoginSession(userID, "product_broker", c.ClientIP(), c.Request.UserAgent())
	if e != nil {
		productBrowserFailure(c, state.ReturnTo)
		return
	}
	service.WriteRefreshCookie(c, bundle.RefreshToken)
	c.Redirect(303, state.ReturnTo)
}

func reconcileSSOUser(c *gin.Context, userID int) error {
	user, e := model.GetUserById(userID, false)
	if e != nil {
		return e
	}
	if e = model.SyncProductAuthVersions(c.Request.Context(), userID); e != nil {
		return e
	}
	if e = model.ReconcileProductIdentity(c.Request.Context(), user); e != nil {
		return e
	}
	if user.Status != common.UserStatusEnabled {
		return userkit.ErrDenied
	}
	return nil
}
