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
	"github.com/QuantumNous/new-api/setting/billing_setting"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func (a *Adapter) AdmitOperation(ctx context.Context, credential, task, id, operation, contentType string, body []byte) (*Session, error) {
	return a.admitWireOperation(ctx, credential, task, id, WireOperation{ID: legacyWireOperation(operation)}, contentType, body)
}
func (a *Adapter) admitWireOperation(ctx context.Context, credential, task, id string, wire WireOperation, contentType string, body []byte, prepare ...KnowledgePreparation) (*Session, error) {
	if nativeHistoryWire(wire.ID) && strings.HasPrefix(credential, "uh_key_") {
		task = "task_" + id
		if len(task) > 128 {
			task = "task_" + nativeDigest([]byte(id))
		}
	}
	operation := wireBillingOperation(wire.ID)
	if operation == "" {
		return nil, ErrInvalid
	}
	if wire.ID == "gemini.generate" || wire.ID == "gemini.images.generate" {
		if wire.PathModel == "" || (wire.Mode != "generateContent" && wire.Mode != "streamGenerateContent") {
			return nil, ErrInvalid
		}
	} else if (wire.ID == "rerank.create" || wire.ID == "multimodal.embeddings.create") && wire.PathModel == "" && (wire.Mode == "" || wire.Mode == "knowledge-native-v1") {
	} else if wire.ID == "ocr.jobs.create" && wire.PathModel == "" && (wire.Mode == "" || wire.Mode == "paddle-native-v1") {
	} else if wire.PathModel != "" || wire.Mode != "" {
		return nil, ErrInvalid
	}

	if !a.Enabled() || !stableID.MatchString(id) || !stableID.MatchString(task) || !IsModelCredential(credential) || int64(len(body)) > a.OperationBodyLimit(operation) {
		return nil, ErrInvalid
	}
	if nativeHistoryWire(wire.ID) {
		if exists, e := a.NativeHistoryReplay(ctx, credential, task, id, wire, body); exists || e != nil {
			return nil, e
		}
	}
	if wire.ID == "ocr.jobs.create" || wire.ID == "dashscope.ocr.recognitions.create" {
		if exists, e := a.OCRReplay(ctx, credential, task, id, wire, contentType, body); exists || e != nil {
			return nil, e
		}
	}
	if IsImageWire(wire.ID) {
		if exists, e := a.ImageReplay(ctx, credential, task, id, wire, contentType, body); exists || e != nil {
			return nil, e
		}
	}
	if IsAudioWire(wire.ID) {
		if exists, e := a.AudioReplay(ctx, credential, task, id, wire.ID, contentType, body); exists || e != nil {
			return nil, e
		}
	}
	if wire.ID == "videos.create" {
		if exists, e := a.VideoReplay(ctx, credential, task, id, wire, contentType, body); exists || e != nil {
			return nil, e
		}
	}
	var req operationRequest
	var err error
	switch wire.ID {
	case "images.generations.create", "images.edits.create", "dashscope.images.generate", "gemini.images.generate":
		model, e := a.ImageRequestModel(wire, contentType, body)
		if e != nil {
			return nil, e
		}
		p, ok := a.ImagePolicy(model, wire.ID)
		if !ok {
			return nil, ErrInvalid
		}
		req, err = parseImageOperation(p, wire, contentType, body)
	case "dashscope.ocr.recognitions.create":
		req, err = parseQwenOCR(a.config, contentType, body)
	case "ocr.jobs.create":
		req, err = parseOperation(a.config, operation, contentType, body)
		p := a.config.OCRPolicies[wire.ID][req.Model]
		if err != nil || !p.valid(req.Model, wire.ID) {
			return nil, ErrInvalid
		}
		snapshot, _ := common.Marshal(frozenOCR{p})
		req.MediaSnapshot = string(snapshot)
	case "audio.speech.create", "audio.transcriptions.create", "dashscope.audio.transcriptions.create", "bailian.audio.speech.create", "elevenlabs.audio.speech.create":
		model, e := audioRequestModel(wire.ID, contentType, body, a.config)
		if e != nil {
			return nil, e
		}
		p, ok := a.config.AudioPolicies[wire.ID][model]
		if !ok {
			return nil, ErrInvalid
		}
		req, err = parseAudioOperation(a.config, p, contentType, body)
	case "embeddings.create":
		if contentType != "application/json" {
			return nil, ErrInvalid
		}
		var request map[string]any
		if common.UnmarshalUnique(body, &request) != nil {
			return nil, ErrInvalid
		}
		model, _ := request["model"].(string)
		req, err = parseKnowledgeEmbedding(a.config, a.config.KnowledgePolicies[wire.ID][model], body)
	case "rerank.create", "multimodal.embeddings.create":
		if contentType != "application/json" {
			return nil, ErrInvalid
		}
		var request map[string]any
		if common.UnmarshalUnique(body, &request) != nil {
			return nil, ErrInvalid
		}
		model, _ := request["model"].(string)
		p := a.config.KnowledgePolicies[wire.ID][model]
		if wire.ID == "rerank.create" {
			req, err = parseKnowledgeRerank(a.config, p, body, wire.Mode == "knowledge-native-v1")
		} else {
			req, err = parseKnowledgeMultimodal(a.config, p, body, wire.Mode == "knowledge-native-v1")
		}
	case "responses.compact":
		if contentType != "application/json" {
			return nil, ErrInvalid
		}
		var request map[string]any
		if common.UnmarshalUnique(body, &request) != nil {
			return nil, ErrInvalid
		}
		model, _ := request["model"].(string)
		policy, ok := a.config.CompactPolicies[model]
		if !ok || !policy.valid(a.config, model) {
			return nil, ErrInvalid
		}
		req, err = parseCompactOperation(a.config, policy, body)

	case "claude.messages.create":
		if contentType != "application/json" {
			return nil, ErrInvalid
		}
		req, err = parseClaudeOperation(a.config, body)
	case "gemini.generate":
		if contentType != "application/json" {
			return nil, ErrInvalid
		}
		req, err = parseGeminiOperation(a.config, wire.PathModel, body)
	default:
		req, err = parseOperation(a.config, operation, contentType, body)
	}
	if err == nil && operation == "video" {
		if p, ok := a.config.VideoPolicies["videos.create"][req.Model]; ok {
			req, err = parseVideoOperation(p, contentType, body)
		}
	}
	if err != nil {
		return nil, err
	}
	var grant Claims
	if err = a.client.do(ctx, http.MethodPost, "/internal/v1/model-credentials/verify", "", map[string]string{"token": credential, "model": req.Model, "instance_id": a.config.InstanceID, "wire_operation": wire.ID}, &grant); err != nil {
		return nil, err
	}
	if a.validateModelCredential(credential, task, req.Model, grant) != nil || !allowsWire(grant, req.Model, wire.ID) {
		return nil, ErrInvalid
	}
	continuity := ""
	if nativeHistoryWire(wire.ID) {
		task, continuity, err = a.admitNativeContinuity(ctx, grant, task, req.Model, wire.ID, req.Canonical)
		if err != nil {
			return nil, err
		}
	}
	asset, mode, err := normalizeFunding(grant.Asset, grant.FundingMode)
	if err != nil || grant.BillingProfileVersion < 0 {
		return nil, ErrInvalid
	}
	if wire.ID == "multimodal.embeddings.create" {
		var old Call
		existing := a.db.WithContext(ctx).First(&old, "call_id = ?", id).Error
		if existing == nil {
			// Never refetch original media for a duplicate, including UNKNOWN. Original
			// body and all identity fields are compared below using the saved freeze.
			req.MediaSnapshot = old.MediaSnapshot
		} else if !errors.Is(existing, gorm.ErrRecordNotFound) {
			return nil, existing
		} else {
			if len(prepare) != 1 || prepare[0] == nil {
				return nil, ErrInvalid
			}
			var frozen frozenKnowledge
			if common.UnmarshalUnique([]byte(req.MediaSnapshot), &frozen) != nil {
				return nil, ErrInvalid
			}
			effective, facts, e := prepare[0](ctx, frozen.Body, frozen.Policy)
			if e != nil {
				return nil, e
			}
			frozen.Body = effective
			frozen.Facts = facts
			snapshot, e := common.Marshal(frozen)
			if e != nil {
				return nil, e
			}
			req.MediaSnapshot = string(snapshot)
		}
	}
	if len(req.MediaSnapshot) > 160<<20 {
		return nil, ErrInvalid
	}
	digestData, _ := common.Marshal(struct {
		Version, Operation, Task, Actor, Account, Origin, Asset, Funding string
		Profile                                                          int
		Body                                                             string
	}{"platform-metered-v2", operation, task, grant.ActorID, grant.AccountID, grant.OriginProductID, asset, mode, grant.BillingProfileVersion, string(req.Canonical)})
	if req.MediaSnapshot != "" {
		digestData, _ = common.Marshal(struct {
			Request string
			Media   string
		}{string(digestData), req.MediaSnapshot})
	}
	legacySum := sha256.Sum256(digestData)
	legacyDigest := hex.EncodeToString(legacySum[:])
	if grant.Operations != nil || wire.ID != legacyWireOperation(operation) {
		digestData, _ = common.Marshal(struct {
			Version string
			Wire    WireOperation
			Scope   string
		}{"platform-wire-v3", wire, string(digestData)})
	}
	if continuity != "" {
		digestData, _ = common.Marshal(struct{ Version, Request, Continuity string }{"platform-native-history-v1", string(digestData), continuity})
	}
	sum := sha256.Sum256(digestData)
	kind := grant.CredentialKind
	if kind == "" {
		kind = "task"
	}
	row := Call{CredentialKind: kind, CallID: id, Operation: operation, ActorID: grant.ActorID, AccountID: grant.AccountID, OriginProductID: grant.OriginProductID, CredentialID: grant.ID, TaskID: task, Model: req.Model, Digest: hex.EncodeToString(sum[:]), Asset: asset, FundingMode: mode, FundingProductID: "llmrouter", BillingProfileVersion: grant.BillingProfileVersion, Status: "RESERVING"}
	row.InstanceID = a.config.InstanceID
	if grant.Operations != nil || wire.ID != legacyWireOperation(operation) {
		row.WireOperation = wire.ID
		row.WirePathModel = wire.PathModel
		row.WireMode = wire.Mode
		row.DigestVersion = "platform-wire-v3"
	}
	if continuity != "" {
		row.ContinuitySnapshot = continuity
		row.NativeRequestDigest = nativeDigest(req.Canonical)
		row.DigestVersion = "platform-native-history-v1"
	}
	var old Call
	if err = a.db.WithContext(ctx).First(&old, "call_id = ?", id).Error; err == nil {
		return nil, compareWireCall(old, row, legacyDigest)
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	var rates []Rate
	if err = a.client.do(ctx, http.MethodGet, "/internal/v1/rates", "", nil, &rates); err != nil {
		return nil, err
	}
	var chosen *Rate
	candidates := 0
	for i := range rates {
		r := &rates[i]
		if r.Model != req.Model || r.Semantics != "metered_v2" || r.Status != "published" || r.Metered == nil || r.Metered.Operation != operation {
			continue
		}
		ra, e := normalizeAsset(r.Asset)
		if e != nil {
			return nil, e
		}
		if ra != asset {
			continue
		}
		candidates++
		if candidates > 1 {
			return nil, ErrInvalid
		}
		pin := a.config.OperationRateIDs[operation][asset][req.Model]
		if pin != "" && pin != r.ID {
			continue
		}
		if chosen != nil {
			return nil, ErrInvalid
		}
		chosen = r
	}
	if chosen != nil && wire.ID == "responses.compact" && a.config.CompactPolicies[req.Model].RateIDs[asset] != chosen.ID {
		return nil, ErrInvalid
	}
	if wire.ID == "embeddings.create" || wire.ID == "rerank.create" || wire.ID == "multimodal.embeddings.create" {
		p := a.config.KnowledgePolicies[wire.ID][req.Model]
		if chosen == nil || chosen.Metered.Policy != "provider_reported_v1" || p.RateIDs[asset] != chosen.ID {
			return nil, ErrInvalid
		}
		if p.Version == "knowledge_ali_text_v1" && (len(chosen.Metered.Terms) != 1 || chosen.Metered.Terms[0].Key != "input" || chosen.Metered.Terms[0].Unit != "token") {
			return nil, ErrInvalid
		}
	}
	if chosen == nil {
		return nil, ErrInvalid
	}
	if wire.ID == "ocr.jobs.create" || wire.ID == "dashscope.ocr.recognitions.create" {
		p := a.config.OCRPolicies[wire.ID][req.Model]
		if p.UsagePolicy == "provider_tokens_v1" && (p.MaxInputTokens > a.config.MaxInputTokens || p.MaxOutputTokens > a.config.MaxOutputTokens) {
			return nil, ErrInvalid
		}
		if chosen.Metered.Policy != p.UsagePolicy || p.RateIDs[asset] != chosen.ID {
			return nil, ErrInvalid
		}
		if p.UsagePolicy == "success_request" {
			if len(chosen.Metered.Terms) != 1 || chosen.Metered.Terms[0].Key != "requests" || chosen.Metered.Terms[0].Unit != "request" {
				return nil, ErrInvalid
			}
		} else {
			limit := a.config.OperationLimits[operation][req.Model]
			terms := map[string]metering.Term{}
			for _, term := range chosen.Metered.Terms {
				terms[term.Key] = term
			}
			if len(terms) != 2 || terms["input"].Unit != "token" || terms["output"].Unit != "token" || limit.Policy != p.UsagePolicy || limit.MaxQuantity != p.MaxInputTokens {
				return nil, ErrInvalid
			}
		}
	}
	if IsAudioWire(wire.ID) {
		p := a.config.AudioPolicies[wire.ID][req.Model]
		if chosen.Metered.Policy != p.UsagePolicy || p.RateIDs[asset] != chosen.ID || len(chosen.Metered.Terms) != 1 {
			return nil, ErrInvalid
		}
	}
	if err = metering.ValidatePrice(*chosen.Metered); err != nil {
		return nil, ErrInvalid
	}
	if IsAudioWire(wire.ID) {
		p := a.config.AudioPolicies[wire.ID][req.Model]
		limit := a.config.OperationLimits[operation][req.Model]
		unit := "character"
		if operation == "transcription" {
			unit = "audio_second"
		}
		if wire.ID == "elevenlabs.audio.speech.create" {
			unit = "provider_unit"
		}
		if chosen.Metered.Terms[0].Unit != unit || (unit != "provider_unit" && limit.MaxQuantity != p.MaxQuantity && p.UsagePolicy != "unicode_scalar_v1") {
			return nil, ErrInvalid
		}
	}
	// Legacy Wan prices were expressed as yuan, but the platform ledger uses
	// micro-yuan. Keep old snapshots readable, never reserve new platform calls.
	if operation == "video" && chosen.Metered.Policy == "ali_wan26_t2v_billable_duration_v1" {
		return nil, ErrInvalid
	}
	if operation == "video" && !metering.WanCashTierPolicy(chosen.Metered.Policy) && billing_setting.ValidateNativeVideoPrice(*chosen.Metered) != nil {
		return nil, ErrInvalid
	}
	if operation == "video" {
		p, configured := a.config.VideoPolicies["videos.create"][req.Model]
		if metering.WanCashTierPolicy(chosen.Metered.Policy) || configured {
			if !configured || !p.valid(req.Model, "videos.create") || asset != "CNY/6" || p.UsagePolicy != chosen.Metered.Policy || !validWanVideoPrice(*chosen.Metered) || p.RateIDs[asset] != chosen.ID {
				return nil, ErrInvalid
			}
		}
	}
	if IsImageWire(wire.ID) {
		p := a.config.ImagePolicies[wire.ID][req.Model]
		if chosen.Metered.Policy != "successful_outputs_v1" || p.RateIDs[asset] != chosen.ID {
			return nil, ErrInvalid
		}
	}
	if operation == "image_generation" || operation == "image_edit" {
		if billing_setting.ValidateNativeImageTiers(*chosen.Metered) != nil {
			return nil, ErrInvalid
		}
	}
	if limits, ok := a.config.OperationLimits[operation][req.Model]; ok && limits.Policy != "" && limits.Policy != chosen.Metered.Policy {
		return nil, ErrInvalid
	}
	if (operation == "video" || operation == "speech") && chosen.Metered.Terms[0].Unit == "provider_unit" {
		limit := a.config.OperationLimits[operation][req.Model]
		capValue, ok := new(big.Int).SetString(limit.ProviderUnitCap, 10)
		if !ok || capValue.Sign() <= 0 || capValue.BitLen() > 31 || limit.ProviderUnit != chosen.Metered.Terms[0].ProviderUnit || limit.Policy != chosen.Metered.Policy {
			return nil, ErrInvalid
		}
		req.Quantity = int(capValue.Int64())
	}
	if operation == "speech" && chosen.Metered.Policy == "provider_reported_v1" {
		limit, configured := a.config.OperationLimits[operation][req.Model]
		if !configured || limit.Policy != "provider_reported_v1" || limit.MaxQuantity <= 0 {
			return nil, ErrInvalid
		}
		req.Quantity = limit.MaxQuantity
	}
	row.MediaSnapshot = req.MediaSnapshot
	row.ReservationQuantity = int64(req.Quantity)
	row.ReservationOutput = int64(req.Output)
	amount, err := meteredReserve(*chosen.Metered, req)
	if err != nil {
		return nil, err
	}
	snapshot, err := common.Marshal(chosen)
	if err != nil {
		return nil, err
	}
	row.RateSnapshot = string(snapshot)
	row.RateID = chosen.ID
	row.Amount = amount
	created := a.db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&row)
	if created.Error != nil {
		return nil, created.Error
	}
	if created.RowsAffected != 1 {
		if err = a.db.WithContext(ctx).First(&old, "call_id = ?", id).Error; err != nil {
			return nil, err
		}
		return nil, compareWireCall(old, row, legacyDigest)
	}
	var remote RemoteCall
	err = a.client.do(ctx, http.MethodPost, "/internal/v1/calls/reserve", id, map[string]string{"credential": credential, "task_id": task, "call_id": id, "request_digest": row.Digest, "rate_id": row.RateID, "amount": amount, "instance_id": a.config.InstanceID, "wire_operation": row.WireOperation}, &remote)
	if err != nil || !remote.matches(row) || remote.Status != "RESERVED" || (strings.HasPrefix(credential, "uh_key_") && (remote.InstanceID != a.config.InstanceID || remote.CredentialID != grant.ID)) {
		_ = a.db.WithContext(ctx).Model(&Call{}).Where("call_id = ?", id).Update("status", "RESERVE_UNKNOWN").Error
		if err != nil {
			return nil, err
		}
		return nil, ErrPending
	}
	row.FundingSnapshot = remote.fundingSnapshot()
	saved := a.db.WithContext(ctx).Model(&Call{}).Where("call_id = ? AND status = ?", id, "RESERVING").Updates(map[string]any{"status": "RESERVED", "funding_snapshot": row.FundingSnapshot})
	if saved.Error != nil {
		return nil, saved.Error
	}
	if saved.RowsAffected != 1 {
		return nil, ErrPending
	}
	row.Status = "RESERVED"
	return &Session{adapter: a, call: row, safeFailure: true, canonical: req.Canonical}, nil
}

