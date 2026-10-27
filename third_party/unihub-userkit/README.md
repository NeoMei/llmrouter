# UniHub product user kit (source contract v0.1.0)

Independent Go module (Go 1.23+):
`github.com/NeoMei/UniHub/packages/userkit-go`. It imports no UniHub internals and
contains no billing, password storage or OIDC password implementation. Browser signature/discovery validation uses coreos/go-oidc and x/oauth2 (Go 1.23).
This task distributes source only; no module release/tag has been published.
A future repository tag must use `packages/userkit-go/v0.x.y`. Pin an immutable
commit or verified source snapshot until then. The repository has no declared
license; do not infer redistribution permission or invent a license.

```go
func NewClient(Config) (*Client, error)
type Config struct {
    BaseURL, Issuer, ClientID, ProductID, InstanceID string
    HTTPClient *http.Client
}
type Provider interface {
    Resolve(context.Context, string) (Access, error)
}
type Provisioner interface {
    Ensure(context.Context, Access) (LocalUser, error)
}
type LocalUser struct { ID string }
type ResourcePolicy interface {
    Allow(context.Context, Principal, string, string) error
}
func NewService(Provider, Provisioner, ResourcePolicy) (*Service, error)
func (*Service) Authenticate(context.Context, string) (Principal, error)
type Principal struct { LocalUser LocalUser; Access Access /* internal policy */ }
func (Principal) HasPermission(string) bool
func (Principal) RequirePermission(context.Context, string, string) error
```

`Access` JSON fields are `issuer`, `subject`, `product_id`, `instance_id`,
`user:{id,name,email}`, `roles`, `permissions`, `version`, `active`, and
`expires_at` (int64 Unix seconds). Go field names are `Issuer`, `Subject`,
`ProductID`, `InstanceID`, `User`, `Roles`, `Permissions`, `Version`, `Active`,
`ExpiresAt`. `User.ID == Subject` is the immutable central user UUID, never email.
`ErrDenied` rejects central deny/expiry; `ErrInvalid` rejects malformed identities.

`BaseURL` is a backend-only central API origin; `Issuer` is UniHub's canonical
public origin. Both require HTTPS except loopback development. `Client.Resolve`
uses the same bearer for capabilities and product access, checks exact configured
product/client/instance (`default` when omitted), requires `access:read`, bounds
each response to 1 MiB, uses a 10s default / 30s maximum total timeout, and rejects
all redirects. It validates current central active status, stable keys and verified
future expiry on each call. There is no allow cache. Never pass browser-supplied
roles, unsigned token claims, native sessions, model credentials or PATs as proof.

Provisioners own their product database and **must** atomically ensure a stable
local user and initialize product resources in one transaction under an
issuer+subject unique constraint/lock. Repeated and concurrent `Ensure` calls
must return the same local user; initializer failure rolls back both identity and
resources. `Service.Authenticate` invokes that same Ensure path each time and
never returns a principal on failure. Keep the central identity projection at the
native common role with product-specific baseline resources (e.g. zero quota).
Never persist central admin roles as native elevated roles. Local resource policy
can narrow a central permission; it cannot grant a permission central denied.
`HasPermission` evaluates policy with an empty resource and background context;
use `RequirePermission(ctx, permission, resource)` for resource-specific checks.
Retain the returned principal only for the current request and refresh online
before protected operations. Expiry is checked again by permission helpers.

Standalone products implement their own **verified** `Provider` and the same
transactional `Provisioner`, using their established identity/password/OIDC
implementation. This package does not authenticate raw passwords. The existing
UniHub normal registration path remains `/v1/identity/registrations` for explicitly
enabled clients; registration cannot create a configured user administrator.

Runnable local consumer using a verified bearer (no real supplier or billing calls):

```sh
UNIHUB_API=http://127.0.0.1:8088 UNIHUB_ISSUER=http://127.0.0.1:8088 \
  PRODUCT_ID=notes CLIENT_ID=notes-client PRODUCT_BEARER='<local-test-bearer>' \
  go run ./examples/minimal
```

The minimal in-memory provisioner illustrates an atomic boundary for one process;
production products must replace it with a durable database transaction.
`go test -race ./...` tests this nested module separately from the repository root.
Upgrade the kit and central wire contract together; rerun client-boundary and
provisioning tests when replacing a deterministic vendored snapshot.

## Browser authorization code / PKCE

```go
type BrowserConfig struct {
    Issuer, ClientID, ClientSecret, RedirectURI, AccessTokenType string
    Scopes []string
    HTTPClient *http.Client
}
func NewBrowser(context.Context, BrowserConfig) (*Browser, error)
func (*Browser) Begin(browserBinding, returnTo string) (authorizationURL string, err error)
func (*Browser) Complete(ctx context.Context, browserBinding, absoluteCallbackURL string) (BrowserIdentity, error)
type BrowserIdentity struct {
    Subject, AccessToken, ReturnTo string
    ExpiresAt int64
}
func SafeReturn(string) bool
```

Create one Browser on the server with the exact registered callback; keep it for
all requests. Set an unpredictable HttpOnly Secure SameSite=Lax cookie on login
and pass it as browserBinding in Begin/Complete. Never use an identity supplied
by the browser. Complete requires the exact callback origin/path and one state
and code. State/nonce/S256 verifier are browser-bound, expire in five minutes,
and are consumed once, including failed callbacks. The in-process transaction
store is capped at 4096; use sticky login/callback routing across replicas.
Restart invalidates pending logins safely. HTTP is loopback-only, redirects and
code-exchange automatic retries are rejected, operations have bounded timeout.
Both ID and access tokens are signature/issuer/one-client/expiry validated,
nonce binds the ID token, and subjects must agree. For Casdoor configure
AccessTokenType="access-token" to reject other signed token purposes. This
helper requires signed access tokens; opaque-token issuers need a separately
verified provider. BrowserIdentity.Subject is the IdP subject, distinct from
Access.Subject's immutable central UUID. Resolve the returned AccessToken with
the shared Provider before provisioning, then cap the local session to the
minimum of both verified expiries. No returned bearer may enter frontend JSON,
URLs or logs; store it authenticated-encrypted on the server per product SID.
