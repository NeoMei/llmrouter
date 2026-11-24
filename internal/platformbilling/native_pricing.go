package platformbilling

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"math/big"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/metering"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type NativeConversion struct {
	Version     string `json:"version"`
	Numerator   string `json:"numerator"`
	Denominator string `json:"denominator"`
	Rounding    string `json:"rounding"`
}

type NativeQuote struct {
	PricingVersion string           `json:"pricing_version"`
	PricingDigest  string           `json:"pricing_digest"`
	BillingModel   string           `json:"billing_model"`
	WireOperation  string           `json:"wire_operation"`
	RouteID        string           `json:"route_id"`
	ChannelID      string           `json:"channel_id"`
	Group          string           `json:"group"`
	NativeQuotaMax string           `json:"native_quota_max"`
	Conversion     NativeConversion `json:"conversion"`
	Asset          string           `json:"asset"`
	MaxAmount      string           `json:"max_amount"`
}

type NativeReserveRequest struct {
	ManagedOperation     string      `json:"managed_operation,omitempty"`
	MinimumCreditAmount  string      `json:"minimum_credit_amount,omitempty"`
	CreditScope          string      `json:"credit_scope,omitempty"`
	ProductAuthorization string      `json:"product_authorization,omitempty"`
	Credential           string      `json:"credential"`
	InstanceID           string      `json:"instance_id"`
	TaskID               string      `json:"task_id"`
	CallID               string      `json:"call_id"`
	RequestDigest        string      `json:"request_digest"`
	Model                string      `json:"model"`
	WireOperation        string      `json:"wire_operation"`
	Quote                NativeQuote `json:"quote"`
}

type NativeSettleRequest struct {
	PricingDigest string `json:"pricing_digest"`
	NativeQuota   string `json:"native_quota"`
	Amount        string `json:"amount"`
	Usage         *Usage `json:"usage,omitempty"`
	ProviderCost  string `json:"provider_cost,omitempty"`
}

// NativeOperationAvailable describes the delegated execution adapters. A model
// can remain discoverable while an unimplemented wire contract is unavailable.
func NativeOperationAvailable(wire string) bool {
	switch wire {
	case "bailian.audio.speech.create":
		return true
	case "dashscope.images.generate", "chat.completions.create", "claude.messages.create", "gemini.generate", "responses.create", "responses.compact", "embeddings.create", "rerank.create", "multimodal.embeddings.create", "images.generations.create", "images.edits.create", "audio.speech.create", "audio.transcriptions.create", "ocr.jobs.create", "ocr.jobs.retrieve", "ocr.results.retrieve":
		return true
	}
	return false
}

// Amount uses the frozen native conversion, rounding the cumulative charge up
// exactly once. No floating point arithmetic or client amount is accepted.
func (q NativeQuote) Amount(quota int) (string, error) {
	return q.AmountForScope(int64(quota), "")
}

func (q NativeQuote) AmountForScope(quota int64, creditScope string) (string, error) {
	if creditScope == ProductCreditScope {
		if quota < 0 || q.Asset != "CREDIT/6" || !q.Conversion.validCredit() || q.Conversion.Numerator != "1" || q.Conversion.Denominator != "1" {
			return "", ErrInvalid
		}
		return strconv.FormatInt(quota, 10), nil
	}
	if creditScope != "" {
		return "", ErrInvalid
	}
	validFunding := q.Asset == "CNY/6" && q.Conversion.Version == "lr-quota-usd-cny-v1" || q.Asset == "CREDIT/6" && q.Conversion.validCredit()
	if quota < 0 || quota > int64(common.MaxQuota) || !validFunding || q.Conversion.Rounding != "lr-cumulative-ceil-v1" {
		return "", ErrInvalid
	}
	n, nOK := new(big.Int).SetString(q.Conversion.Numerator, 10)
	d, dOK := new(big.Int).SetString(q.Conversion.Denominator, 10)
	if !nOK || !dOK || n.Sign() <= 0 || d.Sign() <= 0 || !n.IsInt64() || !d.IsInt64() || n.String() != q.Conversion.Numerator || d.String() != q.Conversion.Denominator {
		return "", ErrInvalid
	}
	value := new(big.Int).Mul(n, big.NewInt(quota))
	value.Add(value, new(big.Int).Sub(d, big.NewInt(1))).Quo(value, d)
	if !value.IsInt64() {
		return "", ErrInvalid
	}
	return value.String(), nil
}

