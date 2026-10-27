# Product SSO and current product access

LLMRouter consumes the unpublished UniHub Go user kit through the exact checked-in
`third_party/unihub-userkit` source snapshot. `PROVENANCE.json` pins the source
commit and sorted SHA256 hashes. The relative module replacement builds from a
checkout alone. The upstream repository declares no license; this snapshot records
workspace-authorized reuse and makes no licensing/publication claim.

Verify with `python3 scripts/sync-unihub-userkit.py --check`. For an approved kit
update, commit its module sources in UniHub, run the sync script with
`--source <UniHub checkout>`, verify with `--check --source <UniHub checkout>`,
then run both nested-module and LLMRouter regressions. Never use an absolute
sibling replace in go.mod.

## Required setup

Use `docs/integrations/unihub.env.example` and supply these backend-only values:

- `UNIHUB_PRODUCT_ENABLED=true`, `UNIHUB_PLATFORM_ENABLED=true`,
  `NATIVE_PUBLIC_ACCESS=false` for the public product service.
- `UNIHUB_PRODUCT_PUBLIC_ORIGIN`: canonical UniHub business identity origin,
  used to verify Access.issuer. `UNIHUB_PRODUCT_API_ORIGIN`: fixed reachable
  central API origin; it may differ from the public origin.
- `UNIHUB_PRODUCT_OIDC_ISSUER`: actual IdP discovery issuer, distinct from the
  business origin (e.g. loopback Casdoor `http://localhost:58000` with UniHub API
  `http://127.0.0.1:58118`). Both ID and access tokens must be RS256 JWTs for the
  configured client; Casdoor access tokens require tokenType=access-token.
- `UNIHUB_PRODUCT_CLIENT_ID` and optional backend-only
  `UNIHUB_PRODUCT_CLIENT_SECRET` for the exact OIDC client.
- `UNIHUB_PRODUCT_REDIRECT_URI`: exact registered product callback,
  `https://router.example/api/unihub/callback`; the reverse proxy preserves Host.
  The callback is fixed at `/api/unihub/callback`, never `/unihub`.
- `UNIHUB_PRODUCT_INSTANCE_ID=default` (or the exact registered instance).
- A random, persistent `SESSION_SECRET` of at least 32 bytes. Rotation invalidates
  encrypted upstream proofs; users must sign in again. No bearer is logged or
  returned in the product API. Keep refresh cookies Secure in HTTPS deployments
  and explicitly configure trusted public origins when terminating TLS upstream.

Register a dedicated product client through UniHub's explicit prepare/apply/readback
operator workflow. It must authorize product_id=llmrouter, the exact instance,
`access:read`, permitted browser origin and exact callback, plus only the existing
business scopes needed for account/finance UI. Enable browser SSO/shared sign-in
on that IdP application (Casdoor enableSigninSession and enableAutoSignin).
Browser SSO does not require enabling the headless password API. Registration
remains available only when separately enabled for that product client.

Configure a trusted `PRODUCT_USER_MANIFESTS_FILE` in UniHub with product/instance,
entry URL and explicit permissions. A normal default member can have product:use;
privileged channel viewer/operator roles must never be defaults. A viewer needs
channel:read; an operator can additionally receive channel:operate/channel:write.
Central access grants never change the stored local common role or zero quota.
`USER_ADMIN_IDS` holds central immutable UUIDs for user administrators independently
of billing operators. Never use an IdP subject, email, native numeric role or
browser-supplied role as an administrator bootstrap.

## Request/session contracts

`GET /api/unihub/login?return_to=/channels` starts browser-bound authorization
code + S256 PKCE. The server validates discovery, signature, one-client audience,
issuer, ID nonce, matching IdP subjects and expiries before resolving central
product access. The central business UUID is distinct from the IdP subject.
Bounded one-time transactions live for five minutes in-process; use sticky
login/callback routing with replicas. Code exchange has no automatic retries.
OIDC discovery initialization has a ten-second deadline. Concurrent logins share
the in-flight attempt and a canceled request can stop waiting. A failed discovery
returns the explicit SSO failure view; a later Sign in attempt retries against
the same issuer without a process restart. A successful Browser is retained for
the process lifetime, including its existing one-time callback transactions.

The server JIT provisioner preserves existing transactional issuer+central UUID
projection invariants. A native UniHub dashboard SID has an AES-GCM encrypted
upstream bearer, with a domain-separated server key and associated data containing
product, instance, issuer, central UUID, local ID, SID and expiry. The native session,
refresh cookie and dashboard access token expire no later than the verified proof.
Old unbound native projections require sign-in. Expired/revoked proof rows are
purged by hourly auth cleanup; expiry is enforced before decryption independently.