func meteredReserve(price metering.Price, req operationRequest) (string, error) {
	u := metering.Usage{Version: "metered_v2", Operation: price.Operation, State: "confirmed", Source: "reservation_bound"}
	// Sum every tariff dimension at its maximum. This intentionally over-reserves
	// overlapping token dimensions and image tiers, so no rate choice under-reserves.
	for _, term := range price.Terms {
		if (price.Policy == "ali_wan26_t2v_billable_duration_v1" || metering.WanCashTierPolicy(price.Policy)) && term.Key != req.MeasurementKey {
			continue
		}
		n := req.Quantity
		if term.Key == "output" || term.Key == "reasoning" || (price.Operation == "multimodal_embedding" && term.Key == "visual") {
			n = req.Output
		}
		if n < 0 {
			return "", ErrInvalid
		}
		u.Measurements = append(u.Measurements, metering.Measurement{Key: term.Key, Unit: term.Unit, ProviderUnit: term.ProviderUnit, Quantity: metering.Quantity{Value: strconv.Itoa(n)}})
	}
	amount, err := metering.Calculate(price, u)
	if err != nil {
		return "", ErrInvalid
	}
	q, rem := new(big.Int), new(big.Int)
	q.QuoRem(amount.Num(), amount.Denom(), rem)
	if rem.Sign() > 0 {
		q.Add(q, big.NewInt(1))
	}
	if q.Sign() == 0 {
		q.SetInt64(1)
	}
	if q.BitLen() > 63 {
		return "", ErrInvalid
	}
	return q.String(), nil
}
func (s *Session) MeteredPrice() (metering.Price, bool) {
	var r Rate
	if s.call.Operation == "" || common.Unmarshal([]byte(s.call.RateSnapshot), &r) != nil || r.Metered == nil {
		return metering.Price{}, false
	}
	return *r.Metered, true
}
func (s *Session) CallID() string { return s.call.CallID }

