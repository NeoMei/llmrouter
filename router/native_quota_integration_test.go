package router

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/go-redis/redis/v8"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const nativeQuotaFixtureKey = "synthetic-process-native-quota-key"

// Only the strict parent acceptance starts this helper. All connection settings
// remain in environment variables and are never printed, including on failure.
func TestNativeQuotaAcceptanceProcess(t *testing.T) {
	mode := os.Getenv("NATIVE_QUOTA_CHILD_MODE")
	if mode == "" {
		t.Skip("helper process started only by the strict native acceptance parent")
	}
	if os.Getenv("NATIVE_REALM_INTEGRATION") != "1" || os.Getenv("NATIVE_REALM_TEST_ISOLATED") != "1" {
		t.Fatal("isolated parent authorization required")
	}
	if validateNativeRealmPostgresURL(os.Getenv("NATIVE_REALM_TEST_POSTGRES_DSN")) != nil {
		t.Fatal("unsafe parent database")
	}
	// The parent generated this schema and already validated the actual endpoint.
	dsn := os.Getenv("NATIVE_QUOTA_CHILD_SCHEMA_DSN")
	childURL, parseErr := url.Parse(dsn)
	if parseErr != nil {
		t.Fatal("invalid disposable parent schema")
	}
	query := childURL.Query()
	schema := query.Get("search_path")
	if !strings.HasPrefix(schema, "native_accept_") || len(schema) != len("native_accept_")+32 {
		t.Fatal("invalid disposable parent schema")
	}
	if _, err := hex.DecodeString(strings.TrimPrefix(schema, "native_accept_")); err != nil {
		t.Fatal("invalid disposable parent schema")
	}
	query.Del("search_path")
	childURL.RawQuery = query.Encode()
	baseURL, parseErr := url.Parse(os.Getenv("NATIVE_REALM_TEST_POSTGRES_DSN"))
	if parseErr != nil {
		t.Fatal("unsafe parent endpoint")
	}
	baseURL.RawQuery = baseURL.Query().Encode()
	if childURL.String() != baseURL.String() {
		t.Fatal("child endpoint differs from the validated parent endpoint")
	}
	db, err := gorm.Open(postgres.New(postgres.Config{DSN: dsn, PreferSimpleProtocol: true}), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal("child database unavailable")
	}
	redisURL, parseErr := url.Parse(os.Getenv("NATIVE_REALM_TEST_REDIS_URL"))
	if parseErr != nil || !nativeRealmLoopback(redisURL.Hostname()) {
		t.Fatal("unsafe child Redis")
	}
	options, err := redis.ParseURL(os.Getenv("NATIVE_REALM_TEST_REDIS_URL"))
	if err != nil {
		t.Fatal("unsafe child Redis")
	}
	client := redis.NewClient(options)
	defer client.Close()
	model.DB = db
	common.CryptoSecret = "isolated-native-quota-fixture-crypto"
	common.RDB, common.RedisEnabled, common.BatchUpdateEnabled = client, true, true
	common.SetDatabaseTypes(common.DatabaseTypePostgreSQL, common.DatabaseTypePostgreSQL)
	model.InitDatabaseColumns()
	gate := os.NewFile(3, "parent-admission-gate")
	session := os.Getenv("NATIVE_QUOTA_CHILD_SESSION")
	if mode == "policy-race" {
		ready := os.NewFile(4, "policy-read-ready")
		var paused atomic.Bool
		require.NoError(t, db.Callback().Query().After("gorm:query").Register("native-child-policy-read", func(tx *gorm.DB) {
			if tx.Statement.Table == "tokens" && paused.CompareAndSwap(false, true) {
				_, writeErr := ready.Write([]byte{1})
				if writeErr != nil {
					tx.AddError(writeErr)
					return
				}
				_, readErr := io.ReadFull(gate, make([]byte, 1))
				if readErr != nil {
					tx.AddError(readErr)
				}
			}
		}))
	} else {
		_, err = io.ReadFull(gate, make([]byte, 1))
		require.NoError(t, err)
	}
	switch mode {
	case "reserve":
		accepted, err := model.ReserveNativeTokenQuota(1902, nativeQuotaFixtureKey, session, 60)
		require.NoError(t, err)
		fmt.Printf("native-quota-accepted=%t\n", accepted)
	case "restart":
		accepted, err := model.ReserveNativeTokenQuota(1902, nativeQuotaFixtureKey, session, 60)
		require.NoError(t, err)
		require.True(t, accepted)
		remain, used, err := model.NativeTokenQuotaBalance(context.Background(), 1902, 0)
		require.NoError(t, err)
		require.Equal(t, 40, remain)
		require.Equal(t, 60, used)
		require.NoError(t, model.FlushNativeTokenQuotaProjections())
	case "refund":
		require.NoError(t, model.RefundNativeTokenQuota(1902, nativeQuotaFixtureKey, session))
	case "policy-race":
		accepted, err := model.TryReserveTokenQuota(1902, nativeQuotaFixtureKey, 60, true, "cross-process-policy-race")
		require.NoError(t, err)
		require.False(t, accepted)
	default:
		t.Fatal("unsupported helper operation")
	}
	require.True(t, common.RedisEnabled)
	require.True(t, common.BatchUpdateEnabled)
}

