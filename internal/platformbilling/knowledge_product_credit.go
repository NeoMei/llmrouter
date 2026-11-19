package platformbilling

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"math/big"
	"reflect"
	"strconv"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/metering"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	productPublicKnowledgeDigest = "product-knowledge-hmac-v1"
	productBrokerKnowledgeDigest = "product-knowledge-broker-hmac-v1"
)

func (a *Adapter) admitProductKnowledge(ctx context.Context, task, id string, wire WireOperation, body []byte, pin *pinnedKnowledgeAdmission) (*Session, error) {
	grant := pin.grant.Claims
	expected, err := grant.ProductCreditCallID(ctx)
	if err != nil || id != expected || pin.actor == "" && task != "task_"+id || a.knowledgeResults == nil || a.config.NativeGroup == "" || wire.PathModel != "" {
		return nil, ErrInvalid
	}
	policy, err := a.ManagedKnowledgePolicy(pin.binding.Model, wire.ID)
	if err != nil {
		return nil, err
	}
	var request operationRequest
	switch wire.ID {
	case "embeddings.create":
		if wire.Mode != "" || policy.Version != "knowledge_ali_text_v1" {
			return nil, ErrInvalid
		}
		request, err = parseKnowledgeEmbedding(a.config, policy, body)
	case "rerank.create":
		if wire.Mode != "" && wire.Mode != "knowledge-native-v1" {
			return nil, ErrInvalid
		}
		request, err = parseKnowledgeRerank(a.config, policy, body, wire.Mode == "knowledge-native-v1")
	default:
		return nil, ErrInvalid
	}
	if err != nil {
		return nil, err
	}
	var frozen frozenKnowledge
	if common.UnmarshalUnique([]byte(request.MediaSnapshot), &frozen) != nil {
		return nil, ErrInvalid
	}
	frozen.Binding = &pin.binding
	snapshot, err := common.Marshal(frozen)
	if err != nil {
		return nil, ErrInvalid
	}
	// Fingerprint the effective request with the vault key. Low-entropy
	// request text stays protected even if the billing journal is exported.
	digestInput, err := common.Marshal([]any{ProductCreditScope, id, task, grant.ID, grant.CredentialVersion, wire, request.Canonical})
	if err != nil {
		return nil, ErrInvalid
	}
	row := Call{CreditScope: ProductCreditScope, CallID: id, TaskID: task, Operation: wireBillingOperation(wire.ID),
		ActorID: grant.ActorID, AccountID: grant.AccountID, OriginProductID: grant.OriginProductID, InstanceID: grant.InstanceID,
		Model: request.Model, WireOperation: wire.ID, WireMode: wire.Mode, CredentialID: grant.ID, CredentialKind: grant.CredentialKind,
		CredentialVersion: grant.CredentialVersion, Asset: grant.Asset, FundingMode: grant.FundingMode, FundingProductID: grant.FundingProductID,
		BillingProfileVersion: grant.BillingProfileVersion, PricingSource: grant.PricingSource, DigestVersion: productPublicKnowledgeDigest,
		MediaSnapshot: string(snapshot), ReservationQuantity: int64(policy.HardInputTokens), Status: "ADMITTED"}
	if pin.actor != "" {
		row.DigestVersion = productBrokerKnowledgeDigest
	}
	err = a.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		keyID := a.knowledgeResults.active
		var prior Call
		lookup := tx.First(&prior, "call_id = ?", id)
		if lookup.Error == nil {
			var previous frozenKnowledge
			if prior.CredentialID != row.CredentialID || !prior.matchesCreditScope(grant) || common.UnmarshalUnique([]byte(prior.MediaSnapshot), &previous) != nil {
				return ErrConflict
			}
			keyID = previous.BodyKeyID
		} else if !errors.Is(lookup.Error, gorm.ErrRecordNotFound) {
			return lookup.Error
		}
		key := a.knowledgeResults.digestKeys[keyID]
		if len(key) != sha256.Size {
			return ErrInvalid
		}
		mac := hmac.New(sha256.New, key)
		_, _ = mac.Write(digestInput)
		row.Digest = hex.EncodeToString(mac.Sum(nil))
		if lookup.Error == nil {
			return compareCall(prior, row)
		}
		if err := a.preparePinnedCallTx(tx, &row, pin); err != nil {
			return err
		}
		created := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&row)
		if created.Error != nil {
			return created.Error
		}
		if created.RowsAffected != 1 {
			return ErrDuplicate
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &Session{adapter: a, call: row, safeFailure: true, knowledgeAdmission: pin, nativeCredential: pin.credential, nativeCreditConversion: grant.NativeCreditConversion}, nil
}

