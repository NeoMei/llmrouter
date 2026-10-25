package platformaccount

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestProofRequiresExactProductAndImmutableID(t *testing.T) {
	for _, tc := range []struct {
		name, kind, product, client, id string
		valid                           bool
	}{
		{"valid", "product", "llmrouter", "client", "immutable", true},
		{"console", "console", "llmrouter", "client", "immutable", false},
		{"legacy", "legacy_product", "llmrouter", "client", "immutable", false},
		{"wrong_client", "product", "llmrouter", "other", "immutable", false},
		{"wrong_product", "product", "agentwiki", "client", "immutable", false},
		{"missing_id", "product", "llmrouter", "client", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, "Bearer proof", r.Header.Get("Authorization"))
				assert.Equal(t, "https://app.test", r.Header.Get("Origin"))
				switch r.URL.Path {
				case "/v1/capabilities":
					_, _ = w.Write([]byte(`{"kind":"` + tc.kind + `","product_id":"` + tc.product + `","client_id":"` + tc.client + `"}`))
				case "/v1/me":
					_, _ = w.Write([]byte(`{"id":"` + tc.id + `","email":"same@example.test"}`))
				case "/v1/accounts":
					_, _ = w.Write([]byte(`[]`))
				}
			}))
			defer server.Close()
			identity, err := (Config{Enabled: true, APIOrigin: server.URL, PublicOrigin: server.URL, ClientID: "client", RedirectURI: "https://app.test/unihub"}).Prove(context.Background(), "proof")
			if tc.valid {
				require.NoError(t, err)
				assert.Equal(t, tc.id, identity.User.ID)
			} else {
				require.Error(t, err)
			}
		})
	}
}
func TestConfigRejectsUntrustedURLs(t *testing.T) {
	for _, raw := range []string{"http://remote.test", "https://user:pass@example.test", "https://example.test/path", "https://example.test?q=x"} {
		t.Run(raw, func(t *testing.T) {
			t.Setenv("UNIHUB_PRODUCT_ENABLED", "true")
			t.Setenv("UNIHUB_PRODUCT_PUBLIC_ORIGIN", raw)
			t.Setenv("UNIHUB_PRODUCT_API_ORIGIN", raw)
			t.Setenv("UNIHUB_PRODUCT_CLIENT_ID", "client")
			t.Setenv("UNIHUB_PRODUCT_REDIRECT_URI", "https://app.test/unihub")
			_, err := FromEnv()
			require.Error(t, err)
		})
	}
}
func TestProofRejectsRedirect(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "https://example.test", 302) }))
	defer server.Close()
	_, err := (Config{APIOrigin: server.URL, ClientID: "client"}).Prove(context.Background(), "proof")
	require.ErrorContains(t, err, "proof")
}
func TestProofRejectsMalformedMissingAndExpiredServiceProofs(t *testing.T) {
	for _, path := range []string{"/v1/capabilities", "/v1/me", "/v1/accounts"} {
		for _, mode := range []string{"expired", "malformed", "oversize"} {
			t.Run(path+mode, func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == path {
						switch mode {
						case "expired":
							w.WriteHeader(401)
						case "malformed":
							_, _ = w.Write([]byte("invalid"))
						case "oversize":
							_, _ = w.Write([]byte(strings.Repeat("x", 1048577)))
						}
						return
					}
					switch r.URL.Path {
					case "/v1/capabilities":
						_, _ = w.Write([]byte(`{"kind":"product","product_id":"llmrouter","client_id":"client"}`))
					case "/v1/me":
						_, _ = w.Write([]byte(`{"id":"immutable"}`))
					case "/v1/accounts":
						_, _ = w.Write([]byte(`[]`))
					}
				}))
				defer server.Close()
				_, err := (Config{APIOrigin: server.URL, ClientID: "client", RedirectURI: server.URL + "/unihub"}).Prove(context.Background(), "proof")
				require.Error(t, err)
			})
		}
	}
}
func TestImmutableIdentityDoesNotMergeSameEmail(t *testing.T) {
	for _, id := range []string{"first-immutable", "second-immutable"} {
		t.Run(id, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/v1/capabilities":
					_, _ = w.Write([]byte(`{"kind":"product","product_id":"llmrouter","client_id":"client"}`))
				case "/v1/me":
					_, _ = w.Write([]byte(`{"id":"` + id + `","email":"shared@test","role":"root"}`))
				case "/v1/accounts":
					_, _ = w.Write([]byte(`[]`))
				}
			}))
			defer server.Close()
			identity, err := (Config{APIOrigin: server.URL, PublicOrigin: "https://fixed-issuer.test", ClientID: "client", RedirectURI: "https://app.test/unihub"}).Prove(context.Background(), "proof")
			require.NoError(t, err)
			assert.Equal(t, id, identity.User.ID)
			assert.Equal(t, "https://fixed-issuer.test", identity.Issuer)
		})
	}
}

func TestConfigRejectsNonArrayOrInvalidModelIdentifiers(t *testing.T) {
	t.Setenv("UNIHUB_PRODUCT_ENABLED", "true")
	t.Setenv("UNIHUB_PRODUCT_PUBLIC_ORIGIN", "https://hub.test")
	t.Setenv("UNIHUB_PRODUCT_API_ORIGIN", "https://hub.test")
	t.Setenv("UNIHUB_PRODUCT_CLIENT_ID", "client")
	t.Setenv("UNIHUB_PRODUCT_REDIRECT_URI", "https://app.test/unihub")
	for _, raw := range []string{`null`, `{}`, `[1]`, `[""]`, `[" padded "]`} {
		t.Setenv("UNIHUB_PRODUCT_MODELS", raw)
		_, err := FromEnv()
		require.Error(t, err, raw)
	}
	t.Setenv("UNIHUB_PRODUCT_MODELS", `["allowed-model"]`)
	config, err := FromEnv()
	require.NoError(t, err)
	assert.Equal(t, []string{"allowed-model"}, config.Models)
}
