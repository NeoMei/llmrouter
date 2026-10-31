package platformbilling

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func waiverFixture(t *testing.T) (*gorm.DB, Call, Attempt, map[string]any) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.TempDir()+"/waiver.db?_pragma=busy_timeout(5000)"), &gorm.Config{Logger: logger.Discard})
	require.NoError(t, err)
	if dsn := os.Getenv("PLATFORM_FAULT_WAIVER_TEST_POSTGRES_DSN"); dsn != "" {
		sqliteDB, closeErr := db.DB()
		require.NoError(t, closeErr)
		require.NoError(t, sqliteDB.Close())
		base, openErr := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Discard})
		require.NoError(t, openErr)
		schema := fmt.Sprintf("waiver_lr_%d", time.Now().UnixNano())
		require.NoError(t, base.Exec("CREATE SCHEMA "+schema).Error)
		if u, parseErr := url.Parse(dsn); parseErr == nil && (u.Scheme == "postgres" || u.Scheme == "postgresql") {
			q := u.Query()
			q.Set("search_path", schema)
			u.RawQuery = q.Encode()
			dsn = u.String()
		} else {
			dsn += " search_path=" + schema
		}
		db, err = gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Discard})
		require.NoError(t, err)
		t.Cleanup(func() {
			require.NoError(t, base.Exec("DROP SCHEMA "+schema+" CASCADE").Error)
			for _, connection := range []*gorm.DB{db, base} {
				sqlDB, e := connection.DB()
				require.NoError(t, e)
				require.NoError(t, sqlDB.Close())
			}
		})
	} else {
		t.Cleanup(func() { sqlDB, e := db.DB(); require.NoError(t, e); require.NoError(t, sqlDB.Close()) })
	}
	require.NoError(t, db.AutoMigrate(&Call{}, &Attempt{}))
	require.NoError(t, InstallPlatformFaultWaiverSchema(context.Background(), db))
	rate := Rate{ID: "cash-embedding", Model: "embedding", Asset: "CNY/6", Status: "published", Semantics: "metered_v2"}
	rateJSON, err := common.Marshal(rate)
	require.NoError(t, err)
	row := Call{CallID: "waiver-call", ActorID: "actor", AccountID: "payer", OriginProductID: "llmrouter", InstanceID: "default", CredentialID: "credential", TaskID: "task", Model: "embedding", Digest: strings.Repeat("a", 64), RateID: rate.ID, RateSnapshot: string(rateJSON), Asset: "CNY/6", FundingMode: "prepaid_cash", FundingProductID: "llmrouter", Amount: "2048", Operation: "embedding", WireOperation: "embeddings.create", Status: "UNKNOWN", CreatedAt: time.Unix(1000, 0).UTC(), UpdatedAt: time.Unix(1001, 0).UTC()}
	require.NoError(t, db.Create(&row).Error)
	attempt := Attempt{CallID: row.CallID, ChannelID: 102, Status: "DISPATCHED", CreatedAt: time.Unix(1001, 0).UTC(), UpdatedAt: time.Unix(1001, 0).UTC()}
	require.NoError(t, db.Create(&attempt).Error)
	var persisted Call
	require.NoError(t, db.First(&persisted, "call_id = ?", row.CallID).Error)
	require.True(t, row.CreatedAt.Equal(persisted.CreatedAt))
	row = persisted
	var persistedAttempt Attempt
	require.NoError(t, db.First(&persistedAttempt, attempt.ID).Error)
	require.True(t, attempt.CreatedAt.Equal(persistedAttempt.CreatedAt))
	attempt = persistedAttempt
	remote := RemoteCall{CallID: row.CallID, ActorID: row.ActorID, AccountID: row.AccountID, OriginProductID: row.OriginProductID, InstanceID: row.InstanceID, CredentialID: row.CredentialID, RequestDigest: row.Digest, Rate: rate, Held: row.Amount, Charged: "0", Status: "RESERVED", ChargeAsset: row.Asset, FundingMode: row.FundingMode, FundingProductID: row.FundingProductID}
	remote.WireOperation = row.WireOperation
	remoteJSON, err := common.Marshal(remote)
	require.NoError(t, err)
	var snapshot map[string]any
	require.NoError(t, common.UnmarshalUnique(remoteJSON, &snapshot))
	snapshot["task_id"] = row.TaskID
	snapshot["service_id"] = "llmrouter"
	approval := map[string]any{"schema": 1, "case_id": "reviewed-case", "operator_id": "finance-admin", "reason": "customer waived for platform fault; supplier outcome remains unresolved", "call_id": row.CallID, "actor_id": row.ActorID, "account_id": row.AccountID, "origin_product_id": row.OriginProductID, "instance_id": row.InstanceID, "asset": row.Asset, "held": row.Amount, "provider_state": "PROVIDER_UNRESOLVED", "original_evidence_sha256": strings.Repeat("1", 64), "followup_evidence_sha256": strings.Repeat("2", 64), "expected_central_row": map[string]any{"id": row.CallID, "account_id": row.AccountID, "request_digest": "original-reservation-digest", "snapshot": snapshot}, "gateway_call": row, "gateway_attempts": []Attempt{attempt}}
	approval["approval_statement"] = "The customer fee is waived for the platform fault; supplier usage and cost remain unresolved."
	approval["approval_source_sha256"] = strings.Repeat("3", 64)
	return db, row, attempt, approval
}

