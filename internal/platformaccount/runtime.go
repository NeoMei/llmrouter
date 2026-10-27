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
	return userkit.NewRuntimeClient(userkit.ServiceConfig{BaseURL: strings.TrimSuffix(os.Getenv("UNIHUB_ORIGIN"), "/"), ProductID: product, InstanceID: os.Getenv("UNIHUB_PRODUCT_INSTANCE_ID"), ServiceToken: os.Getenv("UNIHUB_PRODUCT_RUNTIME_TOKEN")})
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
