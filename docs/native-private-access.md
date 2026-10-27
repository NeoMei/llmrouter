# Native management boundary

Explicitly configure all three policy flags. Missing or malformed policy denies native routes. An existing installation that retains native accounts and API keys can set `NATIVE_PUBLIC_ACCESS=true` alongside `UNIHUB_PRODUCT_ENABLED=true` and `UNIHUB_PLATFORM_ENABLED=true`. Native login, keys, protocols, balances and history continue through their existing authentication and billing checks; unified users keep UniHub identity and central billing. The two sets of identities, keys and balances are not automatically merged.

Use `NATIVE_PUBLIC_ACCESS=false` for a new isolated UniHub deployment, or when native access is deliberately restricted to private management. This blocks public native APIs while preserving the unified application, including direct GET/HEAD `/keys` and SSO return to `/keys`. Serving that page grants no native token or billing API access; its unified key actions use the authenticated UniHub BFF. With both UniHub flags false, `NATIVE_PUBLIC_ACCESS=true` continues to support standalone native operation.

For UniHub deployments set `NATIVE_ADMIN_LISTEN_ADDRESS=127.0.0.1:3001` (or an explicit IPv6 loopback address and port). Startup validates and binds this separate listener; it shares the application handler and sessions. The private port must differ from the public port. Bind failure stops startup. Do not point any public reverse proxy at the private port. Access it through an authenticated SSH tunnel, e.g. local port 3001 to remote 127.0.0.1:3001, and log in with native credentials. Native route authentication and privileges still apply.

The boundary trusts only the socket address supplied by net/http. Origin, Referer, forwarded headers, query parameters, and MANAGEMENT_TOKEN do not grant native access. MANAGEMENT_TOKEN retains its separate readiness/metrics purpose and must never be sent to the product browser. Old NATIVE_ADMIN_ORIGIN/NATIVE_ADMIN_PROXY settings have no authority.

Signed payment callbacks remain registered publicly so existing orders can settle through their existing signature checks. The boundary does not establish callback signature validity.

The October 27 native-denial acceptance describes the previous policy and remains historical evidence. The [October 28 coexistence contract](superpowers/specs/2026-10-28-nonbreaking-unified-users-design.md) supersedes its blanket-denial requirement.
