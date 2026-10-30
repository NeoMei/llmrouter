package knowledgeoperator

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sort"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/internal/platformbilling"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/router"
	"github.com/QuantumNous/new-api/setting/config"
	"github.com/QuantumNous/new-api/setting/model_setting"
	"gorm.io/gorm"
)

var (
	ErrRejected      = errors.New("knowledge activation approval rejected")
	ErrUnknownCommit = errors.New("knowledge activation commit outcome unknown; do not resend")
)

type Result struct {
	Status       string                           `json:"status"`
	Source       string                           `json:"source_revision"`
	InstanceID   string                           `json:"instance_id"`
	PrincipalID  int                              `json:"principal_id"`
	Group        string                           `json:"group"`
	ConfigSHA256 string                           `json:"config_sha256"`
	PrestateSHA  string                           `json:"prestate_sha256"`
	Bindings     []platformbilling.ManagedBinding `json:"bindings"`
}

func Run(ctx context.Context, db *gorm.DB, cfg platformbilling.Config, rawApproval []byte, approvalSHA, sourceRevision string, apply bool) (Result, error) {
	var result Result
	if db == nil || !revisionRE.MatchString(sourceRevision) {
		return result, ErrRejected
	}
	approval, err := ParseApproval(rawApproval, approvalSHA, apply)
	if err != nil || approval.SourceRevision != sourceRevision || approval.InstanceID != cfg.InstanceID || approval.PrincipalID != cfg.PrincipalID || approval.Group != cfg.Group {
		return result, ErrRejected
	}
	if cfg.NativeHistory != nil {
		return result, ErrRejected
	}
	configDigest, err := ConfigFingerprint(cfg)
	if err != nil {
		return result, ErrRejected
	}
	if approval.ConfigSHA256 != configDigest {
		return result, ErrRejected
	}
	if err := requireOperatorTables(db); err != nil {
		return result, ErrRejected
	}

	var bindings []platformbilling.ManagedBinding
	outer := db.WithContext(ctx).Begin()
	if outer.Error != nil {
		return result, ErrRejected
	}
	committed := false
	commitAttempted := false
	defer func() {
		if !committed && !commitAttempted {
			_ = outer.Rollback().Error
		}
	}()
	if err := loadPassThroughOption(outer); err != nil {
		return result, ErrRejected
	}
	if err := lockApprovedRows(outer); err != nil {
		return result, ErrRejected
	}
	prestate, err := prestateDigest(outer)
	if err != nil || approval.PrestateSHA256 != prestate {
		return result, ErrRejected
	}
	if err := validateApprovedChannelSettings(outer); err != nil {
		return result, ErrRejected
	}
	adapter, err := platformbilling.NewKnowledgeBindingOperatorAdapter(outer, cfg)
	if err != nil {
		return result, ErrRejected
	}
	router.InstallKnowledgeBindings(adapter)
	for _, pair := range [][2]string{{"unihub-text-embedding-v4", "embeddings.create"}, {"qwen3-rerank", "rerank.create"}} {
		activated, activationErr := adapter.ActivateManagedKnowledge(ctx, pair[0], pair[1], []string{"llmrouter", "superwagie"})
		if activationErr != nil || len(activated) != 2 {
			return result, ErrRejected
		}
		bindings = append(bindings, activated...)
	}
	status := "dry-run-rolled-back"
	if apply {
		commitAttempted = true
		if err := outer.Commit().Error; err != nil {
			return result, ErrUnknownCommit
		}
		committed = true
		status = "applied"
	} else {
		if err := outer.Rollback().Error; err != nil {
			return result, ErrRejected
		}
		committed = true
	}
	result = Result{Status: status, Source: sourceRevision, InstanceID: cfg.InstanceID, PrincipalID: cfg.PrincipalID, Group: cfg.Group, ConfigSHA256: configDigest, PrestateSHA: prestate, Bindings: bindings}
	return result, nil
}