func waiverApprovalBytes(t *testing.T, approval map[string]any) ([]byte, string) {
	t.Helper()
	raw, err := common.Marshal(approval)
	require.NoError(t, err)
	sum := sha256.Sum256(raw)
	return raw, hex.EncodeToString(sum[:])
}

func TestPlatformFaultWaiverPreservesEvidenceAndRequiresExplicitApply(t *testing.T) {
	db, original, attempt, approval := waiverFixture(t)
	raw, digest := waiverApprovalBytes(t, approval)
	_, err := PreparePlatformFaultWaiver(context.Background(), db, raw, digest, false)
	require.NoError(t, err)
	var row Call
	require.NoError(t, db.First(&row, "call_id = ?", original.CallID).Error)
	assert.Equal(t, original, row)
	var count int64
	require.NoError(t, db.Model(&PlatformFaultWaiverDecision{}).Count(&count).Error)
	assert.Zero(t, count)
	_, err = PreparePlatformFaultWaiver(context.Background(), db, raw, digest, true)
	require.NoError(t, err)
	require.NoError(t, db.First(&row, "call_id = ?", original.CallID).Error)
	assert.Equal(t, "WAIVER_PENDING", row.Status)
	assert.Empty(t, row.Evidence)
	var saved Attempt
	require.NoError(t, db.First(&saved, attempt.ID).Error)
	assert.Equal(t, attempt, saved)
	var decision PlatformFaultWaiverDecision
	require.NoError(t, db.First(&decision).Error)
	assert.Equal(t, string(raw), decision.ApprovalJSON)
	assert.Equal(t, "PROVIDER_UNRESOLVED", decision.ProviderState)
	assert.Error(t, db.Model(&decision).Update("provider_state", "RESOLVED").Error)
	assert.Error(t, db.Delete(&decision).Error)
	_, err = PreparePlatformFaultWaiver(context.Background(), db, raw, digest, true)
	require.NoError(t, err)
	require.NoError(t, db.Model(&PlatformFaultWaiverDecision{}).Count(&count).Error)
	assert.EqualValues(t, 1, count)
}

func TestPlatformFaultWaiverRejectsChangedScopeAndFullSnapshot(t *testing.T) {
	for _, change := range []string{"account", "amount", "call_snapshot", "attempt", "confirmed_evidence", "approval_digest", "second_case"} {
		t.Run(change, func(t *testing.T) {
			db, original, attempt, approval := waiverFixture(t)
			switch change {
			case "account":
				approval["account_id"] = "other"
			case "amount":
				approval["held"] = "4096"
			case "call_snapshot":
				require.NoError(t, db.Model(&Call{}).Where("call_id = ?", original.CallID).Update("telemetry_latency_ms", 1).Error)
			case "attempt":
				require.NoError(t, db.Model(&Attempt{}).Where("id = ?", attempt.ID).Update("status", "USAGE_CONFIRMED").Error)
			case "confirmed_evidence":
				require.NoError(t, db.Model(&Call{}).Where("call_id = ?", original.CallID).Update("evidence", `{"state":"confirmed"}`).Error)
			case "second_case":
				raw, digest := waiverApprovalBytes(t, approval)
				_, err := PreparePlatformFaultWaiver(context.Background(), db, raw, digest, true)
				require.NoError(t, err)
				approval["case_id"] = "another-case"
			}
			raw, digest := waiverApprovalBytes(t, approval)
			if change == "approval_digest" {
				digest = strings.Repeat("0", 64)
			}
			_, err := PreparePlatformFaultWaiver(context.Background(), db, raw, digest, true)
			require.Error(t, err)
			var count int64
			require.NoError(t, db.Model(&PlatformFaultWaiverDecision{}).Count(&count).Error)
			if change != "second_case" {
				assert.Zero(t, count)
			}
		})
	}
}

