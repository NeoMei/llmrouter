package platformbilling

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/internal/knowledgebinding"
	ci "github.com/QuantumNous/new-api/internal/platformbilling/continuityindex"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Call stores immutable verified payer scope and terminal command intent. Model
// credentials are never persisted: recovery cannot authorize another dispatch.
type Call struct {
	ContinuitySnapshot  string `gorm:"type:text"`
	NativeRequestDigest string `gorm:"size:64"`
	NativeObservation   string `gorm:"type:text"`
	// Unbounded string maps to LONGTEXT on our MySQL driver and TEXT on
	// PostgreSQL/SQLite; admission caps serialized snapshots at 160 MiB.
	ImageObservation      string
	OCRObservation        string
	AudioObservation      string `gorm:"type:text"`
	MediaSnapshot         string
	WireOperation         string `gorm:"size:64"`
	WirePathModel         string `gorm:"size:255"`
	WireMode              string `gorm:"size:32"`
	DigestVersion         string `gorm:"size:32"`
	InstanceID            string `gorm:"size:128;index"`
	TelemetryGroup        string `gorm:"size:128"`
	TelemetryLatencyMS    int64
	TelemetryIsStream     bool
	TelemetryCompletedAt  int64
	TelemetryOutcome      string `gorm:"size:16"`
	ReservationQuantity   int64
	ReservationOutput     int64
	Operation             string `gorm:"size:32"`
	RateSnapshot          string `gorm:"type:text"`
	FundingSnapshot       string `gorm:"type:text"`
	Evidence              string `gorm:"type:text"`
	EvidenceConflict      bool
	ProviderTaskID        string `gorm:"size:255;index"`
	CallID                string `gorm:"primaryKey;size:128"`
	ActorID               string `gorm:"size:128"`
	AccountID             string `gorm:"size:128"`
	OriginProductID       string `gorm:"size:128"`
	CredentialID          string `gorm:"size:128"`
	CredentialKind        string `gorm:"size:16"`
	TaskID                string `gorm:"size:128"`
	Model                 string `gorm:"size:255"`
	Digest                string `gorm:"size:64"`
	RateID                string `gorm:"size:128"`
	Asset                 string `gorm:"size:16;not null;default:CREDIT/6"`
	FundingMode           string `gorm:"size:16;not null;default:credits"`
	FundingProductID      string `gorm:"size:128;not null;default:llmrouter"`
	BillingProfileVersion int    `gorm:"not null;default:0"`
	Amount                string `gorm:"size:32"`
	Status                string `gorm:"size:32;index"`
	Intent                string `gorm:"type:text"`
	Charged               string `gorm:"size:32"`
	CreatedAt             time.Time
	UpdatedAt             time.Time
}

func (Call) TableName() string { return "platform_model_calls" }