// ObserveMetering persists evidence before settlement. Once conflicting evidence
// is seen, a later valid observation cannot erase that conflict.
func (s *Session) ObserveMetering(ctx context.Context, u metering.Usage) error {
	price, ok := s.MeteredPrice()
	if !ok {
		return ErrInvalid
	}
	if u.Version != "metered_v2" || u.Operation != price.Operation {
		return ErrInvalid
	}
	if u.State != "confirmed" && u.State != "missing" && u.State != "invalid" && u.State != "incomplete" {
		return ErrInvalid
	}
	if u.State == "confirmed" {
		if err := metering.ValidateUsage(price, u); err != nil {
			return ErrInvalid
		}
		for _, measurement := range u.Measurements {
			cap := s.call.ReservationQuantity
			if measurement.Key == "output" || measurement.Key == "reasoning" || (price.Operation == "multimodal_embedding" && measurement.Key == "visual") {
				cap = s.call.ReservationOutput
			}
			value, _ := new(big.Int).SetString(measurement.Quantity.Value, 10)
			bound := new(big.Int).Mul(big.NewInt(cap), new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(measurement.Quantity.Scale)), nil))
			if value.Cmp(bound) > 0 {
				u.State = "incomplete"
				break
			}
		}
	}
	if u.State == "confirmed" && nativeHistoryWire(s.WireOperation().ID) {
		input, output := new(big.Int), new(big.Int)
		for _, m := range u.Measurements {
			n, ok := new(big.Int).SetString(m.Quantity.Value, 10)
			if !ok || m.Quantity.Scale != 0 || m.Unit != "token" {
				u.State = "incomplete"
				break
			}
			switch m.Key {
			case "input", "cache_read", "cache_write":
				input.Add(input, n)
			case "output", "reasoning":
				output.Add(output, n)
			default:
				u.State = "incomplete"
			}
		}
		if input.Cmp(big.NewInt(s.call.ReservationQuantity)) > 0 || output.Cmp(big.NewInt(s.call.ReservationOutput)) > 0 {
			u.State = "incomplete"
		}
	}
	b, err := common.Marshal(u)
	if err != nil {
		return err
	}
	var conflict bool
	err = s.adapter.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Acquire the writer lock before reading evidence. A read-then-CAS loser
		// must never discard a contradictory terminal receipt. A no-op UPDATE
		// serializes this on SQLite too, without unsupported FOR UPDATE syntax.
		if err := tx.Model(&Call{}).Where("call_id = ?", s.call.CallID).UpdateColumn("updated_at", gorm.Expr("updated_at")).Error; err != nil {
			return err
		}
		var row Call
		if e := tx.First(&row, "call_id = ?", s.call.CallID).Error; e != nil {
			return e
		}
		if row.EvidenceConflict {
			return ErrConflict
		}
		if metering.WanCashTierPolicy(price.Policy) && u.State == "confirmed" {
			var frozen frozenVideo
			var storedRate Rate
			if row.Asset != "CNY/6" || row.Operation != "video" || row.WireOperation != "videos.create" || row.ProviderTaskID == "" || row.ProviderTaskID != u.ProviderRequestID || u.Source != price.Policy || common.UnmarshalUnique([]byte(row.MediaSnapshot), &frozen) != nil || common.UnmarshalUnique([]byte(row.RateSnapshot), &storedRate) != nil || storedRate.Metered == nil || !reflect.DeepEqual(*storedRate.Metered, price) || !validWanVideoPrice(price) || len(u.Measurements) != 1 {
				return ErrInvalid
			}
			key := "video_" + strings.ToLower(frozen.Resolution)
			if (key != "video_720p" && key != "video_1080p") || u.Measurements[0].Key != key {
				return ErrInvalid
			}
			quantity := u.Measurements[0].Quantity
			value, ok := new(big.Int).SetString(quantity.Value, 10)
			bound := new(big.Int).Mul(big.NewInt(row.ReservationQuantity), new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(quantity.Scale)), nil))
			if !ok || row.ReservationQuantity < 1 || value.Sign() <= 0 || value.Cmp(bound) != 0 {
				return ErrInvalid
			}
		}

		if row.Evidence != "" {
			var previous metering.Usage
			if common.Unmarshal([]byte(row.Evidence), &previous) != nil {
				return ErrInvalid
			}
			if previous.State == "confirmed" && !reflect.DeepEqual(previous, u) {
				conflict = true
				return tx.Model(&Call{}).Where("call_id = ? AND evidence = ?", row.CallID, row.Evidence).Update("evidence_conflict", true).Error
			}
		}
		if row.Status != "UNKNOWN" {
			if row.Evidence == string(b) {
				return nil
			}
			return ErrPending
		}
		saved := tx.Model(&Call{}).Where("call_id = ? AND evidence = ? AND evidence_conflict = ?", row.CallID, row.Evidence, false).Update("evidence", string(b))
		if saved.Error != nil {
			return saved.Error
		}
		if saved.RowsAffected != 1 {
			return ErrPending
		}
		return nil
	})
	if err != nil {
		return err
	}
	if conflict {
		return ErrConflict
	}
	return nil
}
func (s *Session) finishMetered(ctx context.Context) error {
	var row Call
	if err := s.adapter.db.WithContext(ctx).First(&row, "call_id = ?", s.call.CallID).Error; err != nil {
		return err
	}
	if row.EvidenceConflict {
		return ErrConflict
	}
	if row.Status == "SETTLED" || row.Status == "RELEASED" {
		return nil
	}
	if row.Status == "SETTLE_PENDING" || row.Status == "RELEASE_PENDING" {
		return s.adapter.deliver(ctx, row)
	}
	var usage metering.Usage
	status := "UNKNOWN"
	var intent any
	if row.Evidence != "" && common.Unmarshal([]byte(row.Evidence), &usage) == nil && usage.State == "confirmed" {
		price, ok := s.MeteredPrice()
		if !ok || metering.ValidateUsage(price, usage) != nil {
			return ErrInvalid
		}
		status = "SETTLE_PENDING"
		intent = struct {
			Usage        Usage  `json:"usage"`
			ProviderCost string `json:"provider_cost"`
		}{Usage{Metered: &usage}, ""}
	} else if row.Status == "RESERVED" && s.safeFailure {
		status = "RELEASE_PENDING"
		intent = map[string]string{"reason": "definitive_no_output_failure"}
	}
	if status == "UNKNOWN" {
		return nil
	}
	b, err := common.Marshal(intent)
	if err != nil {
		return err
	}
	saved := s.adapter.db.WithContext(ctx).Model(&Call{}).Where("call_id = ? AND status = ? AND evidence = ? AND evidence_conflict = ?", row.CallID, row.Status, row.Evidence, false).Updates(map[string]any{"status": status, "intent": string(b)})
	if saved.Error != nil {
		return saved.Error
	}
	if saved.RowsAffected != 1 {
		return ErrPending
	}
	row.Status = status
	row.Intent = string(b)
	_ = s.adapter.deliver(ctx, row)
	return nil
}