func (s *Session) NativePricing() bool { return s != nil && nativePricingSource(s.call.PricingSource) }

func (a *Adapter) AdmitNative(ctx context.Context, credential, task, id string, wire WireOperation, model string, body []byte) (*Session, error) {
	return a.admitNative(ctx, credential, task, id, wire, model, body, nil)
}

func (a *Adapter) admitNative(ctx context.Context, credential, task, id string, wire WireOperation, model string, body []byte, certified *operationRequest) (*Session, error) {
	if !a.Enabled() || a.config.NativeGroup == "" || !stableID.MatchString(id) || !stableID.MatchString(task) || wireBillingOperation(wire.ID) == "" || model == "" || len(model) > 255 || strings.TrimSpace(model) != model || len(body) > 64<<20 {
		return nil, ErrInvalid
	}
	grant, err := a.ManagedModelGrant(ctx, credential)
	if err != nil || !grant.NativePricing() {
		if err != nil {
			return nil, err
		}
		return nil, ErrInvalid
	}
	if certified != nil && !grant.ProductCredit() {
		return nil, ErrInvalid
	}
	fundingProduct := "llmrouter"
	if grant.ProductCredit() {
		callID, err := grant.ProductCreditCallID(ctx)
		if err != nil || id != callID || task != "task_"+callID || !ProductCreditOperation(wire.ID) {
			return nil, ErrInvalid
		}
		fundingProduct = grant.FundingProductID
	}
	digestInput, err := common.Marshal(struct {
		Task, Model string
		Wire        WireOperation
		Body        []byte
	}{task, model, wire, body})
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(digestInput)
	row := Call{CreditScope: grant.CreditScope, CallID: id, ActorID: grant.ActorID, AccountID: grant.AccountID, OriginProductID: grant.OriginProductID, CredentialID: grant.ID, CredentialKind: grant.CredentialKind, InstanceID: grant.InstanceID, TaskID: task, Model: model, Digest: hex.EncodeToString(digest[:]), DigestVersion: "native-pricing-v1", Operation: wireBillingOperation(wire.ID), WireOperation: wire.ID, WirePathModel: wire.PathModel, WireMode: wire.Mode, Asset: grant.Asset, FundingMode: grant.FundingMode, FundingProductID: fundingProduct, BillingProfileVersion: grant.BillingProfileVersion, PricingSource: grant.PricingSource, CredentialVersion: grant.CredentialVersion, Status: "ADMITTED"}
	if certified != nil {
		row.MediaSnapshot = certified.MediaSnapshot
		row.ReservationQuantity = int64(certified.Quantity)
		row.ReservationOutput = int64(certified.Output)
	}
	created := a.db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&row)
	if created.Error != nil {
		return nil, created.Error
	}
	if created.RowsAffected != 1 {
		var old Call
		if err := a.db.WithContext(ctx).First(&old, "call_id = ?", id).Error; err != nil {
			return nil, err
		}
		if old.CredentialID != row.CredentialID || old.PricingSource != row.PricingSource {
			return nil, ErrConflict
		}
		return nil, compareCall(old, row)
	}
	session := &Session{adapter: a, call: row, canonical: body, nativeCredential: credential, nativeCreditConversion: grant.NativeCreditConversion, safeFailure: true}
	// Keep the existing duplicate and receipt lane available for historical
	// requests even when a model has since been removed from product policy.
	if err := session.verifyManagedConversation(ctx, task); err != nil {
		finishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
		defer cancel()
		if finishErr := session.Finish(finishCtx); finishErr != nil {
			common.SysError("managed conversation refusal persistence failed")
		}
		return nil, err
	}
	return session, nil
}

