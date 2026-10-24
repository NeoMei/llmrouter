package model

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/utils/tests"
)

// lockForUpdate must emit FOR UPDATE on databases that support it and skip
// it on SQLite, where the syntax does not exist.
//
// The dummy dialector is used because SQLite drivers strip locking clauses
// from the generated SQL, which would mask what the helper itself does.
func TestLockForUpdateEmitsRowLock(t *testing.T) {
	dummyDB, err := gorm.Open(tests.DummyDialector{}, &gorm.Config{DryRun: true})
	require.NoError(t, err)
	buildSQL := func() string {
		var rows []Redemption
		return lockForUpdate(dummyDB).Where("id = ?", 1).Find(&rows).Statement.SQL.String()
	}

	t.Cleanup(func() {
		common.SetDatabaseTypes(common.DatabaseTypeSQLite, common.DatabaseTypeSQLite)
	})

	common.SetDatabaseTypes(common.DatabaseTypeMySQL, common.DatabaseTypeSQLite)
	assert.Contains(t, buildSQL(), "FOR UPDATE")

	common.SetDatabaseTypes(common.DatabaseTypePostgreSQL, common.DatabaseTypeSQLite)
	assert.Contains(t, buildSQL(), "FOR UPDATE")

	common.SetDatabaseTypes(common.DatabaseTypeSQLite, common.DatabaseTypeSQLite)
	assert.NotContains(t, buildSQL(), "FOR UPDATE")
}

// An explicit repository DB must keep correct locking even when application
// globals describe another database (as in migrations or independent fixtures).
func TestLockForUpdateUsesExplicitDatabase(t *testing.T) {
	previous := common.MainDatabaseType()
	t.Cleanup(func() { common.SetMainDatabaseType(previous) })
	common.SetMainDatabaseType(common.DatabaseTypeSQLite)
	for _, dialect := range []gorm.Dialector{
		mysql.New(mysql.Config{DSN: "root@tcp(127.0.0.1:1)/fixture", SkipInitializeWithVersion: true}),
		postgres.New(postgres.Config{DSN: "host=127.0.0.1 port=1 dbname=fixture", PreferSimpleProtocol: true}),
	} {
		t.Run(dialect.Name(), func(t *testing.T) {
			db, err := gorm.Open(dialect, &gorm.Config{DryRun: true, DisableAutomaticPing: true})
			require.NoError(t, err)
			var rows []Redemption
			query := lockForUpdate(db).Where("id = ?", 1).Find(&rows)
			assert.Contains(t, query.Statement.SQL.String(), "FOR UPDATE")
		})
	}
}
