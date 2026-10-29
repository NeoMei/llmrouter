package router

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNativeRealmIntegrationRejectsConnectionOverrides(t *testing.T) {
	valid := "postgres://fixture:synthetic@127.0.0.1:15449/native_realm_test_fixture?sslmode=disable"
	require.NoError(t, validateNativeRealmPostgresURL(valid))
	for _, dsn := range []string{
		valid + "&host=192.0.2.1",
		valid + "&database=other",
		valid + "&dbname=other",
		valid + "&service=remote",
		valid + "&hostaddr=192.0.2.1",
		"postgres://fixture:synthetic@192.0.2.1/native_realm_test_fixture?sslmode=disable",
		"postgres://fixture:synthetic@127.0.0.1/production?sslmode=disable",
	} {
		require.Error(t, validateNativeRealmPostgresURL(dsn))
	}
}
