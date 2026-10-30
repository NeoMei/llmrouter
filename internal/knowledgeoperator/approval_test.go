package knowledgeoperator

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/internal/platformbilling"
	"github.com/QuantumNous/new-api/model"
	"github.com/glebarez/sqlite"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/stretchr/testify/require"
)

func TestApprovalRequiresExactRawSHAAndStrictSchema(t *testing.T) {
	base := `{"schema":1,"mode":"apply","source_revision":"3725f8190ebe026964c19e304313091002cad374","instance_id":"default","principal_id":1,"group":"platform","config_sha256":"` + string(make([]byte, 64)) + `","prestate_sha256":"` + string(make([]byte, 64)) + `"}`
	base = replaceNulls(base)
	sum := sha256.Sum256([]byte(base))
	rawSHA := hex.EncodeToString(sum[:])
	parsed, err := ParseApproval([]byte(base), rawSHA, true)
	require.NoError(t, err)
	require.Equal(t, "apply", parsed.Mode)
	_, err = ParseApproval([]byte(base), "", true)
	require.Error(t, err)
	_, err = ParseApproval([]byte(base), rawSHA, false)
	require.Error(t, err)

	unknown := base[:len(base)-1] + `,"products":["llmrouter"]}`
	unknownSum := sha256.Sum256([]byte(unknown))
	_, err = ParseApproval([]byte(unknown), hex.EncodeToString(unknownSum[:]), true)
	require.Error(t, err)
}

func replaceNulls(raw string) string {
	return string(bytes.ReplaceAll([]byte(raw), []byte{0}, []byte("0")))
}

func TestOperatorDryRunApplyAndReplayFence(t *testing.T) {
	for _, mode := range []string{"dry-run", "apply", "failure"} {
		t.Run(mode, func(t *testing.T) {
			db, cfg := operatorFixture(t, false)
			var sawSecondPairFailure bool
			before := snapshotRows(t, db)
			raw, rawSHA := approvalFor(t, db, cfg, mode == "apply" || mode == "failure")
			if mode == "failure" {
				require.NoError(t, db.Callback().Create().Before("gorm:create").Register("test:fail_second_pair", func(tx *gorm.DB) {
					pin, ok := tx.Statement.Dest.(*model.KnowledgeRoutePin)
					if !ok || pin.Model != "qwen3-rerank" {
						return
					}
					var count int64
					query := tx.Session(&gorm.Session{NewDB: true}).Model(&model.KnowledgeRoutePin{}).Where("model = ?", "unihub-text-embedding-v4").Count(&count)
					sawSecondPairFailure = query.Error == nil && count == 2
					tx.AddError(errors.New("injected second-pair pin failure"))
				}))
			}
			result, err := Run(t.Context(), db, cfg, raw, rawSHA, testRevision, mode != "dry-run")
			if mode == "failure" {
				require.Error(t, err)
				require.True(t, sawSecondPairFailure, "first pair pins must exist in the outer transaction before pair two fails")
				require.Equal(t, before, snapshotRows(t, db))
				return
			}
			require.NoError(t, err)
			if mode == "dry-run" {
				require.Equal(t, "dry-run-rolled-back", result.Status)
				require.Len(t, result.Bindings, 4)
				require.Equal(t, before, snapshotRows(t, db))
				return
			}
			require.Equal(t, "applied", result.Status)
			require.Len(t, result.Bindings, 4)
			var pins int64
			require.NoError(t, db.Model(&model.KnowledgeRoutePin{}).Count(&pins).Error)
			require.EqualValues(t, 4, pins)
			var active int64
			require.NoError(t, db.Model(&model.KnowledgeActiveBinding{}).Count(&active).Error)
			require.EqualValues(t, 4, active)
			stale, staleSHA := raw, rawSHA
			_, err = Run(t.Context(), db, cfg, stale, staleSHA, testRevision, true)
			require.Error(t, err)
			require.NoError(t, db.Model(&model.KnowledgeRoutePin{}).Count(&pins).Error)
			require.EqualValues(t, 4, pins)
		})
	}
}