func (s *Session) BindTask(ctx context.Context, providerTaskID string) error {
	if providerTaskID == "" || len(providerTaskID) > 255 || s.call.Operation == "" {
		return ErrInvalid
	}
	saved := s.adapter.db.WithContext(ctx).Model(&Call{}).Where("call_id = ? AND status = ? AND (provider_task_id = ? OR provider_task_id = ?)", s.call.CallID, "UNKNOWN", "", providerTaskID).Update("provider_task_id", providerTaskID)
	if saved.Error != nil {
		return saved.Error
	}
	if saved.RowsAffected != 1 {
		return ErrConflict
	}
	return nil
}

// ObserveTask is service-only. It neither verifies nor persists a reusable model
// credential; workers must supply the durable exact provider task binding.
func (a *Adapter) ObserveTask(ctx context.Context, callID, providerTaskID string, u metering.Usage) error {
	var row Call
	if providerTaskID == "" {
		return ErrInvalid
	}
	if err := a.db.WithContext(ctx).Where("call_id = ? AND provider_task_id = ?", callID, providerTaskID).First(&row).Error; err != nil {
		return ErrInvalid
	}
	s := &Session{adapter: a, call: row}
	if err := s.ObserveMetering(ctx, u); err != nil {
		return err
	}
	return s.Finish(ctx)
}

