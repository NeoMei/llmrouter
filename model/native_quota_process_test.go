package model

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/glebarez/sqlite"
	"github.com/go-redis/redis/v8"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// Separate processes open the same durable DB. Pipes release the admissions
// together; neither a process mutex nor its private batch map can serialize them.
func TestNativeQuotaIndependentProcessAdmissionAndRecovery(t *testing.T) {
	if dbPath := os.Getenv("NATIVE_QUOTA_PROCESS_FIXTURE"); dbPath != "" {
		db, err := gorm.Open(sqlite.Open(dbPath+"?_pragma=busy_timeout(10000)"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
		require.NoError(t, err)
		DB = db
		common.CryptoSecret = "native-quota-process-fixture-crypto"
		common.RedisEnabled, common.BatchUpdateEnabled = true, true
		common.RDB = redis.NewClient(&redis.Options{Addr: os.Getenv("NATIVE_QUOTA_PROCESS_REDIS")})
		gate := os.NewFile(3, "admission-gate")
		_, err = io.ReadFull(gate, make([]byte, 1))
		require.NoError(t, err)
		ok, err := TryReserveTokenQuota(1, "process-synthetic-key", 60, false, os.Getenv("NATIVE_QUOTA_PROCESS_OP"))
		require.NoError(t, err)
		fmt.Printf("admitted=%t\n", ok)
		return
	}
	oldCrypto := common.CryptoSecret
	common.CryptoSecret = "native-quota-process-fixture-crypto"
	t.Cleanup(func() { common.CryptoSecret = oldCrypto })
	dbPath := t.TempDir() + "/native-process.db"
	db, err := gorm.Open(sqlite.Open(dbPath+"?_pragma=busy_timeout(10000)"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&Token{}))
	require.NoError(t, RegisterNativeTokenQuotaModels(db))
	oldDB, oldRedis, oldBatch := DB, common.RedisEnabled, common.BatchUpdateEnabled
	DB, common.RedisEnabled, common.BatchUpdateEnabled = db, false, true
	t.Cleanup(func() { DB, common.RedisEnabled, common.BatchUpdateEnabled = oldDB, oldRedis, oldBatch })
	require.NoError(t, db.Create(&Token{Id: 1, UserId: 1, Key: "process-synthetic-key", AuthRealm: NativeFundingLocalOnly, Status: common.TokenStatusEnabled, ExpiredTime: -1, RemainQuota: 100}).Error)
	enrollNativeQuotaFixture(t, 1)
	server := useUserCacheMiniRedis(t)
	commands := make([]*exec.Cmd, 2)
	outputs := make([]bytes.Buffer, 2)
	gates := make([]*os.File, 2)
	for i := range commands {
		readGate, writeGate, err := os.Pipe()
		require.NoError(t, err)
		commands[i] = exec.Command(os.Args[0], "-test.run=^TestNativeQuotaIndependentProcessAdmissionAndRecovery$", "-test.count=1")
		commands[i].Env = append(os.Environ(), "NATIVE_QUOTA_PROCESS_FIXTURE="+dbPath, "NATIVE_QUOTA_PROCESS_OP=process-"+strconv.Itoa(i), "NATIVE_QUOTA_PROCESS_REDIS="+server.Addr())
		commands[i].ExtraFiles = []*os.File{readGate}
		commands[i].Stdout, commands[i].Stderr = &outputs[i], &outputs[i]
		require.NoError(t, commands[i].Start())
		require.NoError(t, readGate.Close())
		gates[i] = writeGate
	}
	for _, gate := range gates {
		_, err = gate.Write([]byte{1})
		require.NoError(t, err)
		require.NoError(t, gate.Close())
	}
	accepted := 0
	for i, command := range commands {
		require.NoError(t, command.Wait(), outputs[i].String())
		if bytes.Contains(outputs[i].Bytes(), []byte("admitted=true")) {
			accepted++
		}
	}
	assert.Equal(t, 1, accepted)
	// Neither exited child flushed its token table projection.
	assert.Equal(t, 100, getTokenFromDB(t, 1).RemainQuota)
	remain, used, err := NativeTokenQuotaBalance(context.Background(), 1, 0)
	require.NoError(t, err)
	assert.Equal(t, 40, remain)
	assert.Equal(t, 60, used)
	require.NoError(t, FlushNativeTokenQuotaProjections())
	assert.Equal(t, 40, getTokenFromDB(t, 1).RemainQuota)
	assert.Equal(t, 60, getTokenFromDB(t, 1).UsedQuota)
}