func TestOptionKeyPredicateQuotesMySQLReservedColumn(t *testing.T) {
	db, err := gorm.Open(mysql.New(mysql.Config{DSN: "user:pass@tcp(localhost:3306)/db", SkipInitializeWithVersion: true}), &gorm.Config{DryRun: true, DisableAutomaticPing: true})
	require.NoError(t, err)
	var option model.Option
	query := passThroughOptionQuery(model.KnowledgeLockedRead(db)).First(&option)
	require.NoError(t, query.Error)
	require.Contains(t, query.Statement.SQL.String(), "`key`")
	query = passThroughOptionQuery(model.KnowledgeLockedRead(db)).Model(&model.Option{}).Pluck("value", new(string))
	require.NoError(t, query.Error)
	require.Contains(t, query.Statement.SQL.String(), "`key`")
}

func TestMalformedChannelSettingsRejectBeforeResolver(t *testing.T) {
	db, cfg := operatorFixture(t, false)
	raw, _ := approvalFor(t, db, cfg, true)
	var approval Approval
	require.NoError(t, common.UnmarshalUnique(raw, &approval))
	malformed := `{"system_prompt":123456789}`
	require.NoError(t, db.Model(&model.Channel{}).Where("id = ?", 102).Update("setting", malformed).Error)
	tx := db.Begin()
	require.NoError(t, tx.Error)
	approval.PrestateSHA256, _ = prestateDigest(tx)
	require.NoError(t, tx.Rollback().Error)
	raw, err := common.Marshal(approval)
	require.NoError(t, err)
	sum := sha256.Sum256(raw)
	_, err = Run(t.Context(), db, cfg, raw, hex.EncodeToString(sum[:]), testRevision, true)
	require.Error(t, err)
	require.Equal(t, malformed, *func() *string { var ch model.Channel; require.NoError(t, db.First(&ch, 102).Error); return ch.Setting }())
}

const testRevision = "3725f8190ebe026964c19e304313091002cad374"

type vaultTestKey struct {
	ID       string `gorm:"primaryKey;size:32"`
	Verifier string `gorm:"size:64"`
}

func (vaultTestKey) TableName() string { return "platform_knowledge_result_keys" }

type vaultTestActive struct {
	ID    string `gorm:"primaryKey;size:32"`
	KeyID string `gorm:"size:32"`
}

func (vaultTestActive) TableName() string { return "platform_knowledge_result_active" }

type vaultTestResult struct {
	CallID         string `gorm:"primaryKey;size:128"`
	ExpiresAt      int64
	Ciphertext     []byte
	BindingVersion int
	AttemptID      uint
	ObservationTag string `gorm:"size:64"`
	KeyID          string `gorm:"size:32"`
}

func (vaultTestResult) TableName() string { return "platform_knowledge_results" }