func (s *Session) NativeQuote() (NativeQuote, error) {
	var q NativeQuote
	if !s.NativePricing() || common.UnmarshalUnique([]byte(s.call.NativeQuoteJSON), &q) != nil {
		return q, ErrInvalid
	}
	sum := sha256.Sum256([]byte(s.call.NativePriceSnapshot))
	maximum, err := strconv.ParseInt(q.NativeQuotaMax, 10, 64)
	amount, amountErr := q.AmountForScope(maximum, s.call.CreditScope)
	if err != nil || amountErr != nil || strconv.FormatInt(maximum, 10) != q.NativeQuotaMax || !((q.PricingVersion == "lr-native-v1" && s.call.PricingSource == "llmrouter_native" && q.Asset == "CNY/6" && s.call.Asset == "CNY/6" && s.call.FundingMode == "prepaid_cash") || (q.PricingVersion == NativeCreditPricingVersion && s.call.PricingSource == NativeCreditPricingSource && q.Asset == "CREDIT/6" && s.call.Asset == "CREDIT/6" && s.call.FundingMode == "credits")) || q.PricingDigest != hex.EncodeToString(sum[:]) || q.WireOperation != s.call.WireOperation || amount != q.MaxAmount {
		return q, ErrInvalid
	}
	if q.Asset == "CREDIT/6" {
		var frozen struct {
			Conversion    NativeConversion `json:"conversion"`
			PriceSnapshot string           `json:"price_snapshot"`
		}
		if common.UnmarshalUnique([]byte(s.call.NativePriceSnapshot), &frozen) != nil || frozen.Conversion != q.Conversion {
			return q, ErrInvalid
		}
		if s.ProductCredit() {
			var snapshot struct {
				Native struct {
					GroupRatio   string
					QuotaPerUnit string
					Model        string
					Price        metering.Price
					ProductPrice *struct {
						Model     string         `json:"model"`
						CNYPerUSD string         `json:"cny_per_usd"`
						Revision  string         `json:"revision"`
						Tariff    metering.Price `json:"tariff"`
					} `json:"product_price"`
				} `json:"native"`
			}
			if common.UnmarshalUnique([]byte(frozen.PriceSnapshot), &snapshot) != nil {
				return q, ErrInvalid
			}
			price := snapshot.Native.ProductPrice
			if price == nil || price.Model == "" || price.Model != snapshot.Native.Model || len(price.Revision) != 64 ||
				snapshot.Native.GroupRatio != "1" || !reflect.DeepEqual(snapshot.Native.Price, price.Tariff) ||
				price.Tariff.Operation != s.call.Operation || !productCreditConversionMatches(q.Conversion, price.CNYPerUSD, snapshot.Native.QuotaPerUnit) {
				return q, ErrInvalid
			}
		}
	}
	return q, nil
}

// SetNativeMeteringCalculator installs the owning native-price calculator.
// Recovery supplies only a persisted, digest-checked snapshot and evidence.
func (a *Adapter) SetNativeMeteringCalculator(calculate func(string, metering.Usage) (int, error)) {
	if a != nil {
		a.nativeMetering = calculate
	}
}

func (a *Adapter) SetProductCreditMeteringCalculator(calculate func(string, metering.Usage) (int64, error)) {
	if a != nil {
		a.productCreditMetering = calculate
	}
}

func (a *Adapter) SetNativeDispatchAuthorizer(authorize func(context.Context, Claims, string, NativeQuote) error) {
	if a != nil {
		a.nativeDispatch = authorize
	}
}

// BindNativeDispatchGuard binds a producer's exact target to this admission.
// It runs after the final remote key verification and shared authorization,
// immediately before the durable dispatch fence. It cannot be replaced after
// binding or attached after reservation, and is never shared across sessions.
func (s *Session) BindNativeDispatchGuard(guard func(context.Context) error) error {
	if !s.NativePricing() || s.call.Status != "ADMITTED" || guard == nil || s.nativeDispatchGuard != nil {
		return ErrInvalid
	}
	s.nativeDispatchGuard = guard
	return nil
}