func TestPlatformFaultWaiverFinalizeVerifiesCentralAndResumesWithoutProvider(t *testing.T) {
	db, original, attempt, approval := waiverFixture(t)
	raw, digest := waiverApprovalBytes(t, approval)
	_, err := PreparePlatformFaultWaiver(context.Background(), db, raw, digest, true)
	require.NoError(t, err)
	centralRow := approval["expected_central_row"].(map[string]any)
	snapshot := centralRow["snapshot"].(map[string]any)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, "/internal/v1/calls/"+original.CallID, r.URL.Path)
		data, marshalErr := common.Marshal(snapshot)
		require.NoError(t, marshalErr)
		_, _ = w.Write(data)
	}))
	defer server.Close()
	cfg := Config{Origin: server.URL, ServiceToken: strings.Repeat("s", 32)}
	_, err = FinalizePlatformFaultWaiver(context.Background(), db, cfg, raw, digest)
	require.Error(t, err, "central reserve is not a successful waiver")
	snapshot["status"], snapshot["charged"] = "RELEASED", "0"
	reason := "platform_fault_waiver:" + digest
	finalRaw, err := common.Marshal([]string{"release", reason})
	require.NoError(t, err)
	finalSum := sha256.Sum256(finalRaw)
	snapshot["final_digest"] = hex.EncodeToString(finalSum[:])
	snapshot["account_id"] = "other"
	_, err = FinalizePlatformFaultWaiver(context.Background(), db, cfg, raw, digest)
	require.Error(t, err)
	snapshot["account_id"] = original.AccountID
	_, err = FinalizePlatformFaultWaiver(context.Background(), db, cfg, raw, digest)
	require.NoError(t, err)
	_, err = FinalizePlatformFaultWaiver(context.Background(), db, cfg, raw, digest)
	require.NoError(t, err, "process restart reuses the same immutable decision")
	var row Call
	require.NoError(t, db.First(&row).Error)
	assert.Equal(t, "RELEASED", row.Status)
	assert.Equal(t, "0", row.Charged)
	assert.Empty(t, row.Evidence)
	var saved Attempt
	require.NoError(t, db.First(&saved).Error)
	assert.Equal(t, attempt, saved)
}

func TestPlatformFaultWaiverConcurrentStagesCreateOneDecision(t *testing.T) {
	db, _, _, approval := waiverFixture(t)
	raw, digest := waiverApprovalBytes(t, approval)
	var wg sync.WaitGroup
	start := make(chan struct{})
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := PreparePlatformFaultWaiver(context.Background(), db, raw, digest, true)
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	var successes int
	for err := range errs {
		if err == nil {
			successes++
		}
	}
	assert.Positive(t, successes)
	var count int64
	require.NoError(t, db.Model(&PlatformFaultWaiverDecision{}).Count(&count).Error)
	assert.EqualValues(t, 1, count)
	_, err := PreparePlatformFaultWaiver(context.Background(), db, raw, digest, true)
	require.NoError(t, err)
}

func TestPlatformFaultWaiverRejectsScopeChangesAfterStage(t *testing.T) {
	for _, column := range []string{"account_id", "amount", "telemetry_group"} {
		t.Run(column, func(t *testing.T) {
			db, original, _, approval := waiverFixture(t)
			raw, digest := waiverApprovalBytes(t, approval)
			_, err := PreparePlatformFaultWaiver(context.Background(), db, raw, digest, true)
			require.NoError(t, err)
			require.NoError(t, db.Model(&Call{}).Where("call_id = ?", original.CallID).Update(column, "changed").Error)
			_, err = PreparePlatformFaultWaiver(context.Background(), db, raw, digest, true)
			require.Error(t, err)
			snapshot := approval["expected_central_row"].(map[string]any)["snapshot"].(map[string]any)
			finalRaw, err := common.Marshal([]string{"release", "platform_fault_waiver:" + digest})
			require.NoError(t, err)
			finalSum := sha256.Sum256(finalRaw)
			snapshot["status"], snapshot["charged"], snapshot["final_digest"] = "RELEASED", "0", hex.EncodeToString(finalSum[:])
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				require.Equal(t, http.MethodGet, r.Method)
				data, err := common.Marshal(snapshot)
				require.NoError(t, err)
				_, _ = w.Write(data)
			}))
			defer server.Close()
			_, err = FinalizePlatformFaultWaiver(context.Background(), db, Config{Origin: server.URL, ServiceToken: strings.Repeat("s", 32)}, raw, digest)
			require.Error(t, err, "a central release cannot bless a changed gateway scope")
		})
	}
}

func TestPlatformFaultWaiverRejectsMissingBusinessApprovalEvidence(t *testing.T) {
	for _, field := range []string{"approval_statement", "approval_source_sha256"} {
		t.Run(field, func(t *testing.T) {
			db, _, _, approval := waiverFixture(t)
			approval[field] = ""
			raw, digest := waiverApprovalBytes(t, approval)
			_, err := PreparePlatformFaultWaiver(context.Background(), db, raw, digest, true)
			require.Error(t, err)
		})
	}
}