func requireOperatorTables(db *gorm.DB) error {
	models := []any{&model.KnowledgeActiveBinding{}, &model.KnowledgeRoutePin{}, &model.ClientModelRoute{}, &model.ClientModelTarget{}, &model.Channel{}, &model.Ability{}, &model.Option{}}
	for _, m := range models {
		if !db.Migrator().HasTable(m) {
			return errors.New("required schema missing")
		}
	}
	return nil
}

func loadPassThroughOption(tx *gorm.DB) error {
	var option model.Option
	if err := passThroughOptionQuery(model.KnowledgeLockedRead(tx)).First(&option).Error; err != nil {
		return err
	}
	raw := strings.TrimSpace(option.Value)
	if raw != "true" && raw != "false" {
		return ErrRejected
	}
	settings := config.GlobalConfig.Get("global")
	if settings == nil {
		return ErrRejected
	}
	if err := config.UpdateConfigFromMap(settings, map[string]string{"pass_through_request_enabled": raw}); err != nil {
		return err
	}
	if model_setting.GetGlobalSettings().PassThroughRequestEnabled != (raw == "true") {
		return ErrRejected
	}
	return nil
}

type prestate struct {
	PassThroughOption string
	Routes            []routeState
	Targets           []model.ClientModelTarget
	Channels          []channelState
	Abilities         []model.Ability
	Active            []model.KnowledgeActiveBinding
	Pins              []model.KnowledgeRoutePin
}

type routeState struct {
	ID                                  int
	PublicID, Client, Aliases, Metadata string
	Enabled                             bool
	KnowledgeGeneration                 string
}

type channelState struct {
	ID, Type, Status                     int
	Key                                  string
	Origin                               string
	Models, Group                        string
	ModelMapping, Setting, OtherSettings string
	ParamOverride, HeaderOverride        *string
	ChannelInfo                          model.ChannelInfo
}

func lockApprovedRows(tx *gorm.DB) error {
	if err := model.LockKnowledgeParents(tx, 0); err != nil {
		return err
	}
	var routes []model.ClientModelRoute
	if err := model.KnowledgeLockedRead(tx).Where("public_id IN ?", []string{"unihub-text-embedding-v4", "qwen3-rerank"}).Order("id").Find(&routes).Error; err != nil {
		return err
	}
	if len(routes) != 2 {
		return ErrRejected
	}
	for _, route := range routes {
		if err := model.LockKnowledgeParents(tx, route.Id); err != nil {
			return err
		}
	}
	return nil
}