func (s *Session) observeNativeTask(ctx context.Context, u metering.Usage) error {
	if u.State != "confirmed" || u.ProviderRequestID == "" || u.ProviderRequestID != s.call.ProviderTaskID {
		return ErrInvalid
	}
	if _, err := s.NativeQuote(); err != nil {
		return err
	}
	if s.ProductCredit() {
		if s.call.WireOperation == "videos.create" {
			frozen, err := s.FrozenVideoPolicy()
			if err != nil || u.Source != frozen.Policy.UsagePolicy || len(u.Measurements) != 1 || s.call.ReservationQuantity < 1 {
				return ErrInvalid
			}
			m := u.Measurements[0]
			if m.Key != "video_"+strings.ToLower(frozen.Resolution) || m.Unit != "video_second" || m.Quantity.Scale < 0 || m.Quantity.Scale > 9 {
				return ErrInvalid
			}
			quantity, ok := new(big.Int).SetString(m.Quantity.Value, 10)
			expected := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(m.Quantity.Scale)), nil)
			expected.Mul(expected, big.NewInt(s.call.ReservationQuantity))
			if !ok || quantity.Cmp(expected) != 0 {
				return ErrInvalid
			}
		}
		return s.observeProductNativeMetering(ctx, u)
	}
	if s.adapter.nativeMetering == nil {
		return ErrPending
	}
	quota, err := s.adapter.nativeMetering(s.call.NativePriceSnapshot, u)
	if err != nil {
		return err
	}
	evidence, err := common.Marshal(u)
	if err != nil {
		return err
	}
	if s.call.Evidence != "" && s.call.Evidence != string(evidence) {
		_ = s.adapter.db.WithContext(ctx).Model(&Call{}).Where("call_id = ?", s.call.CallID).Update("evidence_conflict", true).Error
		return ErrConflict
	}
	if s.call.Status == "SETTLED" {
		return nil
	}
	if s.call.Status == "SETTLE_PENDING" {
		return s.adapter.deliverNative(ctx, s.call)
	}
	if err := s.adapter.db.WithContext(ctx).Model(&Call{}).Where("call_id = ? AND status = ?", s.call.CallID, "UNKNOWN").Update("evidence", string(evidence)).Error; err != nil {
		return err
	}
	s.call.Evidence = string(evidence)
	return s.SettleNative(ctx, quota, &Usage{Metered: &u})
}

func (a *Adapter) verifyNativeTaskSettlement(ctx context.Context, row Call) error {
	var intent NativeSettleRequest
	var evidence metering.Usage
	if common.UnmarshalUnique([]byte(row.Intent), &intent) != nil || common.UnmarshalUnique([]byte(row.Evidence), &evidence) != nil || intent.Usage == nil || intent.Usage.Metered == nil || !reflect.DeepEqual(*intent.Usage.Metered, evidence) || evidence.State != "confirmed" || row.ProviderTaskID != "" && evidence.ProviderRequestID != row.ProviderTaskID {
		return ErrInvalid
	}
	q, err := (&Session{call: row}).NativeQuote()
	if err != nil {
		return err
	}
	quota, err := strconv.ParseInt(intent.NativeQuota, 10, 64)
	if err != nil {
		return err
	}
	amount, err := q.AmountForScope(quota, row.CreditScope)
	if err != nil || amount != row.Charged || amount != intent.Amount || q.PricingDigest != intent.PricingDigest {
		return ErrInvalid
	}
	var receipt struct {
		RemoteCall
		NativeQuota string `json:"native_quota"`
		Usage       *Usage `json:"usage"`
	}
	if err := a.client.do(ctx, http.MethodGet, "/internal/v1/calls/"+row.CallID, "", nil, &receipt); err != nil {
		return err
	}
	if !receipt.RemoteCall.matches(row) || receipt.Status != "SETTLED" || receipt.Charged != row.Charged || receipt.NativeQuota != intent.NativeQuota || !reflect.DeepEqual(receipt.Usage, intent.Usage) {
		return ErrInvalid
	}
	var current Call
	if a.db.WithContext(ctx).First(&current, "call_id = ?", row.CallID).Error != nil || current.Status != "SETTLED" || current.EvidenceConflict || current.Evidence != row.Evidence || current.Intent != row.Intent || current.ProviderTaskID != row.ProviderTaskID {
		return ErrPending
	}
	return nil
}

