package common

import (
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/go-redis/redis/v8"
)

var insecureProductionValues = map[string]struct{}{
	"123456":        {},
	"password":      {},
	"random_string": {},
	"change-me":     {},
	"changeme":      {},
}

func ValidateProductionConfig() error {
	if !strings.EqualFold(strings.TrimSpace(os.Getenv("RUN_MODE")), "production") {
		return nil
	}

	if err := validateSecretEnv("SESSION_SECRET", 32); err != nil {
		return err
	}
	if err := validateSecretEnv("GATEWAY_AES_KEY", 32); err != nil {
		return err
	}
	if err := requireProductionEnv("SQL_DSN"); err != nil {
		return err
	}
	if containsDefaultCredential(os.Getenv("SQL_DSN")) {
		return fmt.Errorf("SQL_DSN contains an insecure example credential")
	}

	redisURL := strings.TrimSpace(os.Getenv("REDIS_CONN_STRING"))
	if redisURL == "" {
		return fmt.Errorf("REDIS_CONN_STRING is required in production")
	}
	redisOptions, err := redis.ParseURL(redisURL)
	if err != nil {
		return fmt.Errorf("REDIS_CONN_STRING is invalid: %w", err)
	}
	if strings.TrimSpace(redisOptions.Password) == "" || isInsecureSecret(redisOptions.Password) {
		return fmt.Errorf("REDIS_CONN_STRING must contain a non-default password")
	}

	if !strings.EqualFold(strings.TrimSpace(os.Getenv("OBJECT_STORE_TYPE")), "s3") {
		return fmt.Errorf("OBJECT_STORE_TYPE must be s3 in production")
	}
	for _, name := range []string{
		"OBJECT_STORE_ENDPOINT",
		"OBJECT_STORE_BUCKET",
		"OBJECT_STORE_REGION",
		"OBJECT_STORE_ACCESS_KEY_ID",
		"OBJECT_STORE_SECRET_ACCESS_KEY",
	} {
		if err := requireProductionEnv(name); err != nil {
			return err
		}
		if isInsecureSecret(os.Getenv(name)) {
			return fmt.Errorf("%s contains a placeholder value", name)
		}
	}
	endpoint, err := url.Parse(strings.TrimSpace(os.Getenv("OBJECT_STORE_ENDPOINT")))
	if err != nil || endpoint.Scheme != "https" || endpoint.Host == "" {
		return fmt.Errorf("OBJECT_STORE_ENDPOINT must be an absolute HTTPS URL")
	}

	if !strings.EqualFold(strings.TrimSpace(os.Getenv("SESSION_COOKIE_SECURE")), "true") {
		return fmt.Errorf("SESSION_COOKIE_SECURE must be true in production")
	}
	trustedURLs := strings.TrimSpace(os.Getenv("SESSION_COOKIE_TRUSTED_URL"))
	if trustedURLs == "" {
		return fmt.Errorf("SESSION_COOKIE_TRUSTED_URL is required in production")
	}
	for _, raw := range strings.Split(trustedURLs, ",") {
		origin, err := NormalizeOrigin(strings.TrimSpace(raw))
		if err != nil || !strings.HasPrefix(origin, "https://") {
			return fmt.Errorf("SESSION_COOKIE_TRUSTED_URL must contain exact HTTPS origins")
		}
	}
	if strings.TrimSpace(os.Getenv("TRUSTED_PROXIES")) == "" {
		return fmt.Errorf("TRUSTED_PROXIES must be explicit in production")
	}
	return nil
}

func validateSecretEnv(name string, minLength int) error {
	value := strings.TrimSpace(os.Getenv(name))
	if len(value) < minLength || isInsecureSecret(value) {
		return fmt.Errorf("%s must be a non-default secret of at least %d bytes", name, minLength)
	}
	return nil
}

func requireProductionEnv(name string) error {
	if strings.TrimSpace(os.Getenv(name)) == "" {
		return fmt.Errorf("%s is required in production", name)
	}
	return nil
}

func isInsecureSecret(value string) bool {
	lower := strings.ToLower(strings.TrimSpace(value))
	if strings.Contains(lower, "replace-with") {
		return true
	}
	_, insecure := insecureProductionValues[lower]
	return insecure
}

func containsDefaultCredential(value string) bool {
	lower := strings.ToLower(strings.TrimSpace(value))
	return strings.Contains(lower, ":123456@") ||
		strings.Contains(lower, ":password@") ||
		strings.Contains(lower, "password=123456")
}