func prestateDigest(tx *gorm.DB) (string, error) {
	var state prestate
	if err := passThroughOptionQuery(model.KnowledgeLockedRead(tx)).Model(&model.Option{}).Pluck("value", &state.PassThroughOption).Error; err != nil {
		return "", err
	}
	var routes []model.ClientModelRoute
	if err := tx.Where("enabled = ?", true).Order("id").Limit(1025).Find(&routes).Error; err != nil {
		return "", err
	}
	if len(routes) > 1024 {
		return "", ErrRejected
	}
	for _, row := range routes {
		state.Routes = append(state.Routes, routeState{ID: row.Id, PublicID: row.PublicId, Client: row.Client, Aliases: row.Aliases, Metadata: row.Metadata, Enabled: row.Enabled, KnowledgeGeneration: row.KnowledgeGeneration})
	}
	var pairRoutes []model.ClientModelRoute
	if err := tx.Where("public_id IN ?", []string{"unihub-text-embedding-v4", "qwen3-rerank"}).Order("id").Find(&pairRoutes).Error; err != nil {
		return "", err
	}
	if len(pairRoutes) != 2 {
		return "", ErrRejected
	}
	ids := []int{pairRoutes[0].Id, pairRoutes[1].Id}
	if err := tx.Where("route_id IN ?", ids).Order("route_id, id").Limit(4097).Find(&state.Targets).Error; err != nil {
		return "", err
	}
	if len(state.Targets) > 4096 {
		return "", ErrRejected
	}
	var channels []model.Channel
	if err := tx.Where("id IN ?", []int{102, 103}).Order("id").Find(&channels).Error; err != nil {
		return "", err
	}
	for _, row := range channels {
		origin := row.GetBaseURL()
		if origin == "" {
			origin = "https://dashscope.aliyuncs.com"
		}
		state.Channels = append(state.Channels, channelState{ID: row.Id, Type: row.Type, Status: row.Status, Key: row.Key, Origin: origin, Models: row.Models, Group: row.Group, ModelMapping: value(row.ModelMapping), Setting: value(row.Setting), OtherSettings: row.OtherSettings, ParamOverride: row.ParamOverride, HeaderOverride: row.HeaderOverride, ChannelInfo: row.ChannelInfo})
	}
	if err := tx.Where("channel_id IN ? AND enabled = ?", []int{102, 103}, true).Order("channel_id, model").Limit(1025).Find(&state.Abilities).Error; err != nil {
		return "", err
	}
	sort.Slice(state.Abilities, func(i, j int) bool {
		if state.Abilities[i].ChannelId != state.Abilities[j].ChannelId {
			return state.Abilities[i].ChannelId < state.Abilities[j].ChannelId
		}
		if state.Abilities[i].Model != state.Abilities[j].Model {
			return state.Abilities[i].Model < state.Abilities[j].Model
		}
		return state.Abilities[i].Group < state.Abilities[j].Group
	})
	if len(state.Abilities) > 1024 {
		return "", ErrRejected
	}
	if err := tx.Where("model IN ?", []string{"unihub-text-embedding-v4", "qwen3-rerank"}).Order("instance_id, origin_product_id, model, wire_operation").Limit(8193).Find(&state.Active).Error; err != nil {
		return "", err
	}
	if len(state.Active) > 8192 {
		return "", ErrRejected
	}
	if err := tx.Where("model IN ?", []string{"unihub-text-embedding-v4", "qwen3-rerank"}).Order("pin").Limit(8193).Find(&state.Pins).Error; err != nil {
		return "", err
	}
	if len(state.Pins) > 8192 {
		return "", ErrRejected
	}
	raw, err := common.Marshal(state)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func validateApprovedChannelSettings(tx *gorm.DB) error {
	var channels []model.Channel
	if err := model.KnowledgeLockedRead(tx).Where("id IN ?", []int{102, 103}).Order("id").Find(&channels).Error; err != nil || len(channels) != 2 {
		return ErrRejected
	}
	for _, channel := range channels {
		var settings dto.ChannelSettings
		if channel.Setting != nil && *channel.Setting != "" && common.UnmarshalUnique([]byte(*channel.Setting), &settings) != nil {
			return ErrRejected
		}
	}
	return nil
}

func passThroughOptionQuery(tx *gorm.DB) *gorm.DB {
	return tx.Where(map[string]any{"key": "global.pass_through_request_enabled"})
}

func value(pointer *string) string {
	if pointer == nil {
		return ""
	}
	return *pointer
}

// ConfigFingerprint hashes the effective operator configuration while replacing
// secret values with one-way fingerprints. No secret value enters an approval
// file or the operator's output.
func ConfigFingerprint(cfg platformbilling.Config) (string, error) {
	copy := cfg
	copy.ServiceToken = ""
	copy.AliEmbeddingResultKey = nil
	copy.AliEmbeddingResultKeys = nil
	secrets := map[string]string{}
	if cfg.ServiceToken != "" {
		sum := sha256.Sum256([]byte(cfg.ServiceToken))
		secrets["service_token_sha256"] = hex.EncodeToString(sum[:])
	}
	if len(cfg.AliEmbeddingResultKey) > 0 {
		sum := sha256.Sum256(cfg.AliEmbeddingResultKey)
		secrets["legacy_result_key_sha256"] = hex.EncodeToString(sum[:])
	}
	for id, key := range cfg.AliEmbeddingResultKeys {
		sum := sha256.Sum256(key)
		secrets["result_key_"+id] = hex.EncodeToString(sum[:])
	}
	raw, err := common.Marshal(struct {
		Config                    platformbilling.Config `json:"config"`
		EnableKnowledgeResultRead bool                   `json:"enable_knowledge_result_read"`
		Secrets                   map[string]string      `json:"secret_fingerprints"`
	}{copy, cfg.EnableKnowledgeResultRead, secrets})
	if err != nil {
		return "", ErrRejected
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}