func (s *Session) ProductKnowledgeBound() (ManagedBinding, string, int64, bool) {
	if !s.ProductCredit() || !s.PinnedKnowledge() {
		return ManagedBinding{}, "", 0, false
	}
	frozen, err := s.adapter.knowledgeSnapshot(s.call)
	if err != nil || frozen.Binding == nil || s.call.ReservationQuantity < 1 {
		return ManagedBinding{}, "", 0, false
	}
	return *frozen.Binding, frozen.Policy.UpstreamModel, s.call.ReservationQuantity, true
}

// knowledgeTariff keeps the accepted supplier measurement contract independent
// of which configuration supplies the public tariff. Product amounts are USD
// tariff rates and must never be interpreted as already-denominated microcredits.
func (s *Session) knowledgeTariff(row Call, frozen frozenKnowledge) (metering.Price, error) {
	policy := frozen.Policy
	if row.CreditScope == ProductCreditScope {
		if frozen.Binding == nil || frozen.Binding.ChannelID != policy.ChannelID || s.adapter == nil {
			return metering.Price{}, ErrInvalid
		}
		frozenSession := Session{call: row}
		price, ok := frozenSession.MeteredPrice()
		q, err := frozenSession.NativeQuote()
		if !ok || err != nil || !validProductKnowledgePrice(price, row.Operation) ||
			q.RouteID != strconv.Itoa(frozen.Binding.RouteID) || q.ChannelID != strconv.Itoa(policy.ChannelID) || q.Group != s.adapter.config.Group ||
			q.BillingModel != policy.UpstreamModel || q.MaxAmount != row.Amount {
			return metering.Price{}, ErrInvalid
		}
		return price, nil
	}
	var rate Rate
	if common.UnmarshalUnique([]byte(row.RateSnapshot), &rate) != nil || rate.Metered == nil {
		return metering.Price{}, ErrInvalid
	}
	asset, err := normalizeAsset(row.Asset)
	if err != nil || rate.ID != row.RateID || rate.Model != row.Model || rate.Asset != asset || rate.Status != "published" || rate.Semantics != "metered_v2" ||
		policy.RateIDs[asset] != rate.ID || rate.Metered.Operation != row.Operation || metering.ValidatePrice(*rate.Metered) != nil {
		return metering.Price{}, ErrInvalid
	}
	return *rate.Metered, nil
}

func validProductKnowledgePrice(price metering.Price, operation string) bool {
	return price.Operation == operation && price.Policy == "provider_reported_v1" && len(price.Terms) == 1 &&
		price.Terms[0].Key == "input" && price.Terms[0].Unit == "token" && metering.ValidatePrice(price) == nil
}

func (a *Adapter) productKnowledgeIntent(row Call, usage metering.Usage) (NativeSettleRequest, error) {
	q, err := (&Session{call: row}).NativeQuote()
	if err != nil || a.productCreditMetering == nil {
		return NativeSettleRequest{}, ErrInvalid
	}
	actual, err := a.productCreditMetering(row.NativePriceSnapshot, usage)
	if err != nil || actual < 0 {
		return NativeSettleRequest{}, ErrInvalid
	}
	amount, err := q.AmountForScope(actual, row.CreditScope)
	held, heldErr := strconv.ParseInt(row.Amount, 10, 64)
	if err != nil || heldErr != nil || actual > held {
		return NativeSettleRequest{}, ErrInvalid
	}
	return NativeSettleRequest{PricingDigest: q.PricingDigest, NativeQuota: strconv.FormatInt(actual, 10), Amount: amount, Usage: &Usage{Metered: &usage}}, nil
}

func (a *Adapter) ReadProductKnowledgeResult(ctx context.Context, credential, task, id string) ([]byte, string, error) {
	raw, _, err := a.readProductKnowledgeResult(ctx, credential, task, id, "", "")
	if err != nil {
		return nil, "", err
	}
	return raw, nativeDigest(raw), nil
}

