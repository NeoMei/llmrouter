# LLMRouter settings simplification — Task 1 report

Status: implementation finished; final verification results are recorded below.

Workspace: `/Volumes/codex/new-worktrees/uls-settings-20261105/llmrouter`
Branch: `codex/uls-settings-20261105`
Base: `d8686f28e0d5110888595a47d7171b1b2759360f`
Approved design: `/Users/neomei/项目/codexprojects/U-L-S 综合验收/docs/superpowers/specs/2026-11-05-settings-simplification-design.md`.
No dependency, backend API payload, key ownership, billing policy, release version or deployment changes.

## Delivered behavior

- Wallet loading, rejection, `success=false`, malformed financial data and authoritative zero are distinct. A failed refresh retains explicitly labeled previous values and disables financial controls. Missing metrics and invalid canonical cash quantities cannot become fabricated zeroes or crash the balance renderer.
- Wallet queries, order history, statistics and native receipts use a token-free identity/session/realm boundary. Wallet and native-receipt component state remounts on identity changes. Old responses cannot fill a new identity's query. Native-key providers, unfinished drafts, revealed-key caches and list/model/group queries are also isolated by identity.
- Safe retry buttons only refetch reads. Existing native amount quotation remains part of configured international payment behavior; no quote is automatically requested when there is no applicable international method. Wallet retries in the isolated failure fixtures cause no financial POST.
- Order History is an independent wallet action. Failure is never labeled “no records.” Failed refreshes label previous history, and admin completion is unavailable with failed history. Unresolved wallet funding restricts even an administrator to personal history; shared-cash administrators retain the personal order path.
- Native `/keys` and its old `/unihub` link explicitly identify **LLMRouter product API keys**. UniHub login does not turn these into managed `uh_key` credentials. Budget/unlimited quota, expiration and model access are visible; quantity, routing group, cross-group retry and IP restrictions remain under Advanced Settings. Existing defaults, POST/PUT contract, quota-preservation behavior and advanced values are retained.
- Shared CNY cash, local native quota and LR_QUOTA product measurements retain separate labels. A native receipt's final measurement, including zero, cannot certify a UniHub cash charge. IDs and pricing-policy internals are in native disclosure details. Public pricing is a directory/reference with key/permission/consumer availability caveats. Configured payment options are not certified collection.
- Mobile wallet labels wrap rather than ellipsize the product-usage identity. Original native navigation, payment options, histories and independent assets remain reachable.
- `docs/standalone-llmrouter-acceptance.md` documents these boundaries and the isolated UI workflow. Original source license headers remain byte-identical.

## Regression evidence and RED/GREEN

All new behavior tests render the real app/hooks/components in Chrome. Only API transport, initial browser storage and the real auth-bundle state boundary are fixtures; no component under test is mocked, no static source-text tests or new test dependencies were added.

| Evidence | Observed result | Log under this report directory |
| --- | --- | --- |
| Original wallet/UniHub node baseline | 56 passed | `logs/baseline-tests.log` |
| Key/default model-access disclosure and statistics/history failures | Expected failures reproduced | `logs/red-ui-corrected-fixture.log`, `logs/red-refresh-ui.log` |
| Original wallet code with the corrected financial-only transport boundary restored temporarily | 3 expected failures: business/network failures displayed `Current Balance 0`, and late prior-account response failed isolation | `logs/red-wallet-corrected-boundary.log` |
| Native receipt failure/measurement disclosure | 2 expected failures | `logs/red-native-ui.log` |
| Missing wallet metrics / malformed cash quantity | 2 expected failures | `logs/red-malformed-wallet.log` |
| Native-key draft retained across identity change | 1 expected failure | `logs/red-key-identity.log` |
| Admin financial source unresolved | 1 expected failure: original history selected `/api/user/topup` rather than personal history | `logs/red-admin-funding.log` |
| Chinese mobile product-usage label | 1 expected failure from standard DOM `scrollWidth > clientWidth` (ellipsis clipped the meaning) | `logs/red-mobile-label.log` |
| Final focused desktop UI, including three existing native quota edit checks | 25 passed | `logs/settings-final.log` |
| Final 390px English/Chinese wallet and key UI | 8 passed | `logs/mobile-final.log` |
| Original product user parity suite | 17 passed | `logs/parity-final.log` |
| Original native-metering editor/receipt suite | 2 passed | `logs/native-final.log` |
| Related node suites (UniHub, wallet polling, native metering) | 62 passed | `logs/tests-final.log` |
| `bun run typecheck` | Passed | `logs/typecheck-final.log` |
| Scoped oxlint | Passed, 0 errors; existing warnings retained | `logs/lint-final.log` |
| Protected-header-safe scoped formatter check | Passed for 30 TS/TSX/JSON files | `logs/format-final.log` |
| `bun run build` | Passed | `logs/build-final.log` |
| i18next runtime resolution/interpolation of all 34 new keys across seven locales | Passed | `logs/i18n-final.log` |
| Production output fixture audit | No `settingsFixture` or `fixture-only-` test markers in built JS | `logs/production-fixture-audit.log` |