func (s *Session) ReserveNative(ctx context.Context, q NativeQuote, snapshot string, minimumQuota ...int64) error {
	if !s.NativePricing() {
		return ErrInvalid
	}
	group := s.adapter.config.NativeGroup
	if s.ProductCredit() && s.PinnedKnowledge() {
		group = s.adapter.config.Group
	}
	if s.nativeCredential == "" || s.call.Status != "ADMITTED" || q.Group != group || q.BillingModel == "" || len(snapshot) > 1<<20 {
		return ErrInvalid
	}
	if s.call.PricingSource == NativeCreditPricingSource && (s.nativeCreditConversion == nil || q.Conversion != *s.nativeCreditConversion) {
		return ErrInvalid
	}
	channel, e := strconv.Atoi(q.ChannelID)
	route, re := strconv.ParseInt(q.RouteID, 10, 64)
	if e != nil || channel < 1 || strconv.Itoa(channel) != q.ChannelID || re != nil || route < 0 || strconv.FormatInt(route, 10) != q.RouteID {
		return ErrInvalid
	}
	encoded, err := common.Marshal(q)
	if err != nil {
		return err
	}
	s.call.NativeQuoteJSON, s.call.NativePriceSnapshot = string(encoded), snapshot
	if _, err := s.NativeQuote(); err != nil {
		return err
	}
	var minimumCreditAmount string
	if len(minimumQuota) != 0 {
		maximum, _ := strconv.ParseInt(q.NativeQuotaMax, 10, 64)
		if !s.ProductCredit() || len(minimumQuota) != 1 || minimumQuota[0] <= 0 || minimumQuota[0] > maximum {
			return ErrInvalid
		}
		minimumCreditAmount, err = q.AmountForScope(minimumQuota[0], s.call.CreditScope)
		if err != nil {
			return err
		}
	}
	s.call.Amount = q.MaxAmount
	updates := map[string]any{"status": "RESERVING", "native_quote_json": s.call.NativeQuoteJSON, "native_price_snapshot": snapshot, "amount": q.MaxAmount}
	if s.ProductBandedChat() {
		updates["text_input_snapshot"] = s.call.TextInputSnapshot
		updates["reservation_quantity"] = s.call.ReservationQuantity
		updates["reservation_output"] = s.call.ReservationOutput
	}
	saved := s.adapter.db.WithContext(ctx).Model(&Call{}).Where("call_id = ? AND status = ?", s.call.CallID, "ADMITTED").Updates(updates)
	if saved.Error != nil {
		return saved.Error
	}
	if saved.RowsAffected != 1 {
		return ErrPending
	}
	s.call.Status = "RESERVING"
	var remote RemoteCall
	err = s.adapter.client.do(ctx, http.MethodPost, "/internal/v1/calls/native-reserve", s.call.CallID, NativeReserveRequest{ManagedOperation: s.managedOperation, MinimumCreditAmount: minimumCreditAmount, CreditScope: s.call.CreditScope, ProductAuthorization: productAuthorizationFrom(ctx).proof, Credential: s.nativeCredential, InstanceID: s.call.InstanceID, TaskID: s.call.TaskID, CallID: s.call.CallID, RequestDigest: s.call.Digest, Model: s.call.Model, WireOperation: s.call.WireOperation, Quote: q}, &remote)
	if err != nil || !remote.matches(s.call) || remote.Status != "RESERVED" || remote.Held != q.MaxAmount {
		status := "RESERVE_UNKNOWN"
		var rejected *remoteError
		if errors.As(err, &rejected) && rejected.reserveRejected {
			status = "REJECTED"
		}
		_ = s.adapter.db.WithContext(ctx).Model(&Call{}).Where("call_id = ? AND status = ?", s.call.CallID, "RESERVING").Update("status", status).Error
		s.call.Status = status
		// Purchase guidance requires both a proven no-hold refusal and the
		// authenticated native CREDIT contract. Budget/cash errors never qualify.
		if status == "REJECTED" && rejected.reserveCode == "insufficient_credits" && s.call.PricingSource == NativeCreditPricingSource && s.call.Asset == "CREDIT/6" && s.call.FundingMode == "credits" {
			return ErrNativeCreditsRequired
		}
		if status == "REJECTED" && rejected.reserveCode == "managed_model_not_allowed" &&
			s.ProductCredit() && s.call.OriginProductID == "superwagie" {
			return ErrManagedModelNotAllowed
		}
		if status == "REJECTED" && rejected.reserveCode == "product_membership_expired" && s.ProductCredit() {
			return ErrProductMembershipExpired
		}
		if err != nil {
			return err
		}
		return ErrPending
	}
	s.call.FundingSnapshot = remote.fundingSnapshot()
	saved = s.adapter.db.WithContext(ctx).Model(&Call{}).Where("call_id = ? AND status = ?", s.call.CallID, "RESERVING").Updates(map[string]any{"status": "RESERVED", "funding_snapshot": s.call.FundingSnapshot})
	if saved.Error != nil {
		return saved.Error
	}
	if saved.RowsAffected != 1 {
		return ErrPending
	}
	s.call.Status = "RESERVED"
	return nil
}

