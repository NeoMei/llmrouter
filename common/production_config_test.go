package common

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func setValidProductionEnvironment(t *testing.T) {
	t.Helper()
	t.Setenv("RUN_MODE", "production")
	t.Setenv("SESSION_SECRET", "session-secret-that-is-at-least-32-bytes")
	t.Setenv("GATEWAY_AES_KEY", "gateway-key-that-is-at-least-32-bytes")
	t.Setenv("SQL_DSN", "gateway:strong-db-password@tcp(db:3306)/gateway")
	t.Setenv("REDIS_CONN_STRING", "redis://default:strong-redis-password@redis:6379/0")
	t.Setenv("OBJECT_STORE_TYPE", "s3")
	t.Setenv("OBJECT_STORE_ENDPOINT", "https://objects.example.com")
	t.Setenv("OBJECT_STORE_BUCKET", "gateway-files")
	t.Setenv("OBJECT_STORE_REGION", "us-east-1")
	t.Setenv("OBJECT_STORE_ACCESS_KEY_ID", "gateway-access-key")
	t.Setenv("OBJECT_STORE_SECRET_ACCESS_KEY", "gateway-secret-key")
	t.Setenv("SESSION_COOKIE_SECURE", "true")
	t.Setenv("SESSION_COOKIE_TRUSTED_URL", "https://gateway.example.com")
	t.Setenv("TRUSTED_PROXIES", "10.0.0.10/32")
}

func TestValidateProductionConfigRequiresStableSecrets(t *testing.T) {
	setValidProductionEnvironment(t)
	t.Setenv("SESSION_SECRET", "")
	require.ErrorContains(t, ValidateProductionConfig(), "SESSION_SECRET")

	setValidProductionEnvironment(t)
	t.Setenv("GATEWAY_AES_KEY", "123456")
	require.ErrorContains(t, ValidateProductionConfig(), "GATEWAY_AES_KEY")
}

func TestValidateProductionConfigRequiresDependenciesAndSecurity(t *testing.T) {
	setValidProductionEnvironment(t)
	t.Setenv("REDIS_CONN_STRING", "")
	require.ErrorContains(t, ValidateProductionConfig(), "REDIS_CONN_STRING")

	setValidProductionEnvironment(t)
	t.Setenv("SESSION_COOKIE_SECURE", "false")
	require.ErrorContains(t, ValidateProductionConfig(), "SESSION_COOKIE_SECURE")

	setValidProductionEnvironment(t)
	t.Setenv("TRUSTED_PROXIES", "")
	require.ErrorContains(t, ValidateProductionConfig(), "TRUSTED_PROXIES")
}

func TestValidateProductionConfigAcceptsExplicitSecureConfiguration(t *testing.T) {
	setValidProductionEnvironment(t)
	require.NoError(t, ValidateProductionConfig())
}

func TestValidateProductionConfigSkipsDevelopment(t *testing.T) {
	t.Setenv("RUN_MODE", "development")
	require.NoError(t, ValidateProductionConfig())
}
