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
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type checkinCommitControl struct {
	armed     atomic.Bool
	lostReply bool
	results   chan error
}
type checkinCommitDriver struct {
	driver.Driver
	control *checkinCommitControl
}
type checkinCommitConn struct {
	driver.Conn
	control *checkinCommitControl
}
type checkinCommitTx struct {
	driver.Tx
	control *checkinCommitControl
}

func (d checkinCommitDriver) Open(name string) (driver.Conn, error) {
	conn, err := d.Driver.Open(name)
	if err != nil {
		return nil, err
	}
	return checkinCommitConn{Conn: conn, control: d.control}, nil
}
func (c checkinCommitConn) Begin() (driver.Tx, error) {
	tx, err := c.Conn.Begin()
	if err != nil {
		return nil, err
	}
	return checkinCommitTx{Tx: tx, control: c.control}, nil
}
func (tx checkinCommitTx) Commit() error {
	err := tx.Tx.Commit()
	if tx.control.armed.Swap(false) {
		tx.control.results <- err
		if err == nil && tx.control.lostReply {
			return errors.New("synthetic lost check-in COMMIT response")
		}
	}
	return err
}

func checkinCommitFixture(t *testing.T, lostReply bool) (*gorm.DB, *ProductFundingStore, *checkinCommitControl) {
	t.Helper()
	control := &checkinCommitControl{lostReply: lostReply, results: make(chan error, 1)}
	name := fmt.Sprintf("checkin-commit-%p", control)
	sql.Register(name, checkinCommitDriver{Driver: &sqlitedriver.Driver{}, control: control})
	db, store := checkinRealmFixture(t, name)
	return db, store, control
}

func requireCheckinPoolClean(t *testing.T, db *gorm.DB) {
	t.Helper()
	pool, err := db.DB()
	require.NoError(t, err)
	require.Equal(t, 4, pool.Stats().MaxOpenConnections)
	var pinned []*sql.Conn
	t.Cleanup(func() {
		for _, conn := range pinned {
			_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
			_ = conn.Close()
		}
	})
	// Hold all four connections so every returned driver connection is checked,
	// including the one whose database/sql Tx is already closed after COMMIT.
	for range 4 {
		conn, err := pool.Conn(context.Background())
		require.NoError(t, err)
		pinned = append(pinned, conn)
		_, err = conn.ExecContext(context.Background(), "BEGIN")
		require.NoError(t, err, "a failed check-in must not return a connection inside an abandoned SQLite transaction")
		_, err = conn.ExecContext(context.Background(), "ROLLBACK")
		require.NoError(t, err)
		var timeout int
		require.NoError(t, conn.QueryRowContext(context.Background(), "PRAGMA busy_timeout").Scan(&timeout))
		assert.Zero(t, timeout)
	}
	for _, conn := range pinned {
		require.NoError(t, conn.Close())
	}
}

func TestUserCheckinSQLiteBusyCommitRollsBackPinnedConnection(t *testing.T) {
	db, _, control := checkinCommitFixture(t, false)
	require.NoError(t, PinNativeFundingRealm(context.Background(), 42, "reviewed-native"))
	useUserCacheMiniRedis(t)
	common.BatchUpdateEnabled = true
	_, err := GetUserCache(42)
	require.NoError(t, err)
	pool, err := db.DB()
	require.NoError(t, err)
	reader, err := pool.Conn(context.Background())
	require.NoError(t, err)
	readTx, err := reader.BeginTx(context.Background(), nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = readTx.Rollback(); _ = reader.Close() })
	var original int
	require.NoError(t, readTx.QueryRow("SELECT quota FROM users WHERE id = 42").Scan(&original))
	control.armed.Store(true)
	receipt, err := UserCheckin(42)
	require.Error(t, err)
	require.Nil(t, receipt)
	commitErr := <-control.results
	var sqliteErr *sqlitedriver.Error
	require.ErrorAs(t, commitErr, &sqliteErr)
	assert.Equal(t, 5, sqliteErr.Code()&0xff, "the actual SQLite COMMIT, not a simulated UPDATE, must fail BUSY")
	require.NoError(t, readTx.Rollback())
	require.NoError(t, reader.Close())
	requireCheckinPoolClean(t, db)
	var user User
	require.NoError(t, db.First(&user, 42).Error)
	assert.Zero(t, user.Quota)
	var receipts int64
	require.NoError(t, db.Model(&Checkin{}).Count(&receipts).Error)
	assert.Zero(t, receipts)
	cached, err := GetUserCache(42)
	require.NoError(t, err)
	assert.Zero(t, cached.Quota, "an uncommitted check-in must not publish a Redis credit")
	receipt, err = UserCheckin(42)
	require.NoError(t, err, "once the reader releases its lock, the failed daily entry remains retryable")
	require.Equal(t, 20, receipt.QuotaAwarded)
	_, err = UserCheckin(42)
	require.Error(t, err)
	require.NoError(t, db.First(&user, 42).Error)
	assert.Equal(t, 20, user.Quota)
	cached, err = GetUserCache(42)
	require.NoError(t, err)
	assert.Equal(t, 20, cached.Quota)
}

