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
	Credential    string      `json:"credential"`
	InstanceID    string      `json:"instance_id"`
	TaskID        string      `json:"task_id"`
	CallID        string      `json:"call_id"`
	RequestDigest string      `json:"request_digest"`
	Model         string      `json:"model"`
	WireOperation string      `json:"wire_operation"`
	Quote         NativeQuote `json:"quote"`
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
	if quota < 0 || quota > common.MaxQuota || q.Asset != "CNY/6" || q.Conversion.Version != "lr-quota-usd-cny-v1" || q.Conversion.Rounding != "lr-cumulative-ceil-v1" {
		return "", ErrInvalid
	}
	n, nOK := new(big.Int).SetString(q.Conversion.Numerator, 10)
	d, dOK := new(big.Int).SetString(q.Conversion.Denominator, 10)
	if !nOK || !dOK || n.Sign() <= 0 || d.Sign() <= 0 || !n.IsInt64() || !d.IsInt64() || n.String() != q.Conversion.Numerator || d.String() != q.Conversion.Denominator {
		return "", ErrInvalid
	}
	value := new(big.Int).Mul(n, big.NewInt(int64(quota)))
	value.Add(value, new(big.Int).Sub(d, big.NewInt(1))).Quo(value, d)
	if !value.IsInt64() {
		return "", ErrInvalid
	}
	return value.String(), nil
}

func (s *Session) NativePricing() bool { return s != nil && s.call.PricingSource == "llmrouter_native" }

func (a *Adapter) AdmitNative(ctx context.Context, credential, task, id string, wire WireOperation, model string, body []byte) (*Session, error) {
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
	digestInput, err := common.Marshal(struct {
		Task, Model string
		Wire        WireOperation
		Body        []byte
	}{task, model, wire, body})
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(digestInput)
	row := Call{CallID: id, ActorID: grant.ActorID, AccountID: grant.AccountID, OriginProductID: grant.OriginProductID, CredentialID: grant.ID, CredentialKind: grant.CredentialKind, InstanceID: grant.InstanceID, TaskID: task, Model: model, Digest: hex.EncodeToString(digest[:]), DigestVersion: "native-pricing-v1", Operation: wireBillingOperation(wire.ID), WireOperation: wire.ID, WirePathModel: wire.PathModel, WireMode: wire.Mode, Asset: grant.Asset, FundingMode: grant.FundingMode, FundingProductID: "llmrouter", BillingProfileVersion: grant.BillingProfileVersion, PricingSource: grant.PricingSource, CredentialVersion: grant.CredentialVersion, Status: "ADMITTED"}
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
	return &Session{adapter: a, call: row, nativeCredential: credential, safeFailure: true}, nil
}

