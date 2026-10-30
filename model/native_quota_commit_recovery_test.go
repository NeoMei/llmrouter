package model

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/QuantumNous/new-api/common"
	sqlitedriver "github.com/glebarez/go-sqlite"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type nativeCommitControl struct {
	commits   atomic.Int32
	armed     atomic.Bool
	committed bool
}
type nativeCommitDriver struct {
	base    driver.Driver
	control *nativeCommitControl
}
type nativeCommitConn struct {
	driver.Conn
	control *nativeCommitControl
}
type nativeCommitTx struct {
	driver.Tx
	control *nativeCommitControl
}

func (d nativeCommitDriver) Open(name string) (driver.Conn, error) {
	conn, err := d.base.Open(name)
	if err != nil {
		return nil, err
	}
	return nativeCommitConn{Conn: conn, control: d.control}, nil
}
func (c nativeCommitConn) Begin() (driver.Tx, error) {
	tx, err := c.Conn.Begin()
	if err != nil {
		return nil, err
	}
	return nativeCommitTx{Tx: tx, control: c.control}, nil
}
func (t nativeCommitTx) Commit() error {
	if t.control.armed.Load() && t.control.commits.Add(1) == 2 {
		if t.control.committed {
			if err := t.Tx.Commit(); err != nil {
				return err
			}
		} else {
			if err := t.Tx.Rollback(); err != nil {
				return err
			}
		}
		return errors.New("synthetic lost COMMIT response")
	}
	return t.Tx.Commit()
}

// The database driver returns the same transport error for a committed write
// and an aborted one. Only the durable receipt may decide whether to debit.
func TestNativeQuotaUnknownCommitReadsSameReceiptWithoutResending(t *testing.T) {
	for _, committed := range []bool{false, true} {
		t.Run(fmt.Sprintf("committed_%t", committed), func(t *testing.T) {
			control := &nativeCommitControl{committed: committed}
			name := fmt.Sprintf("native-commit-%p", control)
			sql.Register(name, nativeCommitDriver{base: &sqlitedriver.Driver{}, control: control})
			db, err := gorm.Open(sqlite.Dialector{DriverName: name, DSN: t.TempDir() + "/unknown.db"}, &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
			require.NoError(t, err)
			conn, err := db.DB()
			require.NoError(t, err)
			conn.SetMaxOpenConns(1)
			require.NoError(t, db.AutoMigrate(&Token{}))
			require.NoError(t, RegisterNativeTokenQuotaModels(db))
			previousDB := DB
			DB = db
			t.Cleanup(func() { DB = previousDB; require.NoError(t, conn.Close()) })
			resetBatchUpdateTestState(t)
			server := useUserCacheMiniRedis(t)
			common.BatchUpdateEnabled = true
			token := createReserveTestToken(t, 100)
			require.NoError(t, db.Model(&token).Update("auth_realm", NativeFundingLocalOnly).Error)
			enrollNativeQuotaFixture(t, token.Id)
			control.armed.Store(true)
			ok, err := TryReserveTokenQuota(token.Id, token.Key, 60, false, "lost-commit")
			if committed {
				require.NoError(t, err)
				require.True(t, ok)
			} else {
				require.ErrorIs(t, err, ErrNativeQuotaUnknown)
				assert.False(t, ok)
			}
			control.armed.Store(false)
			receipt, readErr := NativeTokenQuotaOperationReceipt(token.Id, "lost-commit")
			require.NoError(t, readErr)
			if committed {
				assert.Equal(t, "accepted", receipt.Outcome)
			} else {
				assert.Equal(t, "prepared", receipt.Outcome)
			}
			server.FlushAll()
			ok, err = TryReserveTokenQuota(token.Id, token.Key, 60, false, "lost-commit")
			if committed {
				require.NoError(t, err)
				assert.True(t, ok)
			} else {
				require.ErrorIs(t, err, ErrNativeQuotaUnknown)
				assert.False(t, ok)
			}
			remain, used, err := NativeTokenQuotaBalance(context.Background(), token.Id, 0)
			require.NoError(t, err)
			if committed {
				assert.Equal(t, 40, remain)
				assert.Equal(t, 60, used)
			} else {
				assert.Equal(t, 100, remain)
				assert.Zero(t, used)
			}
		})
	}
}