func (remote RemoteCall) matchesNative(row Call) bool {
	var q NativeQuote
	if common.UnmarshalUnique([]byte(row.NativeQuoteJSON), &q) != nil || remote.NativePricing == nil || !reflect.DeepEqual(q, *remote.NativePricing) {
		return false
	}
	if row.FundingSnapshot != "" && row.FundingSnapshot != remote.fundingSnapshot() {
		return false
	}
	if remote.CredentialVersion != row.CredentialVersion || remote.TaskID != row.TaskID {
		return false
	}
	if remote.Rate.ID != "" || remote.Rate.Asset != row.Asset || remote.Rate.Semantics != "llmrouter_native" {
		return false
	}
	fundingProduct := row.FundingProductID
	if row.CreditScope == "" && fundingProduct == "" {
		fundingProduct = "llmrouter"
	}
	return remote.CreditScope == row.CreditScope && remote.InstanceID == row.InstanceID && remote.CallID == row.CallID && remote.CredentialID == row.CredentialID && remote.ActorID == row.ActorID && remote.AccountID == row.AccountID && remote.OriginProductID == row.OriginProductID && remote.RequestDigest == row.Digest && remote.WireOperation == row.WireOperation && remote.Rate.Model == row.Model && remote.ChargeAsset == row.Asset && remote.FundingMode == row.FundingMode && remote.FundingProductID == fundingProduct && remote.BillingProfileVersion == row.BillingProfileVersion
}

func (s *Session) dispatchNative(ctx context.Context, channel int) error {
	q, err := s.NativeQuote()
	if err != nil || q.ChannelID != strconv.Itoa(channel) || s.err != nil || s.uncertain || s.ProductCredit() && s.nativeSend == "" {
		return ErrPending
	}
	var grant Claims
	if s.ProductCredit() {
		grant, err = s.adapter.managedGrant(ctx, s.nativeCredential, "", "", "receipt")
		if err == nil {
			err = s.adapter.validateManagedCredential(ctx, s.nativeCredential, "", "", grant, true)
		}
	} else {
		grant, err = s.adapter.ManagedModelGrant(ctx, s.nativeCredential)
	}
	if err != nil {
		return err
	}
	if (!s.ProductCredit() && !grant.NativePricing()) || !s.call.matchesCreditScope(grant) || grant.ID != s.call.CredentialID || grant.CredentialVersion != s.call.CredentialVersion || grant.ActorID != s.call.ActorID || grant.AccountID != s.call.AccountID || grant.OriginProductID != s.call.OriginProductID || grant.InstanceID != s.call.InstanceID || grant.Asset != s.call.Asset || grant.FundingMode != s.call.FundingMode || grant.PricingSource != s.call.PricingSource {
		return ErrConflict
	}
	if s.adapter.nativeDispatch == nil {
		return ErrPending
	}
	if err := s.adapter.nativeDispatch(ctx, grant, s.call.Model, q); err != nil {
		return err
	}
	if s.call.WireOperation == "bailian.audio.speech.create" && s.nativeDispatchGuard == nil {
		return ErrPending
	}
	if s.nativeDispatchGuard != nil {
		if err := s.nativeDispatchGuard(ctx); err != nil {
			return err
		}
	}
	attempt := Attempt{CallID: s.call.CallID, ChannelID: channel, Status: "DISPATCHED"}
	if s.ProductCredit() {
		attempt.UpstreamIdentity = s.nativeSend
	}
	err = s.adapter.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&Call{}).Where("call_id = ? AND status = ?", s.call.CallID, "RESERVED").Update("status", "UNKNOWN")
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrPending
		}
		return tx.Create(&attempt).Error
	})
	if err != nil {
		return err
	}
	s.call.Status, s.attempt, s.uncertain, s.safeFailure = "UNKNOWN", attempt, true, false
	s.nativeCredential = ""
	return nil
}

