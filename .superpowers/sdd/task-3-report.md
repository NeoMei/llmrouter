# Task 3 Browser Acceptance Report

## Scope

Added concise LLMRouter connection-guide browser acceptance coverage only. It covers public `/docs` rendering, explicit Chinese/English switching and persistence, Base URL copy feedback, supported Agent content, approved internal links (`/keys`, `/dashboard/models`, `/client-model-routes`, `/install/`), direct-anchor refresh, and mobile navigation. No SDK, API, or tutorial coverage was added.

## RED evidence: current production

Command (temporary config outside committed source):

```sh
cd web && ./node_modules/.bin/playwright test docs.spec.ts \
  --config=/tmp/llmrouter-production.playwright.config.ts \
  --project=chromium --grep='renders the public concise'
```

Result: expected failure against `https://llmrouter.quukk.com/docs` before deployment. The test could not find the `Connect an LLMRouter Agent` heading (`getByRole(...).toBeVisible()` timed out), confirming production did not yet render the guide. The temporary `/tmp/llmrouter-production.playwright.config.ts` was not committed.

## GREEN evidence: local embedded Go server

Focused docs and existing route regression command:

```sh
cd web && ./node_modules/.bin/playwright test docs.spec.ts client-model-routes.spec.ts --retries=0
```

Result: `17 passed`, `1 skipped` in 4.5 s. The skipped test is the desktop instance of the mobile-navigation case; its mobile Chromium instance passed. Playwright started the existing full Go server command from `web/playwright.config.ts` at `http://127.0.0.1:3200`.

Full web verification command:

```sh
scripts/verify-web.sh
```

Result: completed successfully in the final prior run: protected-header format check, copyright check, lint (existing repository warnings only), TypeScript/build checks, Node tests (`104 pass`, `0 fail`), production build, and knip. A later direct `npm run knip` also exited 0. The formatter required a pre-existing wrapped union in `docs-content.ts`; its equivalent one-line formatting change is included.

## UI observations

- Desktop renders the public English guide with all seven concise sections and approved link destinations.
- Explicit language selection displays Chinese, survives reload, and switches back to English.
- The Base URL copy test forces the native clipboard fallback and observes the `Copied` feedback; the URL text remains a selectable `<code>` value.
- `#configure-agent` remains in the URL and renders after refresh; Agent cards list Codex CLI/Desktop, Claude Code/Desktop with MCP tool access, OpenCode, Continue, and Aider.
- Mobile Chromium exposes the horizontally scrollable documentation navigation and its `#configure-agent` link.

## Files and commit

- `web/e2e/docs.spec.ts` — new Playwright acceptance suite.
- `web/src/features/docs/docs-content.ts` — formatter-only line wrapping normalization.
- `.superpowers/sdd/task-3-report.md` — this report.

No screenshots, traces, or temporary Playwright configuration were added to committed source. Test artifacts remain ignored by `web/.gitignore`.

## Concerns

- Production is intentionally RED until the new web assets are deployed; no deployment was performed in this task.
- The Go server emits its existing `TRUSTED_PROXIES` startup warning and Playwright emits existing `NO_COLOR` environment warnings; neither was a docs UI or test failure.
