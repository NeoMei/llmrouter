package platformbilling

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type fundingHub struct {
	mu                sync.Mutex
	grant             map[string]any
	rates             []map[string]any
	reserved          map[string]map[string]string
	reserveCount      int
	settleCount       int
	releaseCount      int
	failReserve       bool
	responseAsset     string
	responseRateAsset string
	responseMode      string
	responseFunder    string
	responseVersion   *int
}

func fundingFixture(t *testing.T, asset, mode string, version int) (*Adapter, *gorm.DB, *fundingHub) {
	t.Helper()
	hub := &fundingHub{grant: map[string]any{"id": "cred", "audience": "llmrouter", "actor_id": "actor", "account_id": "account", "origin_product_id": "product", "models": []string{"test"}, "operations": map[string][]string{"test": {"chat.completions.create"}}, "task_id": "task", "expires_at": time.Now().Add(time.Hour), "asset": asset, "funding_mode": mode, "billing_profile_version": version}, reserved: make(map[string]map[string]string)}
	for _, pair := range []struct{ id, asset string }{{"credit-rate", ""}, {"cash-rate", "CNY/6"}} {
		hub.rates = append(hub.rates, map[string]any{"id": pair.id, "asset": pair.asset, "model": "test", "status": "published", "semantics": "exclusive_v1", "input": Rational{"1", "1"}, "output": Rational{"2", "1"}, "cache_read": Rational{"1", "1"}, "cache_write": Rational{"1", "1"}, "reasoning": Rational{"2", "1"}})
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hub.mu.Lock()
		defer hub.mu.Unlock()
		assert.Equal(t, "Bearer service-service-service-service-token", r.Header.Get("Authorization"))
		var v any
		switch r.URL.Path {
		case "/internal/v1/model-credentials/verify":
			v = hub.grant
		case "/internal/v1/rates":
			v = hub.rates
		default:
			status, id := "RESERVED", ""
			if r.URL.Path == "/internal/v1/calls/reserve" {
				var request map[string]string
				if err := common.DecodeJson(r.Body, &request); !assert.NoError(t, err) {
					w.WriteHeader(400)
					return
				}
				id = request["call_id"]
				hub.reserved[id] = request
				hub.reserveCount++
				if hub.failReserve {
					w.WriteHeader(503)
					return
				}
			} else {
				parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/internal/v1/calls/"), "/")
				id = parts[0]
				if len(parts) == 2 {
					switch parts[1] {
					case "settle":
						hub.settleCount++
						status = "SETTLED"
					case "release":
						hub.releaseCount++
						status = "RELEASED"
					}
				}
			}
			request := hub.reserved[id]
			asset, mode, funder := "CREDIT/6", "credits", "llmrouter"
			if request["rate_id"] == "cash-rate" {
				asset, mode = "CNY/6", "prepaid_cash"
			}
			rateAsset := asset
			if hub.responseAsset != "" {
				asset = hub.responseAsset
			}
			if hub.responseRateAsset != "" {
				rateAsset = hub.responseRateAsset
			}
			if hub.responseMode != "" {
				mode = hub.responseMode
			}
			if hub.responseFunder != "" {
				funder = hub.responseFunder
			}
			remoteVersion := version
			if hub.responseVersion != nil {
				remoteVersion = *hub.responseVersion
			}
			v = map[string]any{"call_id": id, "status": status, "actor_id": "actor", "account_id": "account", "origin_product_id": "product", "request_digest": request["request_digest"], "wire_operation": request["wire_operation"], "rate": map[string]any{"id": request["rate_id"], "model": "test", "asset": rateAsset}, "charge_asset": asset, "funding_mode": mode, "funding_product_id": funder, "billing_profile_version": remoteVersion, "charged": "20"}
		}
		b, err := common.Marshal(v)
		assert.NoError(t, err)
		_, _ = w.Write(b)
	}))
	t.Cleanup(server.Close)
	db, err := gorm.Open(sqlite.Open("file:"+t.TempDir()+"/platform.db"), &gorm.Config{})
	require.NoError(t, err)
	a, err := New(db, Config{Enabled: true, Origin: server.URL, ServiceToken: "service-service-service-service-token", Group: "platform", MaxInputTokens: 1000, MaxOutputTokens: 100, MaxBodyBytes: 8192})
	require.NoError(t, err)
	return a, db, hub
}