func (s *Session) SettleNative(ctx context.Context, actual int, usage *Usage) error {
	return s.settleNative(ctx, int64(actual), usage, nil)
}

func (s *Session) settleNative(ctx context.Context, actual int64, usage *Usage, artifact *productMediaResult) error {
	q, err := s.NativeQuote()
	if err != nil {
		return err
	}
	amount, err := q.AmountForScope(actual, s.call.CreditScope)
	if err != nil {
		return err
	}
	updates := map[string]any{"status": "SETTLE_PENDING"}
	if s.ProductCredit() {
		if usage == nil || usage.Metered == nil || usage.Metered.State != "confirmed" || s.adapter.productCreditMetering == nil {
			return ErrPending
		}
		confirmed, err := s.adapter.productCreditMetering(s.call.NativePriceSnapshot, *usage.Metered)
		if err != nil || confirmed != actual {
			return ErrInvalid
		}
		raw, err := common.Marshal(usage.Metered)
		if err != nil {
			return err
		}
		if s.call.Evidence != "" && s.call.Evidence != string(raw) {
			return ErrConflict
		}
		updates["evidence"] = string(raw)
	}
	intent, err := common.Marshal(NativeSettleRequest{PricingDigest: q.PricingDigest, NativeQuota: strconv.FormatInt(actual, 10), Amount: amount, Usage: usage})
	if err != nil {
		return err
	}
	updates["intent"] = string(intent)
	err = s.adapter.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		query := tx.Model(&Call{}).Where("call_id = ? AND status = ? AND evidence_conflict = ?", s.call.CallID, "UNKNOWN", false)
		if evidence, ok := updates["evidence"]; ok {
			query = query.Where("(evidence = ? OR evidence = '' OR evidence IS NULL)", evidence)
		}
		saved := query.Updates(updates)
		if saved.Error != nil {
			return saved.Error
		}
		if saved.RowsAffected != 1 {
			return ErrPending
		}
		if artifact != nil {
			return tx.Create(artifact).Error
		}
		return nil
	})
	if err != nil {
		return err
	}
	s.call.Status, s.call.Intent = "SETTLE_PENDING", string(intent)
	return s.adapter.deliverNative(ctx, s.call)
}

func (s *Session) finishNative(ctx context.Context) error {
	if s.err != nil {
		return s.err
	}
	if s.call.Status == "ADMITTED" {
		return s.adapter.db.WithContext(ctx).Model(&Call{}).Where("call_id = ? AND status = ?", s.call.CallID, "ADMITTED").Update("status", "REJECTED").Error
	}
	if s.call.Status == "SETTLE_PENDING" || s.call.Status == "RELEASE_PENDING" {
		return s.adapter.deliverNative(ctx, s.call)
	}
	if !s.safeFailure || s.uncertain || s.call.Status != "RESERVED" {
		return nil
	}
	intent, _ := common.Marshal(map[string]string{"reason": "definitive_no_output_failure"})
	result := s.adapter.db.WithContext(ctx).Model(&Call{}).Where("call_id = ? AND status = ?", s.call.CallID, "RESERVED").Updates(map[string]any{"status": "RELEASE_PENDING", "intent": string(intent)})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrPending
	}
	s.call.Status, s.call.Intent = "RELEASE_PENDING", string(intent)
	return s.adapter.deliverNative(ctx, s.call)
}

