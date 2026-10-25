# UniHub integration local acceptance — 2026-10-25

This report covers the integration source branch based on current LLMRouter `f808b1cd6` plus the restored durable bridge, and UniHub reporting commit `90a18fb`. It is local source/build/browser acceptance. No production upgrade, live supplier credential or real payment provider was used.

## Executed components

- Actual LLMRouter router/controller/OpenAI adapter, full executable and embedded production frontend; fixed product proof endpoint.
- Actual UniHub HTTP service and Casdoor identity, registered exact llmrouter client/Origin/redirect, JSON password authorization + S256 exchange.
- Real PostgreSQL using a dedicated acceptance schema and real Formance using a separate logical ledger.
- Fresh headless Chrome browser profiles, independent owner/member users. No existing operator/business account or balance migrated.
- A controlled loopback model endpoint returning explicit ordinary/SSE usage. This is a model-response fixture, not live provider acceptance.
- Initial isolated `prepaid` profile, then monotonic `hybrid` profile version2 with explicitly sandbox month/year/rank2/addon offers. Test wallets were deliberately funded through Formance fixture commands; this is not merchant payment evidence.

## Browser and ledger readback

| Flow | Observed result |
| --- | --- |
| Register/password login | New users created in real Casdoor; PKCE token exchange and fixed exact product identity accepted |
| Definitive registration rejection | Real weak-password400 left fields/sign-in editable; corrected request created a new user, which signed in and out successfully |
| Memory-only identity | Reload required login; no UniHub access/model token in localStorage/sessionStorage; no local LLMRouter business User/session created |
| Explicit personal payer | Resource query showed the selected account's real llmrouter CNY wallet; unified wallet remained separate |
| Cash ordinary + SSE | Two calls in each development/production-asset pass, four total, all SETTLED20CNY/6 each; exactly four supplier sends and aggregate80CNY/6; local quota unchanged |
| Team invitation | Owner created team, invited another user, recipient accepted and selected team in a second browser context |
| Role boundaries | Member/admin finance controls absent, billing_admin controls present; downgrade before stale-UI submission returned403 from UniHub |
| Recharge | Owner entered10.50yuan; body retained exact10500000CNY/6, productllmrouter; original object readback worked; creation was not displayed as a payment or ledger posting |
| Month + upgrade + addon | Sandbox monthly order paid/fulfilled; rank2 prorated upgrade and addon paid/fulfilled through real wallet/ledger, with separate payment/fulfillment readback |
| Annual membership | Year order paid/fulfilled; first-month spendable credit1000000CREDIT/6, not twelve months upfront |
| Credit model calls | Two SETTLED20CREDIT/6 calls; final owner monthlyrank2 plus addon left2999960CREDIT/6; reporting separated80CNY/6 and40CREDIT/6, six calls total |
| Final production assets after correction | SETTLED automatically refreshed credits and usage without reloading; manual receipt did not redispatch; invalid upgrade buttons absent; mobile390 no overflow and no page errors |
| Mobile390 | Long account/team selection no longer widened document; no horizontal overflow after correction |
| Rendering | Meaningful LLMRouter page, no framework overlay or page errors; desktop/mobile screenshots inspected |

Screenshots and nonsecret readback JSON are retained in the local acceptance directory outside tracked source. Passwords and runtime service secrets remain owner-only private files and are absent from commits/reports.

The opt-in `TestComposableBillingActualGatewayAcceptance` additionally ran against actual LLMRouter TCP routing + UniHub HTTP + isolated PostgreSQL/Formance with race detection. Legacy hybrid CREDIT and no-membership prepaid CNY both settled exactly once after a deliberately lost settlement response and gateway restart. Duplicate logical POST was rejected, no second supplier dispatch occurred and local quota remained unchanged. Unknown dispatch remains reconciliation work rather than an automatic model retry.

## Automated validation

- LLMRouter full `GOMAXPROCS=4 go test -p4 ./... -count=1`:63 passing test packages, no failures.
- UniHub full `go test -race ./... -count=1 -timeout=10m` with explicit test database/ledger and LLMRouter fixture:14 passing test packages, no failures.
- Focused LLMRouter platform billing/account-pool and product identity/router race suites passed.
- Final `bun x playwright test --config playwright.unihub.config.ts`:20/20 passed (31.5s), using the complete final suite at `fb25a525e`.
- Final `bun run test src/features/unihub/__tests__/*.test.ts`:14/14 passed, zero skipped/failed.
- Product browser fixtures covered exact client proof, PKCE, memory cleanup, stale actor/account reads, late401/grants, role visibility, remote-member refresh, exact amounts, stable financial intent keys, recharge readback, definitive/uncertain registration, ordinary/SSE/receipt/revoke and responsive layout.
- Product frontend unit tests, existing auth/wallet regressions (24 tests), SDK protocol/OpenAPI tests (14), SDK typecheck and offline onboarding tests (7) passed.
- Frontend typecheck, touched-file lint, actual production build, Go build and whitespace checks passed. Embedded frontend assets were built normally; no placeholder `web/dist` was introduced.