func nativeQuotaChild(t *testing.T, dsn, mode, session string, policy bool) (*exec.Cmd, *bytes.Buffer, *os.File, *os.File) {
	t.Helper()
	readGate, writeGate, err := os.Pipe()
	require.NoError(t, err)
	childContext, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	command := exec.CommandContext(childContext, os.Args[0], "-test.run=^TestNativeQuotaAcceptanceProcess$", "-test.count=1")
	command.Env = append(os.Environ(), "NATIVE_QUOTA_CHILD_MODE="+mode, "NATIVE_QUOTA_CHILD_SCHEMA_DSN="+dsn, "NATIVE_QUOTA_CHILD_SESSION="+session)
	command.ExtraFiles = []*os.File{readGate}
	var readyRead, readyWrite *os.File
	if policy {
		readyRead, readyWrite, err = os.Pipe()
		require.NoError(t, err)
		command.ExtraFiles = append(command.ExtraFiles, readyWrite)
	}
	output := &bytes.Buffer{}
	command.Stdout, command.Stderr = output, output
	require.NoError(t, command.Start())
	require.NoError(t, readGate.Close())
	if policy {
		require.NoError(t, readyWrite.Close())
	}
	t.Cleanup(func() {
		_ = writeGate.Close()
		if readyRead != nil {
			_ = readyRead.Close()
		}
		if command.ProcessState == nil {
			_ = command.Process.Kill()
			_ = command.Wait()
		}
	})
	return command, output, writeGate, readyRead
}

