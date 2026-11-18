package platformbilling

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"math/big"
	"net/http"
	"regexp"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
)

const ProductCreditScope = "product_v1"
const ProductAuthorizationHeader = "X-UniHub-Product-Authorization"

type productAuthorizationKey struct{}

type productAuthorization struct {
	proof, idempotencyKey, receiptAlias string
}

var productAuthorizationPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.:-]*$`)

// WithProductAuthorization transports untrusted proof to UniHub. It conveys no
// authority until the authenticated verification service validates it.
func WithProductAuthorization(ctx context.Context, proof, idempotencyKey string) (context.Context, error) {
	if len(proof) > 4096 || !productAuthorizationPattern.MatchString(proof) || idempotencyKey != "" && !stableID.MatchString(idempotencyKey) {
		return nil, ErrInvalid
	}
	return context.WithValue(ctx, productAuthorizationKey{}, productAuthorization{proof: proof, idempotencyKey: idempotencyKey}), nil
}

func productAuthorizationFrom(ctx context.Context) productAuthorization {
	p, _ := ctx.Value(productAuthorizationKey{}).(productAuthorization)
	return p
}

func (a *Adapter) managedGrant(ctx context.Context, credential, model, wire, purpose string) (Claims, error) {
	request := map[string]string{"token": credential, "model": model, "instance_id": a.config.InstanceID}
	if wire != "" {
		request["wire_operation"] = wire
	}
	if purpose != "" {
		request["purpose"] = purpose
	}
	proof := productAuthorizationFrom(ctx).proof
	if proof != "" {
		request["product_authorization"] = proof
	}
	var grant Claims
	if err := a.client.do(ctx, http.MethodPost, "/internal/v1/model-credentials/verify", "", request, &grant); err != nil {
		return Claims{}, err
	}
	return grant, nil
}

func (a *Adapter) validateManagedCredential(ctx context.Context, credential, task, model string, grant Claims, receipt bool) error {
	proof := productAuthorizationFrom(ctx).proof
	if grant.CreditScope != "" || proof != "" {
		if proof == "" || !grant.ProductCredit() || !receipt && !grant.NativePricing() {
			return ErrInvalid
		}
		return a.validateCredentialIdentity(credential, task, grant)
	}
	return a.validateModelCredential(credential, task, model, grant)
}

func (grant Claims) ProductCredit() bool {
	return grant.CreditScope == ProductCreditScope && grant.SourceVerified && grant.CredentialKind == "api_key" && grant.CredentialVersion > 0 &&
		stableID.MatchString(grant.OriginProductID) && grant.FundingProductID == grant.OriginProductID &&
		stableID.MatchString(grant.ID) && stableID.MatchString(grant.AccountID) && stableID.MatchString(grant.ActorID)
}

// ProductCreditCallID binds retries to the verified product, payer and caller.
// Reprovisioned Keys cannot create another call with the same idempotency key.
// Admission and receipt reads still require the original Key.
func (grant Claims) ProductCreditCallID(ctx context.Context) (string, error) {
	p := productAuthorizationFrom(ctx)
	if p.proof == "" || !grant.ProductCredit() || !grant.NativePricing() || !stableID.MatchString(p.idempotencyKey) {
		return "", ErrInvalid
	}
	return grant.productCreditCallID(p.idempotencyKey)
}

func (grant Claims) productCreditCallID(key string) (string, error) {
	if !stableID.MatchString(key) {
		return "", ErrInvalid
	}
	scope, err := common.Marshal([]string{ProductCreditScope, grant.InstanceID, grant.OriginProductID, grant.AccountID, grant.ActorID, key})
	if err != nil {
		return "", ErrInvalid
	}
	sum := sha256.Sum256(scope)
	return "pc_" + hex.EncodeToString(sum[:]), nil
}

func (row Call) matchesCreditReceiptScope(grant Claims) bool {
	return row.CreditScope == grant.CreditScope && (row.CreditScope == "" ||
		grant.ProductCredit() && row.FundingProductID == grant.FundingProductID && row.CredentialVersion == grant.CredentialVersion)
}

func (row Call) matchesCreditScope(grant Claims) bool {
	return row.matchesCreditReceiptScope(grant) && (row.CreditScope == "" ||
		row.PricingSource == grant.PricingSource && row.Asset == grant.Asset && row.FundingMode == grant.FundingMode &&
			row.BillingProfileVersion == grant.BillingProfileVersion)
}

func (s *Session) ProductCredit() bool {
	return s != nil && s.call.CreditScope == ProductCreditScope
}

// ProductCreditOperation admits only producers with a durable native measured
// receipt. Enabled legacy operations keep their separate existing contract.
func ProductCreditOperation(wire string) bool {
	switch wire {
	case "chat.completions.create", "claude.messages.create", "gemini.generate", "responses.create",
		"responses.compact", "embeddings.create", "images.generations.create", "images.edits.create",
		"audio.speech.create", "audio.transcriptions.create", "ocr.jobs.create", "ocr.jobs.retrieve", "ocr.results.retrieve":
		return true
	}
	return false
}

func (s *Session) ProductBillingOperation() string {
	if s.ProductCredit() {
		return s.call.Operation
	}
	return ""
}

func (s *Session) ProductDispatchStarted() bool {
	return s.ProductCredit() && s.attempt.ID != 0
}

// BindProductUpstream records the transformed model sent to the selected
// supplier. No credential, provider URL, request content or charge is stored.
func (s *Session) BindProductUpstream(model string) error {
	if !s.ProductCredit() || s.call.Status != "RESERVED" || len(model) == 0 || len(model) > 255 || strings.TrimSpace(model) != model {
		return ErrInvalid
	}
	identity, err := common.Marshal(struct {
		Model string `json:"model"`
	}{model})
	if err != nil {
		return err
	}
	s.nativeSend = string(identity)
	return nil
}

func productCreditConversionMatches(c NativeConversion, cnyPerUSD, quotaPerUSD string) bool {
	fx, fxOK := new(big.Rat).SetString(cnyPerUSD)
	units, unitsOK := new(big.Rat).SetString(quotaPerUSD)
	if !c.validCredit() || c.Numerator != "1" || c.Denominator != "1" || !fxOK || !unitsOK || fx.Sign() <= 0 || units.Sign() <= 0 {
		return false
	}
	// The source tariff is denominated in USD. A major CNY unit buys 10
	// credits; each credit comprises 10^6 microcredit units.
	expected := new(big.Rat).Mul(fx, big.NewRat(10000000, 1))
	return units.Cmp(expected) == 0
}

// WithProductCreditReceiptAlias keeps a caller's lookup ID in request memory
// so an authorized receipt can echo it after comparing the derived call ID.
func WithProductCreditReceiptAlias(ctx context.Context, alias string) context.Context {
	proof := productAuthorizationFrom(ctx)
	if proof.proof == "" || !stableID.MatchString(alias) {
		return ctx
	}
	proof.receiptAlias = alias
	return context.WithValue(ctx, productAuthorizationKey{}, proof)
}

// ProductCreditChannelSupported gates dispatch on the producer paths that
// retain the native measured receipt for this protocol.
func ProductCreditChannelSupported(wire string, channelType int) bool {
	if !ProductCreditOperation(wire) {
		return false
	}
	switch wire {
	case "chat.completions.create", "responses.create", "responses.compact", "claude.messages.create", "gemini.generate", "embeddings.create":
		_, ok := TokenProducer(wire, channelType)
		return ok
	case "ocr.jobs.create", "ocr.jobs.retrieve", "ocr.results.retrieve":
		return true // The OCR producer verifies its specific channel/credential contract.
	case "images.generations.create", "images.edits.create", "audio.speech.create", "audio.transcriptions.create":
		return channelType == constant.ChannelTypeOpenAI || channelType == constant.ChannelTypeAli
	}
	return false
}
