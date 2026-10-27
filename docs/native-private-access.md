# Native management boundary

Explicitly configure all three policy flags. Missing or malformed policy denies native routes. UniHub authority (`UNIHUB_PRODUCT_ENABLED=true` or `UNIHUB_PLATFORM_ENABLED=true`) never allows public native access, even with `NATIVE_PUBLIC_ACCESS=true`. Standalone public native operation requires both UniHub flags false and `NATIVE_PUBLIC_ACCESS=true`.

For UniHub deployments set `NATIVE_ADMIN_LISTEN_ADDRESS=127.0.0.1:3001` (or an explicit IPv6 loopback address and port). Startup validates and binds this separate listener; it shares the application handler and sessions. The private port must differ from the public port. Bind failure stops startup. Do not point any public reverse proxy at the private port. Access it through an authenticated SSH tunnel, e.g. local port 3001 to remote 127.0.0.1:3001, and log in with native credentials. Native route authentication and privileges still apply.

The boundary trusts only the socket address supplied by net/http. Origin, Referer, forwarded headers, query parameters, and MANAGEMENT_TOKEN do not grant native access. MANAGEMENT_TOKEN retains its separate readiness/metrics purpose and must never be sent to the product browser. Old NATIVE_ADMIN_ORIGIN/NATIVE_ADMIN_PROXY settings have no authority.

Signed payment callbacks remain registered publicly so existing orders can settle through their existing signature checks. The boundary does not establish callback signature validity.