// Task requires a fresh capability with the original actor/account/origin/task.
func (a *Adapter) Task(ctx context.Context, credential, task, callID string) (Call, error) {
	if _, err := a.Receipt(ctx, credential, task, callID); err != nil {
		return Call{}, err
	}
	var row Call
	err := a.db.WithContext(ctx).First(&row, "call_id = ?", callID).Error
	return row, err
}

// ReleaseTask is callable only with the immutable provider task binding and a
// supplier-confirmed no-output terminal. It writes RELEASE_PENDING intent first.
func (a *Adapter) ReleaseTask(ctx context.Context, callID, providerTaskID string) error {
	var row Call
	query := a.db.WithContext(ctx).Where("call_id = ?", callID)
	if providerTaskID != "" {
		query = query.Where("provider_task_id = ?", providerTaskID)
	} else {
		query = query.Where("provider_task_id = ?", "")
	}
	if err := query.First(&row).Error; err != nil {
		return ErrInvalid
	}
	if row.EvidenceConflict {
		return ErrConflict
	}
	var evidence metering.Usage
	if row.Evidence != "" {
		if common.UnmarshalJsonStr(row.Evidence, &evidence) != nil {
			return ErrInvalid
		}
		if evidence.State == "confirmed" {
			return ErrConflict
		}
	}
	if row.Status == "RELEASED" {
		return nil
	}
	if row.Status != "UNKNOWN" && row.Status != "RESERVED" {
		return ErrPending
	}
	b, _ := common.Marshal(map[string]string{"reason": "provider_confirmed_no_output"})
	saved := a.db.WithContext(ctx).Model(&Call{}).Where("call_id = ? AND provider_task_id = ? AND status IN ? AND evidence = ? AND evidence_conflict = ?", callID, providerTaskID, []string{"UNKNOWN", "RESERVED"}, row.Evidence, false).Updates(map[string]any{"status": "RELEASE_PENDING", "intent": string(b)})
	if saved.Error != nil {
		return saved.Error
	}
	if saved.RowsAffected != 1 {
		return ErrPending
	}
	row.Status = "RELEASE_PENDING"
	row.Intent = string(b)
	return a.deliver(ctx, row)
}