func operatorFixture(t *testing.T, breakRerank bool) (*gorm.DB, platformbilling.Config) {
	t.Helper()
	oldMain, oldLog := common.MainDatabaseType(), common.LogDatabaseType()
	oldDB := model.DB
	common.SetDatabaseTypes(common.DatabaseTypeSQLite, common.DatabaseTypeSQLite)
	model.InitDatabaseColumns()
	t.Cleanup(func() { common.SetDatabaseTypes(oldMain, oldLog); model.DB = oldDB; model.InitDatabaseColumns() })
	db, err := gorm.Open(sqlite.Open("file:"+t.TempDir()+"/operator.db"), &gorm.Config{Logger: logger.Discard})
	require.NoError(t, err)
	model.DB = db
	t.Cleanup(func() { sqlDB, _ := db.DB(); _ = sqlDB.Close() })
	require.NoError(t, db.AutoMigrate(&platformbilling.Call{}, &platformbilling.Attempt{}, &model.KnowledgeActiveBinding{}, &model.KnowledgeRoutePin{}, &model.ClientModelRoute{}, &model.ClientModelTarget{}, &model.Channel{}, &model.Ability{}, &model.Option{}, &vaultTestKey{}, &vaultTestActive{}, &vaultTestResult{}))
	require.NoError(t, db.Create(&model.Option{Key: "global.pass_through_request_enabled", Value: "false"}).Error)
	base := "https://dashscope.aliyuncs.com"
	upstreams := []string{"text-embedding-v4", "qwen3-rerank"}
	for i, name := range []string{"unihub-text-embedding-v4", "qwen3-rerank"} {
		channelID := 102 + i
		upstream := upstreams[i]
		require.NoError(t, db.Create(&model.Channel{Id: channelID, Type: 17, Key: "safe-test-key", Status: common.ChannelStatusEnabled, BaseURL: &base, Models: upstream, Group: "platform"}).Error)
		require.NoError(t, db.Create(&model.Ability{Group: "platform", Model: upstream, ChannelId: channelID, Enabled: true}).Error)
		wire := "embeddings.create"
		space := &dto.EmbeddingSpace{Provider: "aliyun", Model: upstream, Version: "v4", Dimensions: 1024, InputSemantics: "text"}
		version := "knowledge_ali_text_v1"
		origin := base
		maxItem, maxBatch := 4096, 10
		if i == 1 {
			wire, space, version, origin, maxItem, maxBatch = "rerank.create", nil, "knowledge_v1", "", 4000, 32
		}
		hash := sha256.Sum256([]byte("safe-test-key"))
		policy := platformbilling.KnowledgePolicy{ChannelKeySHA256: hex.EncodeToString(hash[:]), Version: version, ProviderReference: "https://fixture.invalid/policy", PublicModel: name, WireOperation: wire, ChannelID: channelID, ChannelType: 17, Origin: origin, UpstreamModel: upstream, HardInputTokens: 4096, MaxItemTokens: maxItem, MaxBatch: maxBatch, RateIDs: map[string]string{"CREDIT/6": "rate"}, Space: space}
		var metadata dto.ModelCapabilities
		metadata = dto.ModelCapabilities{Operations: []string{wire}, InputModalities: []string{"text"}, EmbeddingSpace: space}
		encoded, marshalErr := common.Marshal(metadata)
		require.NoError(t, marshalErr)
		route := model.ClientModelRoute{Id: channelID, PublicId: name, DisplayName: name, Client: "all", Enabled: true, Metadata: string(encoded)}
		require.NoError(t, db.Create(&route).Error)
		targetChannel := channelID
		if breakRerank && i == 1 {
			targetChannel = 102
		}
		target := model.ClientModelTarget{Id: channelID, RouteId: channelID, ChannelId: targetChannel, UpstreamModel: upstream, BillingModel: upstream, PreferredWires: `["openai_chat"]`, Enabled: true, Weight: 1}
		require.NoError(t, db.Create(&target).Error)
		_ = policy
	}
	policies := map[string]map[string]platformbilling.KnowledgePolicy{}
	for _, modelName := range []string{"unihub-text-embedding-v4", "qwen3-rerank"} {
		var route model.ClientModelRoute
		require.NoError(t, db.First(&route, "public_id = ?", modelName).Error)
		wire := "embeddings.create"
		channelID, upstream, version := 102, "text-embedding-v4", "knowledge_ali_text_v1"
		space := &dto.EmbeddingSpace{Provider: "aliyun", Model: upstream, Version: "v4", Dimensions: 1024, InputSemantics: "text"}
		origin, maxItem, maxBatch := base, 4096, 10
		if modelName == "qwen3-rerank" {
			wire, channelID, upstream, version, space, origin, maxItem, maxBatch = "rerank.create", 103, "qwen3-rerank", "knowledge_v1", nil, "", 4000, 32
		}
		hash := sha256.Sum256([]byte("safe-test-key"))
		policies[wire] = map[string]platformbilling.KnowledgePolicy{modelName: {ChannelKeySHA256: hex.EncodeToString(hash[:]), Version: version, ProviderReference: "https://fixture.invalid/policy", PublicModel: modelName, WireOperation: wire, ChannelID: channelID, ChannelType: 17, Origin: origin, UpstreamModel: upstream, HardInputTokens: 4096, MaxItemTokens: maxItem, MaxBatch: maxBatch, RateIDs: map[string]string{"CREDIT/6": "rate"}, Space: space}}
	}
	cfg := platformbilling.Config{Enabled: true, Origin: "https://hub.invalid", ServiceToken: "service-service-service-service-token", InstanceID: "default", PrincipalID: 1, Group: "platform", MaxInputTokens: 4096, MaxOutputTokens: 100, MaxBodyBytes: 65536, AllowAliEmbeddingV4: true, AllowAliRerank: true, AliEmbeddingResultKeys: map[string][]byte{"v1": bytes.Repeat([]byte{11}, 32)}, AliEmbeddingResultActiveKey: "v1", KnowledgePolicies: policies}
	return db, cfg
}

