# Task 1 — Fix round 1

Status: fixed and locally verified; ready for independent re-review.

Workspace: `/Volumes/codex/new-worktrees/uls-settings-20261105/llmrouter`
Branch: `codex/uls-settings-20261105`
Reviewed candidate: `66ca4a06606abea79861d1afb41d5b9bed2a586b`
Review: `task-1-review.md` in this directory.

## Findings addressed

1. Statistics now require finite numeric `quota`, `rpm` and `tpm` before the query becomes successful. Missing fields, numeric strings and nonfinite values enter the existing unavailable/stale state. Rendering uses the validated values directly rather than supplying `|| 0`. Existing zero values, numeric rates, units and API payloads remain unchanged; no exchange-rate, charge or nonnegative-quota rule was invented.
2. Billing history now requires an actual `items` array and a nonnegative safe-integer `total`. The owning Go contract has `PageInfo.Total int` and `PageInfo.Items` populated from the top-up result slice; ordinary empty results remain `{items:[],total:0}`. Fractional counts such as `0.5`, missing counts, strings and negative counts cannot be shown as authoritative empty history. The query throws into the existing error/stale flow; cached valid rows remain labeled stale. Query keys, funding/permissions selection and financial mutation guards were not changed.
3. Corrected 21 values among the previously added Traditional Chinese labels, including `最終`, `此處`, `為`, `財務`, `回執`, `與`, `暫時`, `失敗`, and `兌換碼`. Other locales and the disclosed English technical-explanation fallbacks are unchanged.

No new helper, dependency, backend modification, route, account/credential/funds mutation, pricing change, version bump, push or deployment was introduced. Changes are restricted to the two response boundaries, their owning browser tests and the scoped Traditional Chinese wording.

## RED → GREEN

Tests use the real app/components and the existing transport fixture. No production component is mocked and no source-text regression test was added.

| Check | Command / method | Result | Evidence |
| --- | --- | --- | --- |
| Initial malformed-success regressions | From `web/`: `bun x playwright test --config playwright.settings.config.ts --grep 'malformed (statistics\|history)'` | RED: all 9 new cases failed before implementation | `logs/fix-round-1-red.log` |
| Same focused cases after finite/shape validation | Same command | GREEN: 9 passed in 13.1s | `logs/fix-round-1-green-focused.log` |
| Additional fractional-total finding | `bun x playwright test --config playwright.settings.config.ts --grep 'malformed history success with fractional total'` | RED: 1 expected failure before integer-count guard | `logs/fix-round-1-red-fractional-total.log` |
| Final complete focused UI suite, including all 10 new cases and existing valid-zero/empty/state/identity cases | `bun x playwright test --config playwright.settings.config.ts` | GREEN: 35 passed in 45.2s; exit 0 | `logs/fix-round-1-ui.log` |
| TypeScript | `bun run typecheck` | `$ tsgo -b`, exit 0 | `logs/fix-round-1-typecheck.log` |
| Production build | `bun run build` | Exit 0; existing overall footprint ~57.6 MB raw / 16.6 MB gzip | `logs/fix-round-1-build.log` |
| Involved feature/e2e/config lint, same wider scope as candidate gate | `bun x oxlint -c .oxlintrc.json src/features/usage-logs src/features/wallet src/features/keys src/features/unihub/index.tsx src/features/native-metering/billing-panel.tsx src/features/pricing/index.tsx src/hooks/use-auth-identity.ts e2e/native-metering.spec.ts e2e/product-user-parity.spec.ts playwright.settings.config.ts rsbuild.settings.config.ts` | Exit 0, zero errors; the same 10 baseline warnings | `logs/fix-round-1-lint.log` |
| Formatting and retained license headers | For the five changed TS/TSX/JSON files: `oxfmt -c web/.oxfmtrc.json --stdin-filepath=<file>` on the protected-header-free body; compare formatter stdout and original source header bytes | All five passed; validation made no source writes | `logs/fix-round-1-format.log` |
| Traditional Chinese runtime mapping | Load changed locale into a real i18next instance, resolve every changed key, verify reviewed Simplified forms absent and routing-group interpolation preserved | 21 changed mappings passed | `logs/fix-round-1-i18n.log` |
| Whitespace/conflict sanity | `git diff --check` | Passed | Checked before commit |

The initial cases cover missing metrics, wrong metric types, a valid JSON number (`1e999`) that decodes to nonfinite quota, missing history fields, nonarray items, wrong total type, negative total, and both cached-refresh paths. The additional count case covers fractional totals. Every initial failure is followed by a safe GET retry to valid zero metrics or valid empty history; assertions confirm no financial POST. The final full suite retains the original successful-zero/empty tests and nonempty/stale history tests.

## Remaining boundaries

- The ten lint warnings remain unchanged baseline findings: existing nested ternaries, billing/Creem skeleton index keys and E2E console calls. None was silenced, disabled or refactored in this round.
- Unchanged broader node, native-metering, mobile and parity suites retain their candidate evidence in `task-1-report.md`; they were not repeated as unrelated work in this targeted fix round. This round's latest UI suite, typecheck, lint and build ran after the final integer-count guard and final source formatting.
- No real model, payment or key call was accepted or attempted. Build/UI verification is not production collection or provider acceptance. Full localization in the four disclosed fallback locales remains outside this finding.