func (s *Session) NativeQuote() (NativeQuote, error) {
	var q NativeQuote
	if !s.NativePricing() || common.UnmarshalUnique([]byte(s.call.NativeQuoteJSON), &q) != nil {
		return q, ErrInvalid
	}
	sum := sha256.Sum256([]byte(s.call.NativePriceSnapshot))
	maximum, err := strconv.Atoi(q.NativeQuotaMax)
	amount, amountErr := q.Amount(maximum)
	if err != nil || amountErr != nil || strconv.Itoa(maximum) != q.NativeQuotaMax || q.PricingVersion != "lr-native-v1" || q.PricingDigest != hex.EncodeToString(sum[:]) || q.WireOperation != s.call.WireOperation || amount != q.MaxAmount {
		return q, ErrInvalid
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
	if common.UnmarshalUnique([]byte(row.Intent), &intent) != nil || common.UnmarshalUnique([]byte(row.Evidence), &evidence) != nil || intent.Usage == nil || intent.Usage.Metered == nil || !reflect.DeepEqual(*intent.Usage.Metered, evidence) || evidence.State != "confirmed" || evidence.ProviderRequestID != row.ProviderTaskID {
		return ErrInvalid
	}
	q, err := (&Session{call: row}).NativeQuote()
	if err != nil {
		return err
	}
	quota, err := strconv.Atoi(intent.NativeQuota)
	if err != nil {
		return err
	}
	amount, err := q.Amount(quota)
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

func (s *Session) ReserveNative(ctx context.Context, q NativeQuote, snapshot string) error {
	if !s.NativePricing() || s.nativeCredential == "" || s.call.Status != "ADMITTED" || q.Group != s.adapter.config.NativeGroup || q.BillingModel == "" || len(snapshot) > 1<<20 {
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
	s.call.Amount = q.MaxAmount
	saved := s.adapter.db.WithContext(ctx).Model(&Call{}).Where("call_id = ? AND status = ?", s.call.CallID, "ADMITTED").Updates(map[string]any{"status": "RESERVING", "native_quote_json": s.call.NativeQuoteJSON, "native_price_snapshot": snapshot, "amount": q.MaxAmount})
	if saved.Error != nil {
		return saved.Error
	}
	if saved.RowsAffected != 1 {
		return ErrPending
	}
	s.call.Status = "RESERVING"
	var remote RemoteCall
	err = s.adapter.client.do(ctx, http.MethodPost, "/internal/v1/calls/native-reserve", s.call.CallID, NativeReserveRequest{Credential: s.nativeCredential, InstanceID: s.call.InstanceID, TaskID: s.call.TaskID, CallID: s.call.CallID, RequestDigest: s.call.Digest, Model: s.call.Model, WireOperation: s.call.WireOperation, Quote: q}, &remote)
	if err != nil || !remote.matches(s.call) || remote.Status != "RESERVED" || remote.Held != q.MaxAmount {
		status := "RESERVE_UNKNOWN"
		var rejected *remoteError
		if errors.As(err, &rejected) && rejected.reserveRejected {
			status = "REJECTED"
		}
		_ = s.adapter.db.WithContext(ctx).Model(&Call{}).Where("call_id = ? AND status = ?", s.call.CallID, "RESERVING").Update("status", status).Error
		s.call.Status = status
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
	if remote.Rate.ID != "" || remote.Rate.Asset != "CNY/6" || remote.Rate.Semantics != "llmrouter_native" {
		return false
	}
	return remote.InstanceID == row.InstanceID && remote.CallID == row.CallID && remote.CredentialID == row.CredentialID && remote.ActorID == row.ActorID && remote.AccountID == row.AccountID && remote.OriginProductID == row.OriginProductID && remote.RequestDigest == row.Digest && remote.WireOperation == row.WireOperation && remote.Rate.Model == row.Model && remote.ChargeAsset == "CNY/6" && remote.FundingMode == "prepaid_cash" && remote.FundingProductID == "llmrouter" && remote.BillingProfileVersion == row.BillingProfileVersion
}

func (s *Session) dispatchNative(ctx context.Context, channel int) error {
	q, err := s.NativeQuote()
	if err != nil || q.ChannelID != strconv.Itoa(channel) || s.err != nil || s.uncertain {
		return ErrPending
	}
	grant, err := s.adapter.ManagedModelGrant(ctx, s.nativeCredential)
	if err != nil {
		return err
	}
	if !grant.NativePricing() || grant.ID != s.call.CredentialID || grant.CredentialVersion != s.call.CredentialVersion || grant.ActorID != s.call.ActorID || grant.AccountID != s.call.AccountID || grant.OriginProductID != s.call.OriginProductID || grant.InstanceID != s.call.InstanceID {
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
	q, err := s.NativeQuote()
	if err != nil {
		return err
	}
	amount, err := q.Amount(actual)
	if err != nil {
		return err
	}
	intent, err := common.Marshal(NativeSettleRequest{PricingDigest: q.PricingDigest, NativeQuota: strconv.Itoa(actual), Amount: amount, Usage: usage})
	if err != nil {
		return err
	}
	saved := s.adapter.db.WithContext(ctx).Model(&Call{}).Where("call_id = ? AND status = ? AND evidence_conflict = ?", s.call.CallID, "UNKNOWN", false).Updates(map[string]any{"status": "SETTLE_PENDING", "intent": string(intent)})
	if saved.Error != nil {
		return saved.Error
	}
	if saved.RowsAffected != 1 {
		return ErrPending
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
		quota, err := strconv.Atoi(settle.NativeQuota)
		amount, amountErr := q.Amount(quota)
		if err != nil || amountErr != nil || amount != settle.Amount || strconv.Itoa(quota) != settle.NativeQuota {
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
			if !remote.matches(row) || remote.Status != "RESERVED" || remote.Held != amount {
				return ErrPending
			}
			if err := a.db.WithContext(ctx).Model(&Call{}).Where("call_id = ? AND status = ?", row.CallID, "SETTLE_PENDING").Update("amount", amount).Error; err != nil {
				return err
			}
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