func (a *Adapter) deliverNative(ctx context.Context, row Call) error {
	var current Call
	if err := a.db.WithContext(ctx).First(&current, "call_id = ?", row.CallID).Error; err != nil {
		return err
	}
	if current.EvidenceConflict || current.NativeQuoteJSON != row.NativeQuoteJSON || current.NativePriceSnapshot != row.NativePriceSnapshot || current.Intent != row.Intent {
		return ErrConflict
	}
	if current.Status == "SETTLED" || current.Status == "RELEASED" {
		return nil
	}
	q, err := (&Session{call: current}).NativeQuote()
	if err != nil {
		return err
	}
	operation, terminal := "native-settle", "SETTLED"
	var body any
	if current.Status == "RELEASE_PENDING" {
		operation, terminal = "release", "RELEASED"
		if common.UnmarshalUnique([]byte(row.Intent), &body) != nil {
			return ErrInvalid
		}
	} else if current.Status == "SETTLE_PENDING" {
		var settle NativeSettleRequest
		if common.UnmarshalUnique([]byte(row.Intent), &settle) != nil || settle.PricingDigest != q.PricingDigest {
			return ErrInvalid
		}
		quota, err := strconv.ParseInt(settle.NativeQuota, 10, 64)
		if current.CreditScope == ProductCreditScope {
			var evidence metering.Usage
			if settle.Usage == nil || settle.Usage.Metered == nil || common.UnmarshalUnique([]byte(current.Evidence), &evidence) != nil ||
				!reflect.DeepEqual(evidence, *settle.Usage.Metered) || a.productCreditMetering == nil {
				return ErrInvalid
			}
			actual, err := a.productCreditMetering(current.NativePriceSnapshot, evidence)
			if err != nil || actual != quota {
				return ErrInvalid
			}
		}
		amount, amountErr := q.AmountForScope(quota, row.CreditScope)
		if err != nil || amountErr != nil || amount != settle.Amount || strconv.FormatInt(quota, 10) != settle.NativeQuota {
			return ErrInvalid
		}
		held, heldErr := strconv.ParseInt(row.Amount, 10, 64)
		actual, actualErr := strconv.ParseInt(amount, 10, 64)
		if heldErr != nil || actualErr != nil || held < 0 {
			return ErrInvalid
		}
		if actual > held {
			var remote RemoteCall
			if err := a.client.do(ctx, http.MethodPost, "/internal/v1/calls/"+row.CallID+"/topup", row.CallID+":native-topup:"+amount, map[string]string{"amount": strconv.FormatInt(actual-held, 10)}, &remote); err != nil {
				return err
			}
			if !remote.matchesNativeTopUp(row) || remote.Status != "RESERVED" || remote.Held != amount {
				return ErrPending
			}
			if err := a.db.WithContext(ctx).Model(&Call{}).Where("call_id = ? AND status = ?", row.CallID, "SETTLE_PENDING").Updates(map[string]any{"amount": amount, "funding_snapshot": remote.fundingSnapshot()}).Error; err != nil {
				return err
			}
			row.Amount, row.FundingSnapshot = amount, remote.fundingSnapshot()
		}
		body = settle
	} else {
		return ErrPending
	}
	var remote RemoteCall
	if err := a.client.do(ctx, http.MethodPost, "/internal/v1/calls/"+row.CallID+"/"+operation, row.CallID+":"+operation, body, &remote); err != nil {
		return err
	}
	if !remote.matches(row) || remote.Status != terminal {
		return ErrPending
	}
	if settle, ok := body.(NativeSettleRequest); ok && remote.Charged != settle.Amount {
		return ErrPending
	}
	result := a.db.WithContext(ctx).Model(&Call{}).Where("call_id = ? AND status = ? AND intent = ? AND evidence_conflict = ?", row.CallID, current.Status, row.Intent, false).Updates(map[string]any{"status": terminal, "charged": remote.Charged})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrPending
	}
	return nil
}

func (s *Session) NativeOriginProduct() string {
	if s.NativePricing() {
		return s.call.OriginProductID
	}
	return ""
}