// ProductKnowledgeResultWithEvidence accepts the broker's original work ID,
// derives the private journal ID under current product authority and attests
// the exact sealed bytes using the existing knowledge receipt wire contract.
func (a *Adapter) ProductKnowledgeResultWithEvidence(ctx context.Context, actor, credential, task, alias string) (KnowledgeResultEvidence, error) {
	if a == nil || !a.Enabled() || !HasProductAuthorization(ctx) || !OriginalKnowledgeActor(actor) || !stableID.MatchString(alias) || !stableID.MatchString(task) {
		return KnowledgeResultEvidence{}, ErrInvalid
	}
	grant, err := a.managedGrant(ctx, credential, "", "", "receipt")
	if err != nil || a.validateManagedCredential(ctx, credential, task, "", grant, true) != nil {
		return KnowledgeResultEvidence{}, ErrInvalid
	}
	id, err := grant.productCreditCallID(alias)
	if err != nil {
		return KnowledgeResultEvidence{}, err
	}
	raw, receipt, err := a.readProductKnowledgeResult(ctx, credential, task, id, actor, alias)
	if err != nil {
		return KnowledgeResultEvidence{}, err
	}
	return encodeKnowledgeResultEvidence(ctx, raw, receipt)
}

func (a *Adapter) readProductKnowledgeResult(ctx context.Context, credential, task, id, actor, alias string) ([]byte, KnowledgeReceipt, error) {
	var receipt KnowledgeReceipt
	if a == nil || a.knowledgeResults == nil || !a.config.EnableKnowledgeResultRead || !HasProductAuthorization(ctx) {
		return nil, receipt, ErrInvalid
	}
	kind := productPublicKnowledgeDigest
	if actor != "" {
		kind = productBrokerKnowledgeDigest
	}
	authorize := func() (Call, knowledgeReadGrant, error) {
		row, err := a.Task(ctx, credential, task, id)
		if err != nil || row.CreditScope != ProductCreditScope || row.DigestVersion != kind || row.Status != "SETTLED" || row.EvidenceConflict ||
			!ApprovedKnowledgePair(row.Model, row.WireOperation) {
			return Call{}, knowledgeReadGrant{}, ErrInvalid
		}
		if _, err := a.ManagedKnowledgePolicy(row.Model, row.WireOperation); err != nil {
			return Call{}, knowledgeReadGrant{}, err
		}
		if actor == "" {
			return row, knowledgeReadGrant{}, nil
		}
		grant, err := a.managedGrant(ctx, credential, row.Model, row.WireOperation, "receipt")
		if err != nil || a.validateManagedCredential(ctx, credential, task, row.Model, grant, true) != nil || !row.matchesCreditReceiptScope(grant) ||
			grant.ID != row.CredentialID || grant.ActorID != row.ActorID || grant.AccountID != row.AccountID || grant.InstanceID != row.InstanceID || grant.OriginProductID != row.OriginProductID {
			return Call{}, knowledgeReadGrant{}, ErrInvalid
		}
		expected, err := grant.productCreditCallID(alias)
		if err != nil || expected != row.CallID {
			return Call{}, knowledgeReadGrant{}, ErrInvalid
		}
		authority, err := a.verifyProductKnowledgeActor(ctx, actor, grant)
		return row, knowledgeReadGrant{Claims: grant, ActorExpiresAt: authority.expiresAt}, err
	}
	original, _, err := authorize()
	if err != nil {
		return nil, receipt, err
	}
	if err = a.verifyKnowledgeSettlement(ctx, original); err != nil {
		return nil, receipt, err
	}
	// Recheck the original credential after the settlement read. This is
	// receipt-only authorization so the membership rollover does not erase
	// an already paid result; a revoked Key still cannot get the bytes.
	current, reader, err := authorize()
	if err != nil || !sameKnowledgeCallBinding(current, original) || current.Intent != original.Intent || current.Evidence != original.Evidence || current.Charged != original.Charged {
		return nil, receipt, ErrInvalid
	}
	v := a.knowledgeResults
	var output []byte
	var deadline int64
	err = a.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row Call
		if err := platformBillingLockedRead(tx).First(&row, "call_id = ?", id).Error; err != nil || row.Status != "SETTLED" ||
			row.EvidenceConflict || !sameKnowledgeCallBinding(row, original) || row.Intent != original.Intent || row.Evidence != original.Evidence || row.Charged != original.Charged {
			return ErrPending
		}
		var stored knowledgeResultRow
		if err := tx.First(&stored, "call_id = ?", id).Error; err != nil || stored.BindingVersion != 2 {
			return ErrPending
		}
		deadline = v.effectiveReadDeadline(stored)
		var attempt Attempt
		frozen, err := a.knowledgeSnapshot(row)
		if err != nil || tx.First(&attempt, "id = ? AND call_id = ?", stored.AttemptID, id).Error != nil ||
			attempt.Status != "DISPATCHED" || !validKnowledgeResultBinding(row, attempt, frozen) {
			return ErrInvalid
		}
		binding, err := a.ResolveManagedBinding(tx, row.OriginProductID, row.Model, row.WireOperation, frozen.Binding.PolicyDigest)
		if err != nil || !reflect.DeepEqual(binding, *frozen.Binding) {
			return ErrInvalid
		}
		aead, err := v.resultAEAD(stored)
		if err != nil || deadline <= v.now().UnixMilli() || len(stored.Ciphertext) < aead.NonceSize() {
			return ErrPending
		}
		aad, err := knowledgeResultStoredAAD(row, stored)
		if err != nil {
			return ErrInvalid
		}
		output, err = aead.Open(nil, stored.Ciphertext[:aead.NonceSize()], stored.Ciphertext[aead.NonceSize():], aad)
		if err != nil {
			return ErrInvalid
		}
		if actor != "" {
			receipt, err = historicalKnowledgeReceipt(row, attempt, frozen, reader, output, deadline, v.now().Unix())
			if err != nil {
				return err
			}
			receipt.CallID = alias
		}
		return nil
	})
	if err != nil || ctx.Err() != nil || deadline <= v.now().UnixMilli() || actor != "" && receipt.ExpiresAt <= time.Now().Unix() {
		return nil, KnowledgeReceipt{}, ErrPending
	}
	return output, receipt, nil
}