// Selecting by model alone would make dual-asset rates ambiguous or charge the wrong wallet.
func TestAdmissionSelectsVerifiedFundingAsset(t *testing.T) {
	for _, tc := range []struct {
		name, asset, mode, rate string
		version                 int
	}{
		{"legacy", "", "", "credit-rate", 0},
		{"credits", "CREDIT/6", "credits", "credit-rate", 2},
		{"cash", "CNY/6", "prepaid_cash", "cash-rate", 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, db, hub := fundingFixture(t, tc.asset, tc.mode, tc.version)
			_, err := a.Admit(context.Background(), "uh_model_token", "task", "call", []byte(`{"model":"test","messages":[{"role":"user","content":"hi"}],"max_tokens":50}`))
			require.NoError(t, err)
			hub.mu.Lock()
			assert.Equal(t, tc.rate, hub.reserved["call"]["rate_id"])
			assert.Equal(t, "1100", hub.reserved["call"]["amount"])
			hub.mu.Unlock()
			var row map[string]any
			require.NoError(t, db.Table("platform_model_calls").Take(&row).Error)
			asset, mode := tc.asset, tc.mode
			if asset == "" {
				asset, mode = "CREDIT/6", "credits"
			}
			assert.Equal(t, asset, row["asset"])
			assert.Equal(t, mode, row["funding_mode"])
			assert.Equal(t, "llmrouter", row["funding_product_id"])
			assert.EqualValues(t, tc.version, row["billing_profile_version"])
		})
	}
}

func TestAdmissionRejectsMalformedVerifiedFunding(t *testing.T) {
	for _, tc := range []struct {
		asset, mode string
		version     int
	}{{"USD/6", "credits", 0}, {"CNY/6", "credits", 0}, {"CREDIT/6", "prepaid_cash", 0}, {"", "prepaid_cash", 0}, {"CREDIT/6", "other", 0}, {"CREDIT/6", "credits", -1}} {
		t.Run(tc.asset+"-"+tc.mode, func(t *testing.T) {
			a, _, hub := fundingFixture(t, tc.asset, tc.mode, tc.version)
			hub.mu.Lock()
			hub.rates = hub.rates[:1]
			hub.mu.Unlock()
			_, err := a.Admit(context.Background(), "uh_model_token", "task", "call", []byte(`{"model":"test","messages":[{"role":"user","content":"hi"}],"max_tokens":50}`))
			assert.ErrorIs(t, err, ErrInvalid)
			hub.mu.Lock()
			assert.Zero(t, hub.reserveCount)
			hub.mu.Unlock()
		})
	}
}

func TestLegacyRatePinOnlyAppliesToCredits(t *testing.T) {
	a, _, _ := fundingFixture(t, "CNY/6", "prepaid_cash", 1)
	a.config.RateIDs = map[string]string{"test": "credit-rate"}
	_, err := a.Admit(context.Background(), "uh_model_token", "task", "call", []byte(`{"model":"test","messages":[{"role":"user","content":"hi"}],"max_tokens":50}`))
	require.NoError(t, err)
}

func TestAssetRatePinCannotBypassCentralTupleAmbiguity(t *testing.T) {
	a, _, hub := fundingFixture(t, "CNY/6", "prepaid_cash", 1)
	hub.mu.Lock()
	other := make(map[string]any)
	for key, value := range hub.rates[1] {
		other[key] = value
	}
	other["id"] = "other-cash-rate"
	hub.rates = append(hub.rates, other)
	hub.mu.Unlock()
	env := map[string]string{"UNIHUB_PLATFORM_ENABLED": "true", "UNIHUB_ROUTING_PRINCIPAL_ID": "1", "UNIHUB_MAX_INPUT_TOKENS": "1000", "UNIHUB_MAX_OUTPUT_TOKENS": "100", "UNIHUB_MAX_BODY_BYTES": "8192", "UNIHUB_MODEL_RATE_IDS": `{"test":"credit-rate"}`, "UNIHUB_MODEL_ASSET_RATE_IDS": `{"CNY/6":{"test":"cash-rate"}}`}
	c, err := ConfigFromEnv(func(key string) string { return env[key] })
	require.NoError(t, err)
	c.Origin, c.ServiceToken, c.Group = a.config.Origin, a.config.ServiceToken, a.config.Group
	a.config = c
	_, err = a.Admit(context.Background(), "uh_model_token", "task", "call", []byte(`{"model":"test","messages":[{"role":"user","content":"hi"}],"max_tokens":50}`))
	require.ErrorIs(t, err, ErrInvalid)
	hub.mu.Lock()
	assert.Zero(t, hub.reserveCount)
	hub.mu.Unlock()
}