func nativeQuotaRealExercise(t *testing.T, db *gorm.DB, client *redis.Client, dsn string) {
	t.Helper()
	ctx := context.Background()
	oldCrypto := common.CryptoSecret
	common.CryptoSecret = "isolated-native-quota-fixture-crypto"
	t.Cleanup(func() { common.CryptoSecret = oldCrypto })
	require.True(t, common.RedisEnabled)
	require.True(t, common.BatchUpdateEnabled)
	owner := model.User{Id: 1901, Username: "native-quota-process", AffCode: "native-quota-process", Status: common.UserStatusEnabled, AuthVersion: 1, Quota: 1000}
	token := model.Token{Id: 1902, UserId: owner.Id, Key: nativeQuotaFixtureKey, AuthRealm: model.NativeFundingLocalOnly, Status: common.TokenStatusEnabled, ExpiredTime: -1, RemainQuota: 100}
	require.NoError(t, db.Create(&owner).Error)
	require.NoError(t, db.Create(&token).Error)
	cacheKey := "token:" + common.GenerateHMAC(token.Key)
	t.Cleanup(func() {
		assert.NoError(t, client.Del(ctx, cacheKey, "token:fence:"+common.GenerateHMAC(token.Key)).Err())
	})
	keyHash := sha256.Sum256([]byte(token.Key))
	raw, err := common.Marshal(model.NativeTokenQuotaManifest{Version: 1, CutoverEvidence: strings.Repeat("a", 64), Tokens: []model.NativeTokenQuotaImport{{TokenID: token.Id, UserID: owner.Id, KeySHA256: hex.EncodeToString(keyHash[:]), RemainQuota: 100, Status: common.TokenStatusEnabled, ExpiredTime: -1}}})
	require.NoError(t, err)
	approved := sha256.Sum256(raw)
	require.NoError(t, model.ImportNativeTokenQuota(ctx, db, raw, hex.EncodeToString(approved[:])))
	first, firstOutput, firstGate, _ := nativeQuotaChild(t, dsn, "reserve", "native-process-first", false)
	second, secondOutput, secondGate, _ := nativeQuotaChild(t, dsn, "reserve", "native-process-second", false)
	_, err = firstGate.Write([]byte{1})
	require.NoError(t, err)
	_, err = secondGate.Write([]byte{1})
	require.NoError(t, err)
	require.NoError(t, first.Wait(), "first child failed: %s", firstOutput.String())
	require.NoError(t, second.Wait(), "second child failed: %s", secondOutput.String())
	firstAccepted := strings.Contains(firstOutput.String(), "native-quota-accepted=true")
	secondAccepted := strings.Contains(secondOutput.String(), "native-quota-accepted=true")
	require.NotEqual(t, firstAccepted, secondAccepted, "independent processes must share the same 100 quota cap")
	winner := "native-process-first"
	if secondAccepted {
		winner = "native-process-second"
	}
	var projected model.Token
	require.NoError(t, db.First(&projected, token.Id).Error)
	require.Equal(t, 100, projected.RemainQuota, "both accepting processes exited without flushing their private RAM")
	var acceptedCount, declinedCount int64
	require.NoError(t, db.Model(&model.NativeTokenQuotaOperation{}).Where("token_id = ? AND outcome = ?", token.Id, "accepted").Count(&acceptedCount).Error)
	require.NoError(t, db.Model(&model.NativeTokenQuotaOperation{}).Where("token_id = ? AND outcome = ?", token.Id, "declined").Count(&declinedCount).Error)
	require.EqualValues(t, 1, acceptedCount)
	require.EqualValues(t, 1, declinedCount)
	// A fresh process sees the durable hold after deliberate loss of only this
	// fixture's Redis projection and repeats the same operation without debit.
	require.NoError(t, client.Del(ctx, cacheKey).Err())
	restarted, restartOutput, restartGate, _ := nativeQuotaChild(t, dsn, "restart", winner, false)
	_, err = restartGate.Write([]byte{1})
	require.NoError(t, err)
	require.NoError(t, restarted.Wait(), restartOutput.String())
	require.NoError(t, db.First(&projected, token.Id).Error)
	require.Equal(t, 40, projected.RemainQuota)
	require.Equal(t, 60, projected.UsedQuota)
	for i := 0; i < 2; i++ {
		refund, output, gate, _ := nativeQuotaChild(t, dsn, "refund", winner, false)
		_, err = gate.Write([]byte{1})
		require.NoError(t, err)
		require.NoError(t, refund.Wait(), output.String())
	}
	remain, used, err := model.NativeTokenQuotaBalance(ctx, token.Id, 0)
	require.NoError(t, err)
	require.Equal(t, 100, remain)
	require.Zero(t, used)
	require.NoError(t, db.First(&projected, token.Id).Error)
	require.Equal(t, 40, projected.RemainQuota, "refund is spendable before the batch materialization catches up")
	accepted, err := model.TryReserveTokenQuota(token.Id, token.Key, 30, false, "after-process-refund")
	require.NoError(t, err)
	require.True(t, accepted)
	projected.UnlimitedQuota = true
	projected.RemainQuota = 100
	require.NoError(t, projected.Update(model.NativeFundingLocalOnly))
	policy, policyOutput, policyGate, ready := nativeQuotaChild(t, dsn, "policy-race", "", true)
	_, err = io.ReadFull(ready, make([]byte, 1))
	require.NoError(t, err)
	projected.UnlimitedQuota = false
	projected.RemainQuota = 25
	require.NoError(t, projected.Update(model.NativeFundingLocalOnly))
	_, err = policyGate.Write([]byte{1})
	require.NoError(t, err)
	require.NoError(t, policy.Wait(), policyOutput.String())
	require.NoError(t, db.First(&projected, token.Id).Error)
	remain, used, err = model.NativeTokenQuotaBalance(ctx, token.Id, projected.QuotaPolicyGeneration)
	require.NoError(t, err)
	require.Equal(t, 25, remain)
	require.Equal(t, 30, used)
	model.FlushBatchUpdates()
	require.NoError(t, db.First(&projected, token.Id).Error)
	require.Equal(t, 25, projected.RemainQuota)
	require.Equal(t, 30, projected.UsedQuota)
	require.Equal(t, nativeQuotaFixtureKey, projected.Key)
	require.Equal(t, owner.Id, projected.UserId)
	require.Equal(t, model.NativeFundingLocalOnly, projected.AuthRealm)
	require.True(t, common.RedisEnabled)
	require.True(t, common.BatchUpdateEnabled)
}
