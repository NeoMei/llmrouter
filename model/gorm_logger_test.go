package model

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2/lib/proto"
	"github.com/QuantumNous/new-api/common"
	sqlitedriver "github.com/glebarez/go-sqlite"
	"github.com/glebarez/sqlite"
	"github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func preserveGormLoggerGlobals(t *testing.T) {
	t.Helper()
	previousDebug := common.DebugEnabled
	previousThreshold, hadThreshold := os.LookupEnv("SQL_SLOW_THRESHOLD_MS")
	t.Cleanup(func() {
		common.DebugEnabled = previousDebug
		if hadThreshold {
			require.NoError(t, os.Setenv("SQL_SLOW_THRESHOLD_MS", previousThreshold))
		} else {
			require.NoError(t, os.Unsetenv("SQL_SLOW_THRESHOLD_MS"))
		}
	})
}

func sqliteErrorContainingSecret(t *testing.T) error {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Discard})
	require.NoError(t, err)
	err = db.Exec(`SELECT * FROM "super-secret"`).Error
	require.Error(t, err)
	var sqliteErr *sqlitedriver.Error
	require.True(t, errors.As(err, &sqliteErr))
	require.Contains(t, err.Error(), "super-secret")
	return err
}

func TestSanitizeDBErrorRedactsDriverMessage(t *testing.T) {
	tests := []struct {
		name string
		err  func(*testing.T) error
		want string
	}{
		{
			name: "mysql",
			err: func(*testing.T) error {
				return &mysql.MySQLError{Number: 1062, Message: "duplicate super-secret"}
			},
			want: "mysql error 1062",
		},
		{
			name: "postgres",
			err: func(*testing.T) error {
				return &pgconn.PgError{Code: "23505", Detail: "super-secret"}
			},
			want: "postgres error SQLSTATE 23505",
		},
		{
			name: "clickhouse",
			err: func(*testing.T) error {
				return &proto.Exception{Code: 241, Message: "super-secret"}
			},
			want: "clickhouse error 241",
		},
		{
			name: "sqlite",
			err:  sqliteErrorContainingSecret,
			want: "sqlite error 1",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := sanitizeDBError(tc.err(t)).Error()
			assert.Equal(t, tc.want, got)
			assert.NotContains(t, got, "super-secret")
		})
	}
}

func TestSanitizeDBErrorRedactsWrappedDriverMessage(t *testing.T) {
	err := fmt.Errorf("query failed: %w", &mysql.MySQLError{Number: 1064, Message: "near super-secret"})
	assert.EqualError(t, sanitizeDBError(err), "mysql error 1064")
}

func TestSanitizeDBErrorKeepsNonDriverErrors(t *testing.T) {
	err := errors.New("dial tcp 127.0.0.1:3306: connect: connection refused")
	assert.Equal(t, err, sanitizeDBError(err))
}

func TestGormLoggerParameterizesSQLAndSanitizesDriverErrors(t *testing.T) {
	preserveGormLoggerGlobals(t)
	common.DebugEnabled = false
	require.NoError(t, os.Unsetenv("SQL_SLOW_THRESHOLD_MS"))

	var output bytes.Buffer
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: newGormLogger(&output)})
	require.NoError(t, err)
	require.Error(t, db.Exec("SELECT * FROM missing_table WHERE secret = ?", "super-secret").Error)

	logged := output.String()
	assert.Contains(t, logged, "missing_table")
	assert.Contains(t, logged, "secret = ?")
	assert.Contains(t, logged, "sqlite error 1")
	assert.NotContains(t, logged, "super-secret")
	assert.NotContains(t, logged, "no such table")
}

func TestGormLoggerDebugModeStillRedactsDiagnosticValues(t *testing.T) {
	preserveGormLoggerGlobals(t)
	common.DebugEnabled = true

	var output bytes.Buffer
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: newGormLogger(&output)})
	require.NoError(t, err)
	require.Error(t, db.Exec("SELECT * FROM missing_table WHERE secret = ?", "super-secret").Error)

	logged := output.String()
	assert.Contains(t, logged, "secret = ?")
	assert.Contains(t, logged, "sqlite error 1")
	assert.NotContains(t, logged, "super-secret")
	assert.NotContains(t, logged, "no such table")
}

func TestNewGormConfigSetsPrepareStatementPolicy(t *testing.T) {
	preserveGormLoggerGlobals(t)

	assert.True(t, newGormConfig(true).PrepareStmt)
	assert.False(t, newGormConfig(false).PrepareStmt)
	assert.NotNil(t, newGormConfig(true).Logger)
}

func TestSlowThresholdEnvironmentBoundaries(t *testing.T) {
	preserveGormLoggerGlobals(t)
	common.DebugEnabled = false

	tests := []struct {
		name        string
		value       string
		elapsed     time.Duration
		wantSlowLog bool
	}{
		{name: "unset uses default", elapsed: 201 * time.Millisecond, wantSlowLog: true},
		{name: "zero disables", value: "0", elapsed: time.Hour + time.Millisecond},
		{name: "maximum is accepted below boundary", value: "3600000", elapsed: 201 * time.Millisecond},
		{name: "maximum is accepted above boundary", value: "3600000", elapsed: time.Hour + time.Millisecond, wantSlowLog: true},
		{name: "negative falls back", value: "-1", elapsed: 201 * time.Millisecond, wantSlowLog: true},
		{name: "above maximum falls back", value: "3600001", elapsed: 201 * time.Millisecond, wantSlowLog: true},
		{name: "non integer falls back", value: "invalid", elapsed: 201 * time.Millisecond, wantSlowLog: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.value == "" {
				require.NoError(t, os.Unsetenv("SQL_SLOW_THRESHOLD_MS"))
			} else {
				require.NoError(t, os.Setenv("SQL_SLOW_THRESHOLD_MS", tc.value))
			}
			var output bytes.Buffer
			gormLogger := newGormLogger(&output)
			gormLogger.Trace(context.Background(), time.Now().Add(-tc.elapsed), func() (string, int64) {
				return "SELECT ?", 1
			}, nil)
			assert.Equal(t, tc.wantSlowLog, bytes.Contains(output.Bytes(), []byte("SLOW SQL")))
		})
	}
}