// A version-0 CREDIT digest is part of the deployed idempotency contract.
func TestLegacyCallDigestAndOldRowsRemainCompatible(t *testing.T) {
	fail := false
	a, db := fixture(t, &fail)
	ctx := context.Background()
	body := []byte(`{"model":"test","messages":[{"role":"user","content":"hi"}],"max_tokens":50}`)
	_, err := a.Admit(ctx, "uh_model_token", "task", "call", body)
	require.NoError(t, err)
	var row Call
	require.NoError(t, db.First(&row).Error)

	require.NoError(t, db.Model(&Call{}).Where("call_id = ?", "call").Update("updated_at", time.Now().Add(-2*time.Minute)).Error)
	require.NoError(t, a.Recover(ctx))
	require.NoError(t, db.First(&row).Error)
	assert.Equal(t, "RELEASED", row.Status)
	// Simulate the exact pre-v3 persisted contract after the original call has
	// completed. Upgrading credentials must not reinterpret its old digest.
	sum := sha256.Sum256(append([]byte("task\x00"), body...))
	require.NoError(t, db.Model(&Call{}).Where("call_id = ?", "call").Updates(map[string]any{"digest": hex.EncodeToString(sum[:]), "digest_version": "", "wire_operation": "", "asset": "", "funding_mode": "", "funding_product_id": ""}).Error)
	_, err = a.Admit(ctx, "uh_model_token", "task", "call", body)
	assert.ErrorIs(t, err, ErrDuplicate)
	require.NoError(t, db.First(&row).Error)
	assert.Equal(t, hex.EncodeToString(sum[:]), row.Digest)
	receipt, err := a.Receipt(ctx, "uh_model_reissued", "task", "call")
	require.NoError(t, err)
	data, err := common.Marshal(receipt)
	require.NoError(t, err)
	var got map[string]any
	require.NoError(t, common.Unmarshal(data, &got))
	assert.Equal(t, "CREDIT/6", got["asset"])
}

func TestRequestDigestBindsAssetAndProfileSnapshot(t *testing.T) {
	seen := make(map[string]bool)
	for _, tc := range []struct {
		asset, mode string
		version     int
	}{{"", "", 0}, {"CNY/6", "prepaid_cash", 1}, {"CNY/6", "prepaid_cash", 2}, {"CREDIT/6", "credits", 1}} {
		a, _, _ := fundingFixture(t, tc.asset, tc.mode, tc.version)
		s, err := a.Admit(context.Background(), "uh_model_token", "task", "call", []byte(`{"model":"test","messages":[{"role":"user","content":"hi"}],"max_tokens":50}`))
		require.NoError(t, err)
		assert.False(t, seen[s.call.Digest], "funding snapshots must have different idempotency digests")
		seen[s.call.Digest] = true
	}
}

func TestMigrationPreservesLegacyInFlightFunding(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+t.TempDir()+"/legacy.db"), &gorm.Config{})
	require.NoError(t, err)
	// This is the pre-funding schema, with an existing durable unsettled call.
	type legacyCall struct {
		CallID string `gorm:"primaryKey;size:128"`
		Status string `gorm:"size:32;index"`
		Intent string `gorm:"type:text"`
	}
	require.NoError(t, db.Table("platform_model_calls").AutoMigrate(&legacyCall{}))
	require.NoError(t, db.Table("platform_model_calls").Create(&legacyCall{CallID: "inflight", Status: "SETTLE_PENDING", Intent: `{"usage":{"input":"1","output":"0","cache_read":"0","cache_write":"0","reasoning":"0"},"provider_cost":""}`}).Error)
	_, err = New(db, Config{Enabled: true, Origin: "http://localhost:8088", ServiceToken: "service-service-service-service-token", Group: "platform", MaxInputTokens: 1000, MaxOutputTokens: 100, MaxBodyBytes: 8192})
	require.NoError(t, err)
	var row Call
	require.NoError(t, db.First(&row).Error)
	assert.Equal(t, "CREDIT/6", row.Asset)
	assert.Equal(t, "credits", row.FundingMode)
	assert.Equal(t, "llmrouter", row.FundingProductID)
	assert.Zero(t, row.BillingProfileVersion)
	assert.Equal(t, "SETTLE_PENDING", row.Status)
	assert.Contains(t, row.Intent, `"input":"1"`)
}

func TestCashLostReserveResponseReleasesWithoutRedispatch(t *testing.T) {
	a, db, hub := fundingFixture(t, "CNY/6", "prepaid_cash", 1)
	hub.mu.Lock()
	hub.failReserve = true
	hub.mu.Unlock()
	ctx := context.Background()
	body := []byte(`{"model":"test","messages":[{"role":"user","content":"hi"}],"max_tokens":50}`)
	_, err := a.Admit(ctx, "uh_model_token", "task", "call", body)
	require.Error(t, err)
	_, err = a.Admit(ctx, "uh_model_token", "task", "call", body)
	assert.ErrorIs(t, err, ErrDuplicate)
	require.NoError(t, a.Recover(ctx))
	require.NoError(t, a.Recover(ctx))
	var row Call
	require.NoError(t, db.First(&row).Error)
	assert.Equal(t, "RELEASED", row.Status)
	var attempts int64
	require.NoError(t, db.Model(&Attempt{}).Count(&attempts).Error)
	assert.Zero(t, attempts)
	hub.mu.Lock()
	assert.Equal(t, 1, hub.reserveCount)
	assert.Equal(t, 1, hub.releaseCount)
	hub.mu.Unlock()
}