// The vault can authenticate and retain a paid result without the live adapter.
// No configured price or current exchange rate participates in this check.
func validateProductKnowledgeFinancialIntent(row Call) error {
	var evidence metering.Usage
	var intent NativeSettleRequest
	if common.UnmarshalUnique([]byte(row.Evidence), &evidence) != nil || common.UnmarshalUnique([]byte(row.Intent), &intent) != nil ||
		intent.Usage == nil || intent.Usage.Metered == nil || !reflect.DeepEqual(*intent.Usage.Metered, evidence) {
		return ErrInvalid
	}
	session := Session{call: row}
	q, err := session.NativeQuote()
	price, ok := session.MeteredPrice()
	if err != nil || !ok || !validProductKnowledgePrice(price, row.Operation) || q.PricingDigest != intent.PricingDigest || q.MaxAmount != row.Amount {
		return ErrInvalid
	}
	var wrapper struct {
		PriceSnapshot string `json:"price_snapshot"`
	}
	var snapshot struct {
		Native struct{ QuotaPerUnit string } `json:"native"`
	}
	if common.UnmarshalUnique([]byte(row.NativePriceSnapshot), &wrapper) != nil || common.UnmarshalUnique([]byte(wrapper.PriceSnapshot), &snapshot) != nil {
		return ErrInvalid
	}
	units, ok := new(big.Rat).SetString(snapshot.Native.QuotaPerUnit)
	cost, err := metering.Calculate(price, evidence)
	if !ok || err != nil || cost.Sign() < 0 {
		return ErrInvalid
	}
	cost.Mul(cost, units)
	whole, remainder := new(big.Int), new(big.Int)
	whole.QuoRem(cost.Num(), cost.Denom(), remainder)
	if remainder.Sign() > 0 {
		whole.Add(whole, big.NewInt(1))
	}
	if !whole.IsInt64() || intent.NativeQuota != whole.String() {
		return ErrInvalid
	}
	amount, err := q.AmountForScope(whole.Int64(), row.CreditScope)
	held, heldErr := strconv.ParseInt(row.Amount, 10, 64)
	if err != nil || heldErr != nil || whole.Int64() > held || amount != intent.Amount || amount != row.Charged {
		return ErrInvalid
	}
	return nil
}