An early wallet fixture incorrectly failed the layout's separate identity read, so that first failure run is not counted as wallet bug evidence. `walletResponse` now distinguishes the actual Axios financial GET from the layout's plain-fetch identity read. The dedicated restored-original-code RED above proves the corrected test catches the financial zero/race bug. Temporary originals/new-code backups remain in this local task directory; the implementation was restored before GREEN checks.

Two existing parity assertions were updated to the approved native-key heading and collapsed cross-group controls; original payload/permissions assertions remain. Existing native-receipt assertions now use the product-measurement name and explicit LR_QUOTA unit. No pre-existing finding was disabled or suppressed.

## Visual fixtures and screenshots

From the assigned `web/` directory:

```sh
bun x playwright test --config playwright.settings.config.ts
SETTINGS_MOBILE=1 bun x playwright test --config playwright.settings.config.ts --grep 'initial wallet|UniHub signed-in native key|Chinese'
```

The test config starts a separate Rsbuild fixture server on `http://127.0.0.1:58026`. It loads the regular app and a test-only entry; the production Rsbuild config is unchanged and does not import that entry. API routes are intercepted in `web/src/features/wallet/__tests__/settings-fixture.ts`, which exports `fixture`, `walletResponse`, `bundle`, and `screenshot`. Auth changes use the real `applyAuthBundle` boundary through that separate test entry. Do not open the server URL as an unseeded manual acceptance session: Playwright supplies the isolated auth and transport fixtures.

Recipes:

- Initial GET rejection/`success=false`, then read-only retry to exact zero: `--grep 'initial wallet'`.
- Shared funding with `1.230001 CNY`, then explicitly stale failed refresh: `--grep 'failed shared-wallet refresh'`.
- Native zero/new account with prior request delayed: `--grep 'late prior-account wallet'`.
- Native key disclosure, keyboard toggling and unchanged native default payload: `--grep 'UniHub signed-in native key'`.
- Chinese zero, unavailable, shared cash and native key form: `--grep 'Chinese'` (the fixture selects the real `zh` locale).

Screenshots are local under `screenshots/` beside this report; finite animations are fast-forwarded by Playwright, not captured mid-transition. English desktop: `wallet-business-failure.png`, `wallet-network-failure.png`, `wallet-successful-zero.png`, `wallet-shared-cash.png`, `wallet-native-after-account-switch.png`, `native-key-drawer.png`. English mobile equivalents have `-mobile`; Chinese group includes `wallet-failure-zh[-mobile].png`, `wallet-zero-zh[-mobile].png`, `wallet-shared-cash-zh[-mobile].png`, `native-key-drawer-zh[-mobile].png`.

Visual review inspected the error, exact shared cash and native key drawer at 1280px and failure/zero/key drawer at 390px in Chinese. The former half-empty error-grid and clipped mobile measurement label were corrected. Dev-only TanStack controls are visible in fixture screenshots; they are not production UI. Existing application palette/components are retained; no new design system or speculative page redesign was introduced.

## Remaining boundaries and risks

- No production model, payment, actual native/managed key mutation, deployment, version bump or publication was attempted. Browser POSTs only reached synthetic intercepted fixtures. Passing tests/builds do not certify production collection or provider availability.
- The built app retains its existing large dependency/chunk footprint (about 57.6 MB raw / 16.6 MB gzip); this change is not a bundle-size/performance acceptance.
- Existing lint warnings remain: nested ternaries in UniHub/shared unrelated dialogs/table code; an existing billing skeleton array-index key; existing native-e2e console logging. Scoped checks have zero errors and add no warning suppression.
- English, Simplified Chinese and Traditional Chinese contain all 34 new labels. French, Russian, Japanese and Vietnamese have translated action/error short labels but retain English fallbacks for 22 extended technical explanations each; the runtime key and interpolation checks still pass. Full human localization in those four languages is not claimed.
- `web/node_modules` reuses the installed canonical dependency directory via an ignored symlink. No package manifest or lockfile changed; build/test artifacts remain in this assigned external worktree and dependency caches. No new CodeGraph index was created and no canonical repository source was edited.
- The first task attempt stopped before edits when the external volume disconnected. Work resumed only after the original mounted, writable, exact-base worktree returned; there was no internal-disk fallback.

## Self-review

Reviewed the actual diff against the approved brief: loading/error/zero and stale financial states; same-page identity/realm changes; delayed responses; native/shared authority separation; admin history mode; money-action blocking; native key defaults and advanced disclosure; exact money/measurement naming; pricing caveat; original routes; changed translations; protected headers; absence of dependency/backend/publication changes. Parent review/integration remains separate from these local verification results.