// BindTask reconstructs only a service-owned durable submission; callers must
// have read the provider binding from their own persisted execution record.
func (a *Adapter) BindTask(ctx context.Context, callID, providerTaskID string) error {
	var row Call
	if err := a.db.WithContext(ctx).First(&row, "call_id = ?", callID).Error; err != nil {
		return ErrInvalid
	}
	if row.ProviderTaskID == providerTaskID && providerTaskID != "" {
		return nil
	}
	return (&Session{adapter: a, call: row}).BindTask(ctx, providerTaskID)
}

// BindTaskTransaction atomically stores the provider binding and the durable
// business task; an insert failure cannot leave a bound but unpollable job.
func (s *Session) BindTaskOn(ctx context.Context, tx *gorm.DB, providerTaskID string) error {
	if providerTaskID == "" || len(providerTaskID) > 255 || tx == nil {
		return ErrInvalid
	}
	result := tx.WithContext(ctx).Model(&Call{}).Where("call_id = ? AND status = ? AND provider_task_id = ?", s.call.CallID, "UNKNOWN", "").Update("provider_task_id", providerTaskID)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrConflict
	}
	return nil
}
func (s *Session) BindTaskTransaction(ctx context.Context, providerTaskID string, persist func(*gorm.DB) error) error {
	if persist == nil {
		return ErrInvalid
	}
	return s.adapter.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if e := s.BindTaskOn(ctx, tx, providerTaskID); e != nil {
			return e
		}
		return persist(tx)
	})
}
