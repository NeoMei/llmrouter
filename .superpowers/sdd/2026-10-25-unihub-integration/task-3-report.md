# Task 3 implementation report — 2026-10-25

## Implemented boundary

Independent lazy `/unihub` product shell, discoverable from local sign-in and Wallet. This route bypasses local dashboard bootstrap/setup redirect and uses a separate non-persisted QueryClient and SDK instance. Existing operator sessions, wallets, API keys and legacy routes are untouched.

The fixed opt-in server configuration publishes enabled/public origin/client/redirect/product/allowed models only. The proof origin is server-only. HTTPS is required except loopback HTTP; credentials, origin paths, query and fragment injection are rejected. Model configuration is a strict JSON string array. Disabled is the default.

`POST /api/unihub/identity` accepts one bearer and no body/query overrides. It checks fixed-service capabilities kind=product, product_id=llmrouter, exact client_id, immutable `/v1/me.id`, and `/v1/accounts` with the same bearer/fixed Origin. Total proof deadline eight seconds, responses bounded to 1MiB, redirects rejected. The response projects only typed display identity/accounts plus fixed issuer. No local DB/User/session/PAT/quota is created; no cookie is written and UniHub account roles never become operator roles. Failed product proof never falls back to local login.

Environment variables: `UNIHUB_PRODUCT_ENABLED=true`, `UNIHUB_PRODUCT_PUBLIC_ORIGIN`, `UNIHUB_PRODUCT_API_ORIGIN`, `UNIHUB_PRODUCT_CLIENT_ID`, `UNIHUB_PRODUCT_REDIRECT_URI`, `UNIHUB_PRODUCT_MODELS` (JSON string array). Gateway `UNIHUB_ORIGIN`/service credentials remain separate. Development proxy now includes `/platform` for the same-origin gateway. SDK is a provenance-marked local copy from UniHub SDK source at `90a18fb`, not an absolute production import.

## Product behavior

- Registration and password/S256 PKCE login go directly to configured UniHub. UNKNOWN registration preserves original body/key and offers explicit original retry. Passwords/token values are not sent to local identity proof or written to browser storage.
- Tokens, task credentials, user/accounts, payer and product queries are memory-only. Reload requires login. Expiry/401/logout clear state, abort current HTTP work, increment epoch and discard late account results; late prior-account 401 cannot clear a newer account. No automatic financial mutation retries.
- Explicit personal/team payer selection precedes resource/model actions. Team creation, recipient invitation acceptance, owner/admin invite/member-role/removal actions are implemented; owner entries cannot be casually removed/changed. Owner/billing_admin finance controls are separate. Explicit account refresh now refetches members/invitations/resources/reports even if the operator's own account role did not change, with epoch fencing retained for changed roles.
- Resources show CREDIT and CNY separately, unified-wallet label for empty product_id, subscriptions, immutable published offers filtered by product/account kind/billing profile, upgrade quotes and addons. Purchase/accept/recharge creates objects and reads status; recharge success visibly shows ID/exact amount/created-unconfirmed state and queries that exact object, APPLIED alone means ledger-posted; no automatic payment/sandbox settlement. Real-payment availability is shown accurately.
- Recharge accepts ordinary decimal yuan (e.g.10.50 -> canonical string10500000) with exact string arithmetic. All displayed financial values preserve canonical digits above2^53. Financial intents hold account/payload/key across network uncertainty and payer switches (definitive validation/scope denials clear pending retry), and expose original retry/readback; logout/reload intentionally clears memory state.
- Usage attempts finance-authorized all-product model pool, falls back on403 to clearly labeled own-origin data, renders absent finance fields as unavailable rather than zero, groups asset amounts, and exposes accuracy/consistency details only in an optional details panel. Initial calls page is explicitly labeled if more data exists.
- Model send explicitly chooses configured model/funding mode, issues short-lived task credential, sends positive max_tokens/n=1/text messages with stable task/call headers to `/platform/v1/chat/completions`. Ordinary JSON and SSE text are parsed into assistant output, including frames split across chunks. Query receipt and revoke actions are provided; status/exact charge are visible and raw receipt optional. Media/OCR/Responses are explicitly unavailable. No durable key invented; no provider/payer/group/origin submitted by browser.
- English keys registered, Simplified Chinese product copy supplied; other locale additions use English fallback. Existing attribution literal escapes retained.

## Red/green evidence

Initial backend tests failed to compile because Config/FromEnv did not exist. Initial frontend tests failed module resolution because session implementation did not exist. After implementation the focused tests pass. Additional rendered gateway tests first exposed an exact accessible-label lookup gap in funding selector; explicit accessible names fixed it. Root actual two-browser team acceptance exposed stale member cache after remote invitation acceptance; explicit refresh invalidation and a rendered regression fixed it. Root flagged StrictMode mounted-ref reset and CNY raw-unit input; both corrected with unit/rendered coverage.

## Verification completed in this worker