func approvalFor(t *testing.T, db *gorm.DB, cfg platformbilling.Config, apply bool) ([]byte, string) {
	t.Helper()
	tx := db.Begin()
	require.NoError(t, tx.Error)
	require.NoError(t, loadPassThroughOption(tx))
	prestate, err := prestateDigest(tx)
	require.NoError(t, err)
	require.NoError(t, tx.Rollback().Error)
	configSHA, err := ConfigFingerprint(cfg)
	require.NoError(t, err)
	mode := "dry-run"
	if apply {
		mode = "apply"
	}
	approval := Approval{Schema: 1, Mode: mode, SourceRevision: testRevision, InstanceID: cfg.InstanceID, PrincipalID: cfg.PrincipalID, Group: cfg.Group, ConfigSHA256: configSHA, PrestateSHA256: prestate}
	raw, err := common.Marshal(approval)
	require.NoError(t, err)
	sum := sha256.Sum256(raw)
	return raw, hex.EncodeToString(sum[:])
}

func snapshotRows(t *testing.T, db *gorm.DB) string {
	t.Helper()
	var active []model.KnowledgeActiveBinding
	var pins []model.KnowledgeRoutePin
	var calls, attempts, keys, vaultActive, results int64
	require.NoError(t, db.Find(&active).Error)
	require.NoError(t, db.Find(&pins).Error)
	require.NoError(t, db.Model(&platformbilling.Call{}).Count(&calls).Error)
	require.NoError(t, db.Model(&platformbilling.Attempt{}).Count(&attempts).Error)
	require.NoError(t, db.Model(&vaultTestKey{}).Count(&keys).Error)
	require.NoError(t, db.Model(&vaultTestActive{}).Count(&vaultActive).Error)
	require.NoError(t, db.Model(&vaultTestResult{}).Count(&results).Error)
	var routes []model.ClientModelRoute
	var targets []model.ClientModelTarget
	var channels []model.Channel
	var abilities []model.Ability
	var options []model.Option
	require.NoError(t, db.Order("id").Find(&routes).Error)
	require.NoError(t, db.Order("id").Find(&targets).Error)
	require.NoError(t, db.Order("id").Find(&channels).Error)
	require.NoError(t, db.Order("channel_id, model").Find(&abilities).Error)
	require.NoError(t, db.Order("key").Find(&options).Error)
	raw, err := common.Marshal(struct {
		Active                                      []model.KnowledgeActiveBinding
		Pins                                        []model.KnowledgeRoutePin
		Calls, Attempts, Keys, VaultActive, Results int64
		Routes                                      []model.ClientModelRoute
		Targets                                     []model.ClientModelTarget
		Channels                                    []model.Channel
		Abilities                                   []model.Ability
		Options                                     []model.Option
	}{active, pins, calls, attempts, keys, vaultActive, results, routes, targets, channels, abilities, options})
	require.NoError(t, err)
	return fmt.Sprintf("%x", sha256.Sum256(raw))
}

func TestActivationDoesNotCallUniHub(t *testing.T) {
	db, cfg := operatorFixture(t, false)
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests.Add(1) }))
	defer server.Close()
	cfg.Origin = server.URL
	raw, rawSHA := approvalFor(t, db, cfg, false)
	_, err := Run(t.Context(), db, cfg, raw, rawSHA, testRevision, false)
	require.NoError(t, err)
	require.Zero(t, requests.Load())
}

func TestOperatorRejectsSourceConfigAndPrestateDrift(t *testing.T) {
	for _, drift := range []string{"source", "config", "prestate"} {
		t.Run(drift, func(t *testing.T) {
			db, cfg := operatorFixture(t, false)
			raw, rawSHA := approvalFor(t, db, cfg, true)
			source := testRevision
			switch drift {
			case "source":
				source = "0000000000000000000000000000000000000000"
			case "config":
				cfg.Group = "changed"
			case "prestate":
				require.NoError(t, db.Model(&model.ClientModelRoute{}).Where("public_id = ?", "unihub-text-embedding-v4").Update("display_name", "changed").Error)
			}
			_, err := Run(t.Context(), db, cfg, raw, rawSHA, source, true)
			require.Error(t, err)
			var pins int64
			require.NoError(t, db.Model(&model.KnowledgeRoutePin{}).Count(&pins).Error)
			require.Zero(t, pins)
		})
	}
}