func TestUserCheckinSQLiteDeferredCommitFailureIsAtomic(t *testing.T) {
	for _, central := range []bool{false, true} {
		t.Run(fmt.Sprintf("central_%t", central), func(t *testing.T) {
			db, store, control := checkinCommitFixture(t, false)
			if central {
				require.NoError(t, store.ActivateZero(context.Background(), 42, "payer"))
			} else {
				require.NoError(t, PinNativeFundingRealm(context.Background(), 42, "reviewed-native"))
			}
			useUserCacheMiniRedis(t)
			common.BatchUpdateEnabled = true
			_, err := GetUserCache(42)
			require.NoError(t, err)
			require.NoError(t, db.Exec("CREATE TABLE checkin_commit_parent (id INTEGER PRIMARY KEY)").Error)
			require.NoError(t, db.Exec("CREATE TABLE checkin_commit_child (parent_id INTEGER REFERENCES checkin_commit_parent(id) DEFERRABLE INITIALLY DEFERRED)").Error)
			require.NoError(t, db.Exec("CREATE TRIGGER checkin_commit_failure AFTER INSERT ON checkins BEGIN INSERT INTO checkin_commit_child(parent_id) VALUES (987654); END").Error)
			control.armed.Store(true)
			receipt, err := UserCheckin(42)
			require.Error(t, err)
			require.Nil(t, receipt)
			commitErr := <-control.results
			var sqliteErr *sqlitedriver.Error
			require.ErrorAs(t, commitErr, &sqliteErr)
			assert.Equal(t, 19, sqliteErr.Code()&0xff, "the deferred FK must reject COMMIT after the writes")
			requireCheckinPoolClean(t, db)
			var user User
			require.NoError(t, db.First(&user, 42).Error)
			assert.Zero(t, user.Quota)
			for _, model := range []any{&Checkin{}, &ProductCreditAward{}} {
				var count int64
				require.NoError(t, db.Model(model).Count(&count).Error)
				assert.Zero(t, count, "a failed commit cannot strand a business receipt or central outbox entry")
			}
			cached, err := GetUserCache(42)
			require.NoError(t, err)
			assert.Zero(t, cached.Quota)
			require.NoError(t, db.Exec("DROP TRIGGER checkin_commit_failure").Error)
			_, err = UserCheckin(42)
			require.NoError(t, err)
			var receipts int64
			require.NoError(t, db.Model(&Checkin{}).Count(&receipts).Error)
			assert.EqualValues(t, 1, receipts)
			require.NoError(t, db.First(&user, 42).Error)
			if central {
				assert.Zero(t, user.Quota)
				var awards int64
				require.NoError(t, db.Model(&ProductCreditAward{}).Count(&awards).Error)
				assert.EqualValues(t, 1, awards)
			} else {
				assert.Equal(t, 20, user.Quota)
			}
		})
	}
}

func TestUserCheckinLostCommitReplyNeverReportsSuccessOrDuplicatesCredit(t *testing.T) {
	db, _, control := checkinCommitFixture(t, true)
	require.NoError(t, PinNativeFundingRealm(context.Background(), 42, "reviewed-native"))
	control.armed.Store(true)
	receipt, err := UserCheckin(42)
	require.Error(t, err, "an unconfirmed transaction result must not be reported as successful")
	require.Nil(t, receipt)
	require.NoError(t, <-control.results, "the real driver committed before its response was lost")
	requireCheckinPoolClean(t, db)
	var user User
	require.NoError(t, db.First(&user, 42).Error)
	assert.Equal(t, 20, user.Quota, "a durable daily receipt and its credit must belong to the same SQL commit")
	var receipts int64
	require.NoError(t, db.Model(&Checkin{}).Count(&receipts).Error)
	assert.EqualValues(t, 1, receipts)
	_, err = UserCheckin(42)
	require.Error(t, err)
	require.NoError(t, db.First(&user, 42).Error)
	assert.Equal(t, 20, user.Quota, "retrying an uncertain response cannot award the same day twice")
}

func TestUserCheckinSQLiteLocalFundingAccountRetainsOriginalCredit(t *testing.T) {
	db, _ := checkinRealmFixture(t)
	require.NoError(t, db.Create(&ProductFundingAccount{UserID: 42, State: "local"}).Error)
	receipt, err := UserCheckin(42)
	require.NoError(t, err, "an existing local funding account must retain the original SQLite credit backend")
	assert.Equal(t, 20, receipt.QuotaAwarded)
	var user User
	require.NoError(t, db.First(&user, 42).Error)
	assert.Equal(t, 20, user.Quota)
	var awards int64
	require.NoError(t, db.Model(&ProductCreditAward{}).Count(&awards).Error)
	assert.Zero(t, awards)
}