- `go test ./internal/platformaccount ./middleware ./service ./controller ./router ./model` passed (identity, auth/session, wallet/controller/service/router/model suites).
- `go test -race ./internal/platformaccount ./router -run 'TestProof|TestConfig|TestImmutableIdentity|TestUniHub|TestDisabledUniHub|TestNonLocalCapabilities' -count=1` passed after testify conversion.
- `go build ./...` passed after actual frontend production build existed.
- `bun run test src/features/unihub/__tests__/*.test.ts`:10 passed. Exact money and decimal conversion; account stale read/401/logout; retained intent; invalid PKCE state; ordinary/SSE split frames and stale streamed output.
- `bun x playwright test --config playwright.unihub.config.ts`:10 passed using installed Chrome and isolated mock origins58004/58006. PKCE + no storage + reload; owner/member payer visibility; exact >2^53 balances + own-origin fallback; uncertain recharge retains original account/key across switch; expiry clearing; UNKNOWN registration stable payload/key; ordinary+stream task sends/receipts/revoke with unique task/call IDs; late grant after switch causes no dispatch; explicit refresh reveals a remotely accepted member; successful recharge exposes original ID/exact decimal amount/unconfirmed state and exact-object readback; definitive403 shows denial without retaining uncertain retry;390px viewport with long team label has no horizontal overflow.
- Typecheck, touched-file oxlint, production Rsbuild build and diff whitespace checks passed. New backend assertions use testify require/assert. No new dependencies or generic framework.

## Live acceptance boundary

Root owns actual local Casdoor/UniHub/Formance/provider/browser acceptance and records authoritative final evidence separately. Root reported actual login/personal resources/reload/no token storage/no console errors passed; actual ordinary+stream calls each SETTLED20CNY millionths, exactly2 provider sends, payer cash999960 and unchanged local principal quota0. Root reported actual two-browser team creation/invitation acceptance, member/admin finance hiding, billing_admin finance visibility, server403 after downgrade, owner recharge10.50->10500000 and readback all passed after the refresh fix. Root mobile screenshot identified intrinsic payer-select overflow; constrained control width/shortened option display and a390px rendered regression fixed it. These live observations were supplied by root, not independently repeated by this worker. No production changes or real merchant/payment claim.

## Remaining limits

- Product usage lists first calls page (explicitly labeled if has_more); complete reporting still available through SDK/API rather than a full analytics pagination/filter workspace.
- Pending financial intents are intentionally memory-only. Reload/logout loses the in-memory key; UI readback by original ID remains available after login. Uncertain live action should be queried/retried in its original tab before refresh. No automatic financial retry/settlement.
- Optional non-Chinese locales currently fall back to English for new product labels.
- Root owns final live readback and any further acceptance; this worker did not make production changes.

## Round 1 — definitive registration rejection (P2)

Review found that `pending` was established before registration dispatch and remained after definitive validation/policy/conflict rejection, disabling input correction and the sign-in switch. Added three rendered cases before the fix:400 invalid_password,403 origin_not_allowed and409 registration_conflict each failed on the expected disabled username field.

Verified current UniHub identity implementation before classifying errors.400 invalid_request/invalid_password and403 client_not_enabled/origin_not_allowed occur before row creation.409 interaction_required is a pre-create policy refusal; registration_conflict is a definitive collision/reconciled conflicting identity. These exact known code/status combinations now clear the local pending intent so a corrected submission receives a fresh key and registration/sign-in remain reachable.409 idempotency_conflict can reference unresolved original work, so it retains the original locked body/key, as do UNKNOWN/network/service failures and unrecognized error codes. Any successful response other than explicit CREATED also stays unresolved rather than falsely reporting creation. No automatic retry.

Validation:12 module unit tests passed;7 focused registration browser tests passed (three definitive correction/reachable-sign-in cases, original UNKNOWN test and network/pending/idempotency-conflict same-body/key tests). Touched lint, typecheck, production build and diff whitespace passed. No backend or running controller-owned services changed; Task4 docs left untouched.

## Round 2 — settled receipt readouts and upgrade choices

Actual product acceptance found a settled CREDIT task receipt alongside stale resources/usage. Added automatic/manual receipt rendered tests first; both failed because spendable credits stayed3.000000 instead of2.999980. The upgrade rendered regression first failed because four current/lower/different-period offers showed Quote upgrade instead of only the higher-rank same-period offer.

Task receipt acceptance now checks captured epoch, current payer and mounted state, sets the receipt and refetches only that payer's resources and usage when SETTLED. Automatic post-send receipt and explicit Query task receipt share this path. Refetch is read-only and does not retry the financial/model/provider request. A delayed old-payer receipt after switching cannot populate the new payer UI or refetch its resources. Upgrade eligibility now matches an active same-product membership subscription, same period and strictly higher rank; quote creation uses that matching subscription. Successful quote acceptance clears the accepted quote panel. Normal server reauthorization remains authoritative.

Verification:14 module unit tests passed, including upgrade eligibility/current/lower/year/addon/other-product/inactive cases. Full19-case browser suite passed; an additional focused late-receipt/payer-switch test passed afterward (20 total current cases). Three new red regressions became green; settled automatic/manual receipt readouts show updated credits/call count/exact per-asset charge, accepted quote disappears, old receipt does not affect new payer. Touched lint, typecheck, production build and diff whitespace checks passed. Existing registration files only received lint-required braces during scoped checks. No backend rerun/change, no running root service or Task4 docs modified.