func TestFundingSnapshotPreventsCallIDReinterpretation(t *testing.T) {
	a, _, hub := fundingFixture(t, "CNY/6", "prepaid_cash", 1)
	ctx := context.Background()
	body := []byte(`{"model":"test","messages":[{"role":"user","content":"hi"}],"max_tokens":50}`)
	_, err := a.Admit(ctx, "uh_model_token", "task", "call", body)
	require.NoError(t, err)
	hub.mu.Lock()
	hub.grant["asset"], hub.grant["funding_mode"] = "CREDIT/6", "credits"
	hub.mu.Unlock()
	_, err = a.Admit(ctx, "uh_model_token", "task", "call", body)
	assert.ErrorIs(t, err, ErrConflict)
	hub.mu.Lock()
	assert.Equal(t, 1, hub.reserveCount)
	hub.mu.Unlock()
}

// Wrong-asset reserve responses must not authorize dispatch or recovery refunds.
func TestCashReservationRecoveryRejectsChangedFunding(t *testing.T) {
	for _, changed := range []string{"asset", "rate_asset", "mode", "funder", "profile_version"} {
		t.Run(changed, func(t *testing.T) {
			a, db, hub := fundingFixture(t, "CNY/6", "prepaid_cash", 1)
			hub.mu.Lock()
			switch changed {
			case "asset":
				hub.responseAsset = "CREDIT/6"
			case "rate_asset":
				hub.responseRateAsset = "CREDIT/6"
			case "mode":
				hub.responseMode = "credits"
			case "funder":
				hub.responseFunder = "product"
			case "profile_version":
				otherVersion := 2
				hub.responseVersion = &otherVersion
			}
			hub.mu.Unlock()
			ctx := context.Background()
			_, err := a.Admit(ctx, "uh_model_token", "task", "call", []byte(`{"model":"test","messages":[{"role":"user","content":"hi"}],"max_tokens":50}`))
			assert.ErrorIs(t, err, ErrPending)
			require.NoError(t, a.Recover(ctx))
			var row Call
			require.NoError(t, db.First(&row).Error)
			assert.Equal(t, "RESERVE_UNKNOWN", row.Status)
			hub.mu.Lock()
			assert.Zero(t, hub.releaseCount)
			hub.responseAsset, hub.responseRateAsset, hub.responseMode, hub.responseFunder = "", "", "", ""
			hub.responseVersion = nil
			hub.mu.Unlock()
			require.NoError(t, a.Recover(ctx))
			require.NoError(t, db.First(&row).Error)
			assert.Equal(t, "RELEASED", row.Status)
			hub.mu.Lock()
			assert.Equal(t, 1, hub.reserveCount)
			assert.Equal(t, 1, hub.releaseCount)
			hub.mu.Unlock()
		})
	}
}

func TestCashSettlementReceiptKeepsOriginalAssetAfterProfileChange(t *testing.T) {
	a, db, hub := fundingFixture(t, "CNY/6", "prepaid_cash", 3)
	ctx := context.Background()
	s, err := a.Admit(ctx, "uh_model_token", "task", "call", []byte(`{"model":"test","messages":[{"role":"user","content":"hi"}],"max_tokens":50}`))
	require.NoError(t, err)
	require.NoError(t, s.Dispatch(ctx, 1))
	s.observe(&dto.Usage{PromptTokens: 10, CompletionTokens: 5})
	hub.mu.Lock()
	hub.responseAsset = "CREDIT/6"
	hub.mu.Unlock()
	require.NoError(t, s.Finish(ctx))
	var row Call
	require.NoError(t, db.First(&row).Error)
	assert.Equal(t, "SETTLE_PENDING", row.Status)
	hub.mu.Lock()
	hub.responseAsset = ""
	hub.grant["asset"], hub.grant["funding_mode"], hub.grant["billing_profile_version"] = "CREDIT/6", "credits", 4
	hub.mu.Unlock()
	require.NoError(t, a.Recover(ctx))
	receipt, err := a.Receipt(ctx, "uh_model_reissued", "task", "call")
	require.NoError(t, err)
	data, err := common.Marshal(receipt)
	require.NoError(t, err)
	var got map[string]any
	require.NoError(t, common.Unmarshal(data, &got))
	assert.Equal(t, "CNY/6", got["asset"])
	assert.Equal(t, "SETTLED", got["status"])
	assert.Equal(t, "20", got["charged"])
	hub.mu.Lock()
	assert.Equal(t, 1, hub.reserveCount)
	assert.Equal(t, 2, hub.settleCount)
	hub.mu.Unlock()
}