`GET /api/unihub/access` requires a live UniHub dashboard session and returns
`{access, permissions:{admin_permissions}, csrf_token}`. It never returns the
upstream identity bearer. Every management/BFF call checks central state online;
revocation, disable, missing binding, ciphertext swap and central failure deny with
no native role fallback. Logout remains available after revocation. Local sign-out
leaves the central IdP session intact and lands in an explicit signed-out view;
re-entry requires clicking Sign in with UniHub.

`/api/unihub/bff/v1/...` forwards only the exact known account/team/resource/finance
SDK route+method whitelist to the configured API origin. It replaces authorization
with the encrypted server-side bearer, forwards only required content/idempotency
headers and a fixed product Origin. Writes require exact product Origin and the
SID-bound X-UniHub-CSRF value returned by /access. Duplicate sensitive headers,
identity/admin/token exchange routes, encoded path separators and unknown methods
are rejected. Request bodies are bounded to 1 MiB, responses to 4 MiB, total timeout
to 10s, redirects are rejected, and financial writes are never retried. Model
credentials returned for an explicitly requested model task remain purpose-bound
model credentials, distinct from the identity bearer.

The frontend restores its native session via existing refresh-cookie machinery,
hydrates current permissions, then restores accounts through the BFF. It chooses
the lexicographically first personal account only for an empty initial selection;
an existing selection or pending financial intent is preserved. Navigation and
focus/60s visibility refresh hide stale permissions; backend authorization is
immediate on every request. Failed proof stops automatic login; retry is explicit.

## Native management boundary

The channel router's exact method/path/permission table also registers the UniHub
allowlist. A GET channel test or balance refresh requires channel:operate; it is
not classified as a read. Product channel list/tag/search/detail and update responses use an explicit safe
DTO. Opaque config, credential-bearing URLs/overrides, remarks and provider error
metadata are never returned to product sessions. A configuration_redacted marker
makes the editor send only explicitly changed fields; object setting/settings
updates merge their changed keys server-side so unseen credentials survive normal
routing edits. Native operators keep the full existing configuration contract.
Sensitive fields use the same current-request CanRequest
helper in controllers and middleware. Codex usage/reset-credit GETs cannot refresh/persist credentials for a product
reader without current channel:sensitive_write; the explicitly protected refresh
endpoint and standalone native behavior remain available. Channel secrets still require native root
and secure verification and are never opened to projected users. Actual local
user ID/username and stored common role remain in audit records.

The unified identity uses the standard LLMRouter application layout, sidebar and
route outlet. `/dashboard/overview`, model/flow analytics and self-scoped usage
logs render actual product components; `/unihub` is the separate account entry
with a return to the console. Identity, account/team and wallet/billing screens
stay in UniHub; product navigation does not substitute local passwords, opaque
PATs, local quota or native billing for those central services.

Member reads require a live product session and current `product:use`. Self data,
flow and logs remain scoped to the authenticated local projection; query fields
cannot select another user's records. Technical global logs and aggregate views
require `log:read`, metadata administration uses `catalog:read/write`, and client
model routing uses `routing:read/write`. Existing channel-only assignments retain
their prior channel behavior. Runtime settings use `settings:read/write` with an
explicit server field allowlist; identity, payment, secret and deployment options
are excluded rather than returned as editable default values.

UniHub's identity manifest version 2 adds explicitly assigned privileged
`product_viewer` and `product_admin` role presets. The default remains `member`;
no existing user automatically receives either product role. Permissions resolve
online for each request, without changing stored native roles or quota. Native
identity/PAT/finance mutations, secret-key views, platform/relay credentials and
external provisioning are excluded. Unknown API paths never become the SPA.

Standalone native mode requires both UniHub flags false and explicit
NATIVE_PUBLIC_ACCESS=true. Existing native role policies and the private operator
listener keep their established authorization; public product mode never claims
native_access=true based on a role or header.

## Verification boundary

Local automated tests use signed OIDC HTTP fixtures, live local HTTP central proof
fixtures and real isolated product databases. They are separate from real Casdoor
browser acceptance, production grants, upstream supplier requests, payments and
deployment. Existing financial idempotency/payer/UNKNOWN UI regressions remain in
the configured Playwright suite, and registration is retained as an explicit UI.
Per-product blocks now gate manifest-managed access:read product business bearers
centrally as well as through LLMRouter. Existing model-purpose credentials do not
carry instance scope; their historical receipt/recovery and global-disable contract
is preserved rather than claiming new per-instance model eligibility semantics.