The Task 1–3 scoped reviews are approved. Final whole-branch review remains pending and is explicitly unchecked in the integration plan; these acceptance results do not substitute for it. The checked-in Playwright config uses an installed Chrome executable when a bundled shell is unavailable. Browser plugin was unavailable in this session; a fresh headless Playwright context was used, without borrowing user tabs or installing browser dependencies.

## Corrections made from observed failures

- Prevented raw-key HTTP redirects from adding untracked supplier sends; preserved current account-pool retry veto and live authorization validation.
- Replaced permissive shared-DTO funding admission with an explicit supported text contract, rejecting vendor search/surcharge fields before reservation.
- Added exact capabilities product/client proof; `/v1/me` alone also accepts other clients.
- Fixed StrictMode task lifecycle, role/account stale query handling and remote accepted-member refresh.
- Replaced raw micro-unit recharge input with exact decimal yuan; added visible created/unconfirmed receipt and original-object query.
- Fixed definitive registration rejection trapping the form, while retaining original key/payload on uncertain outcomes.
- Constrained the intrinsic account selector on narrow screens.
- Updated same-payer resources/usage after a settled receipt, fenced late old-payer receipts, limited upgrade choices to eligible same-period higher-rank memberships and cleared accepted quotes.

## Remaining release boundaries

Production remains on its previously published build. Deploy the new UniHub reporting support and reviewed llmrouter branch, exact client/rate/profile configuration and protected internal networking before enabling the integration there. Merchant activation, real upstream provider acceptance, Responses/media/OCR billing, durable product API keys and old account/balance migration are separate explicit work. The existing unsupported routes stay available only through their original local authentication/billing paths; no hidden fallback is introduced.

## Reproduction commands and evidence ownership

From the LLMRouter worktree:

```sh
GOMAXPROCS=4 go test -p4 ./... -count=1
cd web
bun run test src/features/unihub/__tests__/*.test.ts
bun x playwright test --config playwright.unihub.config.ts
bun run typecheck
bun run build
```

From the UniHub worktree, after privately configuring the existing isolated database/ledger fixture URLs (never print the secret environment):

```sh
export UNIHUB_LLMROUTER_PATH=/absolute/path/to/current-llmrouter
# UNIHUB_TEST_DATABASE_URL and UNIHUB_TEST_LEDGER_URL are explicitly required.
GOMAXPROCS=4 GOFLAGS=-p=4 go test -race ./internal/httpapi -run '^TestComposableBillingActualGatewayAcceptance$' -v -count=1 -timeout=10m
GOMAXPROCS=4 GOFLAGS=-p=4 go test -race ./... -count=1 -timeout=10m
```

The controller executed the broad Go/race/build and real-stack browser flows; Task 4 packaging independently reran the final14 module units and complete20 browser fixtures and checked the nonsecret readbacks. The browser fixture suite mocks product APIs; it is distinct from the actual Casdoor/UniHub/ledger observations above. The final real-stack readback is `browser-final-refresh-result.json`, with `final-production-workbench.png` and `final-production-mobile.png` in the owner-only local acceptance directory. Earlier `hybrid-ledger-readback.json` captures the five-call/20CREDIT intermediate stage; it is not the final six-call/40CREDIT balance. No private runtime JSON, passwords or service tokens are included in the tracked evidence.

## Final independent core readback

A fresh read-only core query of the isolated PostgreSQL/Formance state (`final-ledger-readback.json`, after the final browser run) confirms six settled owner calls, no pending/unknown/missing-usage calls, and separate charges of80CNY/6 and40CREDIT/6. The summary reports `asset=mixed` and omits a cross-asset aggregate `charged` amount. Owner credit batches total2999960 spendable (999960 base +1000000 upgrade +1000000 addon), with zero reserved; llmrouter cash is17999949CNY/6. The annual member has exactly1000000 available first-month CREDIT/6, zero consumed/reserved and9000000CNY/6 cash. The controller's final provider/local router readback confirms six supplier dispatches and unchanged local quota/used quota/request count0. This readback used the isolated core fixture rather than another product login; repeated test-owner login had reached the expected identity rate limit, which was left intact.

## Final independent review

Final whole-branch review approved spec compliance and code quality for LLMRouter `f808b1cd6..251c476e1` and UniHub `fe53acb..54da524`, with no confirmed important/critical finding or approved-scope gap. The reviewed implementation code is `fb25a525e`; subsequent LLMRouter changes only removed an accidentally tracked coordination report and completed this documentation/checklist. Task-scoped reviews and scoped corrections also passed. Production publication/deployment and merchant/provider activation remain separate operations; both source worktrees are left committed for review.