type Attempt struct {
	UpstreamIdentity string `gorm:"type:text"`
	ID               uint   `gorm:"primaryKey"`
	CallID           string `gorm:"size:128;index"`
	ChannelID        int
	Status           string `gorm:"size:32"`
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

func (Attempt) TableName() string { return "platform_model_attempts" }

type Adapter struct {
	bindingResolver  func(*gorm.DB, string, string, string) (ManagedBinding, string, error)
	bindingRotator   func(*gorm.DB, string) error
	knowledgeResults *knowledgeResultVault
	history          *ci.Index
	historyResolver  NativeHistoryResolver
	db               *gorm.DB
	config           Config
	client           *client
}
type Session struct {
	knowledgeAdmission *pinnedKnowledgeAdmission
	nativeSend         string
	canonical          []byte
	adapter            *Adapter
	call               Call
	attempt            Attempt
	requestTariff      bool
	confirmed          *Usage
	safeFailure        bool
	uncertain          bool
	err                error
}

var stableID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$`)

func New(db *gorm.DB, c Config) (*Adapter, error) {
	c.KnowledgePolicies = cloneKnowledgePolicies(c.KnowledgePolicies)
	c.InstanceID = strings.TrimSpace(c.InstanceID)
	if c.InstanceID == "" {
		c.InstanceID = "default"
	}
	for wire, models := range c.ImagePolicies {
		for model, p := range models {
			if !p.valid(model, wire) {
				return nil, ErrInvalid
			}
		}
	}
	for wire, models := range c.OCRPolicies {
		for model, p := range models {
			if !p.valid(model, wire) {
				return nil, ErrInvalid
			}
		}
	}
	for wire, models := range c.AudioPolicies {
		for model, p := range models {
			if !p.valid(model, wire) {
				return nil, ErrInvalid
			}
		}
	}
	for model, policy := range c.CompactPolicies {
		if !policy.valid(c, model) {
			return nil, ErrInvalid
		}
	}
	for wire, models := range c.NativeInputPolicies {
		for _, policy := range models {
			if !policy.valid(c, wire) {
				return nil, ErrInvalid
			}
		}
	}
	if c.NativePresetPolicies != nil {
		raw, err := common.Marshal(c.NativePresetPolicies)
		if err != nil {
			return nil, ErrInvalid
		}
		if decodeNativePresetPolicies(raw, &c) != nil {
			return nil, ErrInvalid
		}
	}
	a := &Adapter{db: db, config: c}
	if !c.Enabled {
		return a, nil
	}
	if db == nil || !stableID.MatchString(c.InstanceID) || c.Group == "" || c.Group == "auto" || c.MaxInputTokens < 1 || c.MaxInputTokens > 1<<20 || c.MaxOutputTokens < 1 || c.MaxOutputTokens > 32768 || c.MaxBodyBytes < 1 || c.MaxBodyBytes > 1<<20 {
		return nil, ErrInvalid
	}
	if c.EnableKnowledgeResultRead && !c.AllowAliEmbeddingV4 && !c.AllowAliRerank {
		return nil, ErrInvalid
	}
	var err error
	a.client, err = newClient(c)
	if err != nil {
		return nil, err
	}
	if err = db.AutoMigrate(&Call{}, &Attempt{}, &knowledgebinding.KnowledgeActiveBinding{}, &knowledgebinding.KnowledgeRoutePin{}); err != nil {
		return nil, err
	}
	if c.AllowAliEmbeddingV4 || c.AllowAliRerank {
		if c.AliEmbeddingResultKeys != nil || c.AliEmbeddingResultActiveKey != "" {
			if len(c.AliEmbeddingResultKey) != 0 {
				return nil, ErrInvalid
			}
			a.knowledgeResults, err = newKnowledgeResultVaultWithKeys(db, c.AliEmbeddingResultKeys, c.AliEmbeddingResultActiveKey, time.Hour)
		} else {
			a.knowledgeResults, err = newKnowledgeResultVault(db, c.AliEmbeddingResultKey, time.Hour)
		}
		if err != nil {
			return nil, err
		}
	}
	if err = a.initNativeHistory(context.Background()); err != nil {
		return nil, err
	}
	return a, nil
}
func (a *Adapter) Enabled() bool            { return a != nil && a.config.Enabled }
func (a *Adapter) Principal() (int, string) { return a.config.PrincipalID, a.config.Group }
func (a *Adapter) BodyLimit() int64         { return a.config.MaxBodyBytes }
func (a *Adapter) Admit(ctx context.Context, credential, task, id string, body []byte) (*Session, error) {
	// A configured chat metered tariff opts into metered_v2; otherwise the
	// legacy exclusive_v1 contract remains byte-compatible.
	if len(a.config.OperationRateIDs["chat"]) > 0 {
		return a.AdmitOperation(ctx, credential, task, id, "chat", "application/json", body)
	}
	if !a.Enabled() || !stableID.MatchString(id) || !stableID.MatchString(task) || !IsModelCredential(credential) || int64(len(body)) > a.config.MaxBodyBytes || len(body) > a.config.MaxInputTokens/2 {
		return nil, ErrInvalid
	}
	req, parseErr := strictChatRequest(body)
	if parseErr != nil {
		return nil, ErrInvalid
	}
	model, _ := req["model"].(string)
	if model == "" || strings.Contains(strings.ToLower(model), "search") || strings.Contains(strings.ToLower(model), "audio") {
		return nil, ErrInvalid
	}
	max, ok := req["max_tokens"].(json.Number)
	if !ok {
		max, ok = req["max_completion_tokens"].(json.Number)
	}
	maximum, err := confirmedCount(max)
	if !ok || err != nil || maximum < 1 || maximum > a.config.MaxOutputTokens {
		return nil, ErrInvalid
	}
	for _, key := range []string{"max_tokens", "max_completion_tokens"} {
		if value, exists := req[key]; exists {
			n, e := confirmedCount(value)
			if e != nil || n < 1 || n > maximum {
				return nil, ErrInvalid
			}
		}
	}
	if value, exists := req["n"]; exists {
		n, e := confirmedCount(value)
		if e != nil || n != 1 {
			return nil, ErrInvalid
		}
	}
	// Reject multimodal message content and built-in tools. Function tools are
	// ordinary text tokens and remain available for OpenCode agent loops.
	if messages, ok := req["messages"].([]any); ok {
		for _, v := range messages {
			m, ok := v.(map[string]any)
			if !ok {
				return nil, ErrInvalid
			}
			if content, exists := m["content"]; exists && content != nil {
				switch content := content.(type) {
				case string:
				case []any:
					for _, part := range content {
						text, ok := part.(map[string]any)
						if !ok || text["type"] != "text" {
							return nil, ErrInvalid
						}
						if _, ok := text["text"].(string); !ok {
							return nil, ErrInvalid
						}
					}
				default:
					return nil, ErrInvalid
				}
			}
		}
	}
	if ts, ok := req["tools"].([]any); ok {
		for _, v := range ts {
			tool, ok := v.(map[string]any)
			if !ok || tool["type"] != "function" {
				return nil, ErrInvalid
			}
		}
	}
	var grant Claims
	if err := a.client.do(ctx, http.MethodPost, "/internal/v1/model-credentials/verify", "", map[string]string{"token": credential, "model": model, "instance_id": a.config.InstanceID, "wire_operation": "chat.completions.create"}, &grant); err != nil {
		return nil, err
	}
	if a.validateModelCredential(credential, task, model, grant) != nil || !allowsWire(grant, model, "chat.completions.create") {
		return nil, ErrInvalid
	}
	asset, mode, err := normalizeFunding(grant.Asset, grant.FundingMode)
	if err != nil || grant.BillingProfileVersion < 0 {
		return nil, ErrInvalid
	}
	digestBody := append(append([]byte(task), 0), body...)
	// Keep the original CREDIT/version-0 digest byte-for-byte compatible with
	// in-flight calls. New funding snapshots cannot reinterpret that digest.
	if asset != "CREDIT/6" || mode != "credits" || grant.BillingProfileVersion != 0 {
		prefix := fmt.Sprintf("unihub-funding-v1\x00%s\x00%s\x00%d\x00", asset, mode, grant.BillingProfileVersion)
		digestBody = append([]byte(prefix), digestBody...)
	}
	legacySum := sha256.Sum256(digestBody)
	legacyDigest := hex.EncodeToString(legacySum[:])
	if grant.Operations != nil {
		digestBody, _ = common.Marshal(struct{ Version, Wire, Task, Actor, Account, Origin, Body string }{"platform-wire-v3", "chat.completions.create", task, grant.ActorID, grant.AccountID, grant.OriginProductID, string(digestBody)})
	}
	sum := sha256.Sum256(digestBody)
	digest := hex.EncodeToString(sum[:])
	row := Call{CallID: id, ActorID: grant.ActorID, AccountID: grant.AccountID, OriginProductID: grant.OriginProductID, CredentialID: grant.ID, TaskID: task, Model: model, Digest: digest, Asset: asset, FundingMode: mode, FundingProductID: "llmrouter", BillingProfileVersion: grant.BillingProfileVersion, Status: "RESERVING"}
	row.InstanceID = a.config.InstanceID
	if grant.Operations != nil {
		row.WireOperation = "chat.completions.create"
		row.DigestVersion = "platform-wire-v3"
	}
	var old Call
	if err := a.db.WithContext(ctx).First(&old, "call_id = ?", id).Error; err == nil {
		return nil, compareWireCall(old, row, legacyDigest)
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	var rates []Rate
	if err := a.client.do(ctx, http.MethodGet, "/internal/v1/rates", "", nil, &rates); err != nil {
		return nil, err
	}
	var chosen *Rate
	candidates := 0
	for i := range rates {
		r := &rates[i]
		if r.Model != model || r.Status != "published" || (r.Metered != nil && r.Metered.Operation != "chat") {
			continue
		}
		rateAsset, err := normalizeAsset(r.Asset)
		if err != nil {
			return nil, err
		}
		if rateAsset == asset {
			candidates++
			if candidates > 1 {
				return nil, ErrInvalid
			}
		}
		pin := ""
		if asset == "CREDIT/6" {
			pin = a.config.RateIDs[model]
		}
		if assetPin := a.config.AssetRateIDs[asset][model]; assetPin != "" {
			pin = assetPin
		}
		if rateAsset == asset && (pin == "" || r.ID == pin) {
			if chosen != nil {
				return nil, ErrInvalid
			}
			chosen = r
		}
	}
	if chosen == nil {
		return nil, ErrInvalid
	}
	amount, err := reserveBound(*chosen, a.config.MaxInputTokens, maximum)
	if err != nil {
		return nil, err
	}
	row.RateID = chosen.ID
	row.Amount = amount
	created := a.db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&row)
	if created.Error != nil {
		return nil, created.Error
	}
	if created.RowsAffected != 1 {
		if err := a.db.First(&old, "call_id = ?", id).Error; err != nil {
			return nil, err
		}
		return nil, compareWireCall(old, row, legacyDigest)
	}
	var remote RemoteCall
	err = a.client.do(ctx, http.MethodPost, "/internal/v1/calls/reserve", id, map[string]string{"credential": credential, "task_id": task, "call_id": id, "request_digest": digest, "rate_id": chosen.ID, "amount": amount, "instance_id": a.config.InstanceID, "wire_operation": row.WireOperation}, &remote)
	if err != nil {
		status := "RESERVE_UNKNOWN"
		var re *remoteError
		if errors.As(err, &re) && re.status >= 400 && re.status < 500 && re.status != 409 {
			status = "REJECTED"
		}
		_ = a.db.Model(&Call{}).Where("call_id = ?", id).Update("status", status).Error
		return nil, err
	}
	if !remote.matches(row) || remote.Status != "RESERVED" || (strings.HasPrefix(credential, "uh_key_") && (remote.InstanceID != a.config.InstanceID || remote.CredentialID != grant.ID)) {
		_ = a.db.Model(&Call{}).Where("call_id = ?", id).Update("status", "RESERVE_UNKNOWN").Error
		return nil, ErrPending
	}
	reserved := a.db.WithContext(ctx).Model(&Call{}).Where("call_id = ? AND status = ?", id, "RESERVING").Update("status", "RESERVED")
	if reserved.Error != nil {
		return nil, reserved.Error
	}
	if reserved.RowsAffected != 1 {
		return nil, ErrPending
	}
	row.Status = "RESERVED"
	return &Session{adapter: a, call: row, safeFailure: true, requestTariff: chosen.Metered != nil}, nil
}
func compareCall(old, row Call) error {
	oldInstance := old.InstanceID
	if oldInstance == "" {
		oldInstance = "default"
	}
	if oldInstance != row.InstanceID {
		return ErrConflict
	}
	oldAsset, oldMode, err := normalizeFunding(old.Asset, old.FundingMode)
	if err != nil || oldAsset != row.Asset || oldMode != row.FundingMode || old.BillingProfileVersion != row.BillingProfileVersion || (old.FundingProductID != "" && old.FundingProductID != row.FundingProductID) || old.Operation != row.Operation || old.WireOperation != row.WireOperation || old.WirePathModel != row.WirePathModel || old.WireMode != row.WireMode || old.DigestVersion != row.DigestVersion || old.CallID != row.CallID || old.ActorID != row.ActorID || old.AccountID != row.AccountID || old.OriginProductID != row.OriginProductID || old.TaskID != row.TaskID || old.Model != row.Model || old.Digest != row.Digest {
		return ErrConflict
	}
	return ErrDuplicate
}
func (s *Session) Dispatch(ctx context.Context, channel int) error {
	if s.PinnedKnowledge() {
		if channel != s.knowledgeAdmission.binding.ChannelID {
			return ErrConflict
		}
		return nil
	}
	if IsImageWire(s.WireOperation().ID) {
		p, e := s.ImagePolicy()
		if e != nil || p.ChannelID != channel {
			return ErrInvalid
		}
	}
	if s.WireOperation().ID == "ocr.jobs.create" || s.WireOperation().ID == "dashscope.ocr.recognitions.create" {
		p, e := s.OCRPolicy()
		if e != nil || p.ChannelID != channel {
			return ErrInvalid
		}
	}
	if IsAudioWire(s.WireOperation().ID) {
		p, e := s.AudioPolicy()
		if e != nil || p.ChannelID != channel {
			return ErrInvalid
		}
	}
	if s.WireOperation().ID == "videos.create" {
		if _, ok := s.adapter.config.VideoPolicies["videos.create"][s.call.Model]; ok {
			var frozen frozenVideo
			if common.UnmarshalUnique([]byte(s.call.MediaSnapshot), &frozen) != nil || frozen.Policy.ChannelID != channel {
				return ErrInvalid
			}
		}
	}
	if s.WireOperation().ID == "embeddings.create" || s.WireOperation().ID == "rerank.create" || s.WireOperation().ID == "multimodal.embeddings.create" {
		var f frozenKnowledge
		if common.UnmarshalUnique([]byte(s.call.MediaSnapshot), &f) != nil || f.Policy.ChannelID != channel {
			return ErrInvalid
		}
	}

	if s.WireOperation().ID == "responses.compact" {
		var frozen frozenMediaReservation
		if common.Unmarshal([]byte(s.call.MediaSnapshot), &frozen) != nil || frozen.CompactPolicy == nil || frozen.CompactPolicy.ChannelID != channel {
			return ErrInvalid
		}
	}
	if s.err != nil || s.uncertain || s.confirmed != nil {
		return ErrPending
	}
	attempt := Attempt{UpstreamIdentity: s.nativeSend, CallID: s.call.CallID, ChannelID: channel, Status: "DISPATCHED"}
	err := s.adapter.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		claim := tx.Model(&Call{}).Where("call_id = ? AND status = ?", s.call.CallID, "RESERVED").Update("status", "UNKNOWN")
		if claim.Error != nil {
			return claim.Error
		}
		if claim.RowsAffected != 1 {
			return ErrPending
		}
		return tx.Create(&attempt).Error
	})
	if err != nil {
		s.err = err
		return err
	}
	s.attempt = attempt
	s.safeFailure = false
	s.uncertain = true
	return nil
}
func (s *Session) Rejected() {
	if s.PinnedKnowledge() {
		return
	}
	if s.attempt.ID == 0 {
		return
	}
	if err := s.adapter.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&Attempt{}).Where("id = ?", s.attempt.ID).Update("status", "REJECTED").Error; err != nil {
			return err
		}
		result := tx.Model(&Call{}).Where("call_id = ? AND status = ?", s.call.CallID, "UNKNOWN").Update("status", "RESERVED")
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrPending
		}
		return nil
	}); err != nil {
		s.err = err
		return
	}
	s.safeFailure = true
	s.uncertain = false
}
func (s *Session) CanRetry() bool { return s.err == nil && !s.uncertain && s.confirmed == nil }
func (s *Session) invalidateUsage() {
	s.confirmed = nil
	if s.attempt.ID != 0 {
		if err := s.adapter.db.Model(&Attempt{}).Where("id = ?", s.attempt.ID).Update("status", "UNKNOWN").Error; err != nil {
			s.err = err
		}
	}
}
func (s *Session) observe(raw *dto.Usage) {
	if s.call.Operation != "" {
		return
	}
	u, err := NormalizeUsage(raw)
	if err != nil {
		s.invalidateUsage()
		return
	}
	s.confirmed = &u
	if s.attempt.ID != 0 {
		if err := s.adapter.db.Model(&Attempt{}).Where("id = ?", s.attempt.ID).Update("status", "USAGE_CONFIRMED").Error; err != nil {
			s.err = err
		}
	}
}
func (s *Session) Finish(ctx context.Context) error {
	if s.call.Operation != "" {
		return s.finishMetered(ctx)
	}
	if s.err != nil {
		return s.err
	}
	status := "UNKNOWN"
	var intent any
	if s.confirmed != nil {
		status = "SETTLE_PENDING"
		intent = struct {
			Usage        Usage  `json:"usage"`
			ProviderCost string `json:"provider_cost"`
		}{*s.confirmed, ""}
	} else if s.safeFailure {
		status = "RELEASE_PENDING"
		intent = map[string]string{"reason": "definitive_no_output_failure"}
	}
	var b []byte
	var err error
	if intent != nil {
		b, err = common.Marshal(intent)
		if err != nil {
			return err
		}
	}
	saved := s.adapter.db.WithContext(ctx).Model(&Call{}).Where("call_id = ? AND status IN ?", s.call.CallID, []string{"RESERVED", "UNKNOWN"}).Updates(map[string]any{"status": status, "intent": string(b)})
	if saved.Error != nil {
		return saved.Error
	}
	if saved.RowsAffected != 1 {
		return ErrPending
	}
	if status == "UNKNOWN" {
		return nil
	}
	s.call.Status = status
	s.call.Intent = string(b)
	_ = s.adapter.deliver(ctx, s.call)
	return nil
}
func (a *Adapter) deliver(ctx context.Context, row Call) error {
	var current Call
	if err := a.db.WithContext(ctx).First(&current, "call_id = ?", row.CallID).Error; err != nil {
		return err
	}
	if current.EvidenceConflict {
		return ErrConflict
	}
	if current.Status != row.Status || current.Intent != row.Intent {
		return ErrPending
	}
	operation, terminal := "settle", "SETTLED"
	if row.Status == "RELEASE_PENDING" {
		operation, terminal = "release", "RELEASED"
	}
	var body any
	if common.Unmarshal([]byte(row.Intent), &body) != nil {
		return ErrInvalid
	}
	var remote RemoteCall
	if err := a.client.do(ctx, http.MethodPost, "/internal/v1/calls/"+row.CallID+"/"+operation, row.CallID+":"+operation, body, &remote); err != nil {
		return err
	}
	if !remote.matches(row) || remote.Status != terminal {
		return ErrPending
	}
	saved := a.db.WithContext(ctx).Model(&Call{}).Where("call_id = ? AND status = ? AND intent = ? AND evidence_conflict = ?", row.CallID, row.Status, row.Intent, false).Updates(map[string]any{"status": terminal, "charged": remote.Charged})
	if saved.Error != nil {
		return saved.Error
	}
	if saved.RowsAffected != 1 {
		var latest Call
		if err := a.db.WithContext(ctx).First(&latest, "call_id = ?", row.CallID).Error; err != nil {
			return err
		}
		if latest.EvidenceConflict {
			// The remote command may already have succeeded while contradictory
			// evidence was recorded. Preserve its terminal accounting state, but
			// keep the conflict sticky and never return a result as valid.
			if latest.Status == row.Status && latest.Intent == row.Intent {
				if err := a.db.WithContext(ctx).Model(&Call{}).Where("call_id = ? AND status = ? AND intent = ? AND evidence_conflict = ?", row.CallID, row.Status, row.Intent, true).Updates(map[string]any{"status": terminal, "charged": remote.Charged}).Error; err != nil {
					return err
				}
			}
			return ErrConflict
		}
		if latest.Status == terminal && latest.Intent == row.Intent {
			return nil
		}
		return ErrPending
	}
	return nil
}

// Recover only resends durable final commands. UNKNOWN calls are never replayed
// or refunded. A lost reserve response is known not to have dispatched upstream.
func (a *Adapter) Recover(ctx context.Context) error {
	if !a.Enabled() {
		return nil
	}
	if a.history != nil {
		if _, err := a.history.Cleanup(ctx, 500); err != nil {
			common.SysError("native history bounded cleanup failed")
		}
	}
	// Expired undispatched calls are fenced before remote recovery. Dispatch
	// atomically requires RESERVED, so a paused original handler cannot send
	// upstream after its hold was recovered by another process.
	cutoff := time.Now().Add(-time.Minute)
	if err := a.db.WithContext(ctx).Model(&Call{}).Where("status IN ? AND updated_at < ?", []string{"RESERVING", "RESERVED"}, cutoff).Update("status", "RESERVE_UNKNOWN").Error; err != nil {
		return err
	}
	var rows []Call
	if err := a.db.WithContext(ctx).Where("status IN ?", []string{"SETTLE_PENDING", "RELEASE_PENDING", "RESERVE_UNKNOWN"}).Order("updated_at ASC").Limit(100).Find(&rows).Error; err != nil {
		return err
	}
	for _, row := range rows {
		if err := a.db.WithContext(ctx).Model(&Call{}).Where("call_id = ?", row.CallID).UpdateColumn("updated_at", time.Now()).Error; err != nil {
			return err
		}
		if row.Status == "RESERVE_UNKNOWN" {
			var remote RemoteCall
			if a.client.do(ctx, http.MethodGet, "/internal/v1/calls/"+row.CallID, "", nil, &remote) != nil || !remote.matches(row) || remote.Status != "RESERVED" {
				continue
			}
			b, _ := common.Marshal(map[string]string{"reason": "reservation_response_lost_no_dispatch"})
			if err := a.db.WithContext(ctx).Model(&Call{}).Where("call_id = ? AND status = ?", row.CallID, "RESERVE_UNKNOWN").Updates(map[string]any{"status": "RELEASE_PENDING", "intent": string(b)}).Error; err != nil {
				return err
			}
			row.Status = "RELEASE_PENDING"
			row.Intent = string(b)
		}
		_ = a.deliver(ctx, row)
	}
	return ctx.Err()
}
func (a *Adapter) Start(ctx context.Context) func() {
	worker, cancel := context.WithCancel(ctx)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			_ = a.Recover(worker)
			select {
			case <-worker.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	return func() { cancel(); wg.Wait() }
}
func (a *Adapter) String() string { return fmt.Sprintf("platformbilling(enabled=%t)", a.Enabled()) }

// Receipt authenticates a newly issued capability against the original payer
// and task. It intentionally contains no prompt, credential, or provider body.
type Receipt struct {
	CallID  string `json:"call_id"`
	Status  string `json:"status"`
	Charged string `json:"charged,omitempty"`
	Asset   string `json:"asset"`
}

func (a *Adapter) Receipt(ctx context.Context, credential, task, id string) (Receipt, error) {
	if !a.Enabled() || !stableID.MatchString(id) || !stableID.MatchString(task) || !IsModelCredential(credential) {
		return Receipt{}, ErrInvalid
	}
	var row Call
	if err := a.db.WithContext(ctx).First(&row, "call_id = ?", id).Error; err != nil {
		return Receipt{}, ErrInvalid
	}
	var grant Claims
	if err := a.client.do(ctx, http.MethodPost, "/internal/v1/model-credentials/verify", "", map[string]string{"token": credential, "model": row.Model, "wire_operation": row.WireOperation, "purpose": "receipt", "instance_id": a.config.InstanceID}, &grant); err != nil {
		return Receipt{}, err
	}
	instance := row.InstanceID
	if instance == "" {
		instance = "default"
	}
	if a.validateModelCredential(credential, task, row.Model, grant) != nil || instance != a.config.InstanceID || row.CallID != id || grant.ActorID != row.ActorID || grant.AccountID != row.AccountID || grant.OriginProductID != row.OriginProductID || row.TaskID != task {
		return Receipt{}, ErrInvalid
	}
	if row.WireOperation != "" && !allowsWire(grant, row.Model, row.WireOperation) {
		return Receipt{}, ErrInvalid
	}
	if strings.HasPrefix(credential, "uh_key_") && grant.ID != row.CredentialID {
		return Receipt{}, ErrInvalid
	}
	asset, _, err := normalizeFunding(row.Asset, row.FundingMode)
	if err != nil {
		return Receipt{}, ErrInvalid
	}
	return Receipt{CallID: row.CallID, Status: row.Status, Charged: row.Charged, Asset: asset}, nil
}

// ChatAdmissionPolicy describes the billing branch used by Admit and its
// configured per-asset rate pins. It never includes credentials or prices.
func (a *Adapter) ChatAdmissionPolicy() (string, map[string]map[string]string) {
	pins := map[string]map[string]string{}
	mode := "legacy"
	if len(a.config.OperationRateIDs["chat"]) > 0 {
		mode = "metered_v2"
		for asset, models := range a.config.OperationRateIDs["chat"] {
			pins[asset] = map[string]string{}
			for model, id := range models {
				pins[asset][model] = id
			}
		}
		return mode, pins
	}
	pins["CREDIT/6"] = map[string]string{}
	for model, id := range a.config.RateIDs {
		pins["CREDIT/6"][model] = id
	}
	for asset, models := range a.config.AssetRateIDs {
		if pins[asset] == nil {
			pins[asset] = map[string]string{}
		}
		for model, id := range models {
			if id != "" {
				pins[asset][model] = id
			}
		}
	}
	return mode, pins
}
