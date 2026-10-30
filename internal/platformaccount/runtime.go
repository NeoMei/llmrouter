package platformaccount

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	userkit "github.com/NeoMei/UniHub/packages/userkit-go"
	"os"
	"strings"
	"time"
)

func RuntimeEnabled() bool { return os.Getenv("UNIHUB_PRODUCT_RUNTIME_ENABLED") == "true" }

// FinanceEnabled is a separate deployment gate; a runtime identity alone
// does not authorize access to financial operations.
func FinanceEnabled() bool {
	return RuntimeEnabled() && os.Getenv("UNIHUB_PRODUCT_FINANCE_ENABLED") == "true"
}

func FinanceClient(config Config) (*userkit.ProductFinanceClient, error) {
	if !FinanceEnabled() {
		return nil, errors.New("central product finance disabled")
	}
	product := os.Getenv("UNIHUB_PRODUCT_ID")
	if product == "" {
		product = "llmrouter"
	}
	origin := os.Getenv("UNIHUB_PRODUCT_API_ORIGIN")
	if origin == "" {
		origin = os.Getenv("UNIHUB_ORIGIN")
	}
	instance := os.Getenv("UNIHUB_PRODUCT_INSTANCE_ID")
	if instance == "" {
		instance = "default"
	}
	if strings.TrimRight(origin, "/") != strings.TrimRight(config.APIOrigin, "/") || product != config.ProductID || instance != config.InstanceID {
		return nil, errors.New("central product finance scope mismatch")
	}
	return userkit.NewProductFinanceClient(userkit.ProductFinanceConfig{BaseURL: origin, ProductID: product, InstanceID: instance, ServiceToken: os.Getenv("UNIHUB_PRODUCT_RUNTIME_TOKEN")})
}

// RuntimeClient is shared by identity and financial adapters. Product and
// instance are fixed deployment settings, never selected from request bodies.
func RuntimeClient() (*userkit.RuntimeClient, error) {
	if !RuntimeEnabled() {
		return nil, errors.New("central product runtime disabled")
	}
	product := os.Getenv("UNIHUB_PRODUCT_ID")
	if product == "" {
		product = "llmrouter"
	}
	apiOrigin := os.Getenv("UNIHUB_PRODUCT_API_ORIGIN")
	if apiOrigin == "" {
		apiOrigin = os.Getenv("UNIHUB_ORIGIN")
	}
	browserOrigin := os.Getenv("UNIHUB_PRODUCT_PUBLIC_ORIGIN")
	if browserOrigin == "" {
		browserOrigin = os.Getenv("UNIHUB_ORIGIN")
	}
	return userkit.NewRuntimeClient(userkit.ServiceConfig{BaseURL: strings.TrimSuffix(apiOrigin, "/"), BrowserOrigin: browserOrigin, ProductID: product, InstanceID: os.Getenv("UNIHUB_PRODUCT_INSTANCE_ID"), ServiceToken: os.Getenv("UNIHUB_PRODUCT_RUNTIME_TOKEN")})
}
func IdentityForLocal(ctx context.Context, localID string) (userkit.ProductIdentity, error) {
	c, e := RuntimeClient()
	if e != nil {
		return userkit.ProductIdentity{}, e
	}
	return c.ProductIdentity(ctx, localID)
}
func AuthenticateLocal(ctx context.Context, localID string, authVersion int64, method string) (userkit.ProductIdentity, error) {
	c, e := RuntimeClient()
	if e != nil {
		return userkit.ProductIdentity{}, e
	}
	var b [24]byte
	if _, e = rand.Read(b[:]); e != nil {
		return userkit.ProductIdentity{}, e
	}
	return c.AuthenticateProductIdentity(ctx, userkit.ProductAuthentication{LocalUserID: localID, AuthenticationID: hex.EncodeToString(b[:]), Method: method, AuthenticatedAt: time.Now().Unix(), AuthVersion: authVersion})
}
