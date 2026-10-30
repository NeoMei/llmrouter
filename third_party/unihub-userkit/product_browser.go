package userkit

import (
	"context"
	"encoding/hex"
	"net/url"
	"strings"
	"time"
)

// ProductBrowserApproval proves the already authenticated source browser's own
// one-time handoff challenge. The deployment credential fixes product and instance.
type ProductBrowserApproval struct {
	ClientID  string `json:"client_id"`
	HandoffID string `json:"handoff_id"`
	State     string `json:"state"`
}
type ProductBrowserApprovalReceipt struct {
	FinishURL string `json:"finish_url"`
}
type ProductBrowserExchange struct {
	ClientID    string `json:"client_id"`
	Code        string `json:"code"`
	Verifier    string `json:"code_verifier"`
	Nonce       string `json:"nonce"`
	RedirectURI string `json:"redirect_uri"`
}

// ProductBrowserGrant is a scoped, short-lived binding proof, never an OIDC or
// general API access token. Bind consumes ProofToken; current access is reread
// by the product runtime on every authenticated request.
type ProductBrowserGrant struct {
	Access               Access `json:"access"`
	ProofToken           string `json:"proof_token"`
	ExpiresAt            int64  `json:"expires_at"`
	SourceProofToken     string `json:"source_proof_token,omitempty"`
	SourceProofExpiresAt int64  `json:"source_proof_expires_at,omitempty"`
}

func (c *RuntimeClient) ApproveProductBrowserHandoff(ctx context.Context, actor string, in ProductBrowserApproval) (out ProductBrowserApprovalReceipt, e error) {
	e = c.request(ctx, "POST", "/identity/browser/approve", actor, in, &out)
	if e == nil {
		u, err := url.Parse(out.FinishURL)
		origin := c.config.BrowserOrigin
		if origin == "" {
			origin = c.config.BaseURL
		}
		base, _ := url.Parse(origin)
		if err != nil || u.Scheme != base.Scheme || u.Host != base.Host || u.Path != "/auth/product/finish" || u.User != nil || u.Fragment != "" {
			e = ErrInvalid
		}
	}
	return
}
func (c *RuntimeClient) ExchangeProductBrowser(ctx context.Context, in ProductBrowserExchange) (out ProductBrowserGrant, e error) {
	e = c.request(ctx, "POST", "/identity/browser/exchange", "", in, &out)
	if e == nil {
		e = c.scope(out.Access.ProductID, out.Access.InstanceID)
	}
	if e == nil {
		e = validate(out.Access)
	}
	now := time.Now().Unix()
	if e == nil && (!strings.HasPrefix(out.ProofToken, "ubp_") || len(out.ProofToken) > 256 || out.ExpiresAt <= now || out.ExpiresAt > now+125 || out.Access.Subject != out.Access.User.ID) {
		e = ErrInvalid
	}
	if e == nil && (out.SourceProofToken != "" || out.SourceProofExpiresAt != 0) {
		if !validProductBrowserSourceProof(out.SourceProofToken) || out.SourceProofExpiresAt <= now || out.SourceProofExpiresAt > now+3605 {
			e = ErrInvalid
		}
	}
	return
}

func validProductBrowserSourceProof(raw string) bool {
	if len(raw) != len("ubsp_")+64 || !strings.HasPrefix(raw, "ubsp_") {
		return false
	}
	for _, r := range raw[len("ubsp_"):] {
		if r >= 'A' && r <= 'F' {
			return false
		}
	}
	_, err := hex.DecodeString(raw[len("ubsp_"):])
	return err == nil
}

// VerifyProductBrowserSource checks the live central browser session behind a
// broker grant. The source proof is sent only to the fixed private verifier.
func (c *RuntimeClient) VerifyProductBrowserSource(ctx context.Context, proof, centralUserID string) error {
	if !validProductBrowserSourceProof(proof) || centralUserID == "" || len(centralUserID) > 256 || strings.ContainsAny(centralUserID, "\r\n\x00") {
		return ErrInvalid
	}
	return c.request(ctx, "POST", "/identity/browser/source/verify", "", struct {
		SourceProofToken string `json:"source_proof_token"`
		CentralUserID    string `json:"central_user_id"`
	}{proof, centralUserID}, nil)
}
