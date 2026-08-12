# LLMRouter Developer Documentation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build, test, and deploy a public bilingual `/docs` developer center for LLMRouter without embedding dynamic model, pricing, quota, capability, or routing data.

**Architecture:** Add one public TanStack Router route backed by a focused React feature module. Keep all documentation and examples in typed static data, isolate locale resolution and clipboard behavior in testable helpers, and reuse the existing `PublicLayout` and UI primitives. Build a dedicated image from the production branch, deploy it to the parallel `llmrouter` container behind `llmrouter.quukk.com:8445`, and leave `llmrouter.quukk.com` unchanged.

**Tech Stack:** React 19, TypeScript, TanStack Router, Tailwind CSS, existing UI primitives, Node test runner through `tsx --test`, Playwright, Go embedded web assets, Docker, nginx.

## Global Constraints

- `/docs` is public and readable without authentication.
- Documentation supports exactly `zh-CN` and `en`; Chinese browser locales default to Chinese and all other locales default to English.
- The selected documentation language is persisted locally only after explicit user action.
- Documentation is static and must not call model, management, user, pricing, quota, capability, channel-health, or routing APIs.
- The page must not request, accept, retain, validate, render, or transmit an API token.
- Do not embed a current model list, prices, ratios, quotas, context limits, channel state, or per-model capability table.
- Dynamic information is linked to `/dashboard/models` and `/client-model-routes`.
- The production origin is `https://llmrouter.quukk.com`; OpenAI-compatible examples use `https://llmrouter.quukk.com/v1`.
- Reuse the existing design system and dependencies; do not add Markdown, syntax-highlighting, OpenAPI, or documentation-framework dependencies.
- Keep `llmrouter.quukk.com` routed to the parallel NewAPI system and do not modify it.
- Preserve the previous LLMRouter container/image and nginx configuration for rollback.

---

### Task 1: Typed Bilingual Documentation Content and Locale Policy

**Files:**
- Create: `web/src/features/docs/docs-content.ts`
- Create: `web/src/features/docs/docs-content.test.ts`
- Create: `web/src/features/docs/docs-locale.ts`
- Create: `web/src/features/docs/docs-locale.test.ts`

**Interfaces:**
- Produces: `DOCS_LANGUAGES`, `DocsLanguage`, `DocsContent`, `docsContent`, `DOCS_LANGUAGE_STORAGE_KEY`, `resolveDocsLanguage(persisted: string | null, browserLanguage: string): DocsLanguage`, and `persistDocsLanguage(storage: Pick<Storage, 'setItem'>, language: DocsLanguage): boolean`.
- `DocsContent.sections` contains stable IDs for `quick-start`, `authentication`, `protocols`, `streaming`, `tool-calling`, `image-input`, `reasoning-continuity`, `sdk-examples`, `client-integration`, `models-routing`, `errors-troubleshooting`, and `installation`.

- [ ] **Step 1: Write failing content-contract tests**

Create `docs-content.test.ts` using `node:test` and `node:assert/strict`. Assert that both languages contain exactly the required section IDs, all three protocol endpoints, the production origin, and links to `/install/`, `/dashboard/models`, and `/client-model-routes`. Serialize `docsContent` and assert that it does not contain `sk-`, `price`, `倍率`, `quota`, or a `models: [...]` catalog field.

```ts
test('both languages contain every required static docs section', () => {
  for (const language of DOCS_LANGUAGES) {
    assert.deepEqual(
      docsContent[language].sections.map((section) => section.id),
      REQUIRED_SECTION_IDS
    )
  }
})
```

- [ ] **Step 2: Run the content test and verify RED**

Run: `cd web && npm test -- src/features/docs/docs-content.test.ts`

Expected: FAIL because `docs-content.ts` does not exist.

- [ ] **Step 3: Implement typed bilingual static content**

Define focused content types for navigation labels, prose blocks, callouts, code examples, and internal links. Include complete Chinese and English content for all twelve required sections. Use illustrative model names such as `<PUBLIC_MODEL_ID>` and token environment variables such as `$LLMROUTER_API_KEY`; never include a token-shaped example.

Examples must cover:

```text
GET /v1/models
POST /v1/chat/completions
POST /v1/responses
POST /v1/messages
Authorization: Bearer $LLMROUTER_API_KEY
```

- [ ] **Step 4: Run the content test and verify GREEN**

Run: `cd web && npm test -- src/features/docs/docs-content.test.ts`

Expected: PASS.

- [ ] **Step 5: Write failing locale-policy tests**

Cover valid persisted values, invalid persisted values, Chinese browser locales, non-Chinese locales, successful persistence, and storage exceptions.

```ts
test('persisted language wins over browser locale', () => {
  assert.equal(resolveDocsLanguage('en', 'zh-CN'), 'en')
})

test('storage failures do not break language switching', () => {
  const storage = { setItem() { throw new Error('denied') } }
  assert.equal(persistDocsLanguage(storage, 'zh-CN'), false)
})
```

- [ ] **Step 6: Run locale tests and verify RED**

Run: `cd web && npm test -- src/features/docs/docs-locale.test.ts`

Expected: FAIL because `docs-locale.ts` does not exist.

- [ ] **Step 7: Implement locale resolution and safe persistence**

Use exact values:

```ts
export const DOCS_LANGUAGES = ['zh-CN', 'en'] as const
export const DOCS_LANGUAGE_STORAGE_KEY = 'llmrouter.docs.language'
```

`resolveDocsLanguage` accepts only exact persisted values. Otherwise it returns `zh-CN` when `browserLanguage.toLowerCase().startsWith('zh')`, and `en` otherwise. `persistDocsLanguage` catches storage errors and returns a boolean.

- [ ] **Step 8: Run Task 1 tests and commit**

Run:

```bash
cd web
npm test -- src/features/docs/docs-content.test.ts src/features/docs/docs-locale.test.ts
cd ..
git diff --check
git add web/src/features/docs
git commit -m "feat(docs): define bilingual developer content"
```

Expected: all focused tests pass and the commit succeeds.

---

### Task 2: Responsive Documentation Page, Clipboard Feedback, and Public Route

**Files:**
- Create: `web/src/features/docs/code-block.tsx`
- Create: `web/src/features/docs/docs-page.tsx`
- Create: `web/src/features/docs/index.ts`
- Create: `web/src/features/docs/docs-page.test.ts`
- Create: `web/src/routes/docs.tsx`
- Modify: generated route tree file if the router plugin updates it during build

**Interfaces:**
- Consumes: Task 1 `docsContent`, `DocsLanguage`, `resolveDocsLanguage`, and `persistDocsLanguage`.
- Produces: `DocsPage`, public `/docs` route, labelled copy controls, stable section anchors, internal live-data links, and responsive desktop/mobile navigation.

- [ ] **Step 1: Write failing page-source contract tests**

Because the repository does not include a DOM component-test dependency, test static route/component contracts with the existing Node runner. Read source files through `node:fs/promises` and assert:

- `web/src/routes/docs.tsx` registers `createFileRoute('/docs')` and imports `DocsPage`;
- `docs-page.tsx` renders `PublicLayout`, every stable section ID, language controls labelled in both languages, `/dashboard/models`, `/client-model-routes`, and `/install/` links;
- `docs-page.tsx` does not import API clients, React Query, axios, or token stores;
- `code-block.tsx` uses `navigator.clipboard.writeText`, exposes a manual-copy fallback, and never executes example code.

```ts
test('docs route is public and renders DocsPage', async () => {
  const source = await readProjectFile('src/routes/docs.tsx')
  assert.match(source, /createFileRoute\('\/docs'\)/)
  assert.match(source, /component:\s*DocsPage/)
})
```

- [ ] **Step 2: Run page tests and verify RED**

Run: `cd web && npm test -- src/features/docs/docs-page.test.ts`

Expected: FAIL because the page and route files do not exist.

- [ ] **Step 3: Implement clipboard code block**

Create `CodeBlock` with props `{ label: string; code: string; copyLabel: string; copiedLabel: string; manualCopyLabel: string }`. On click, call `navigator.clipboard.writeText(code)`. Show a short copied state on success and an accessible manual-copy message on failure. Always render the code as selectable text in `<pre><code>`.

- [ ] **Step 4: Implement responsive DocsPage**

Use `PublicLayout`. Resolve the initial language in a lazy `useState` initializer without writing storage. On user switch, update state, call `persistDocsLanguage`, and leave `window.location.hash` unchanged.

Desktop layout:

```text
max-width content shell
  sticky left navigation (hidden below lg)
  main article with section cards and code blocks
```

Mobile layout uses a labelled native `<select>` or existing lightweight select primitive that navigates by assigning `window.location.hash` to a known section ID. Do not add a dependency.

Render all content from `docsContent[language]`; avoid duplicating Chinese/English JSX branches. Links to live data use TanStack `Link` where compatible and preserve natural sign-in redirects for protected pages.

- [ ] **Step 5: Register public `/docs` route**

Create:

```tsx
import { createFileRoute } from '@tanstack/react-router'
import { DocsPage } from '@/features/docs'

export const Route = createFileRoute('/docs')({
  component: DocsPage,
})
```

- [ ] **Step 6: Run page tests and verify GREEN**

Run: `cd web && npm test -- src/features/docs/docs-page.test.ts`

Expected: PASS.

- [ ] **Step 7: Run focused and build verification**

Run:

```bash
cd web
npm test -- src/features/docs/docs-content.test.ts src/features/docs/docs-locale.test.ts src/features/docs/docs-page.test.ts
npm run build:check
npm run lint -- src/features/docs src/routes/docs.tsx
cd ..
git diff --check
```

Expected: tests, route generation/type checking, build, and lint pass without relevant warnings.

- [ ] **Step 8: Commit**

```bash
git add web/src/features/docs web/src/routes/docs.tsx web/src/routeTree.gen.ts
git commit -m "feat(web): add LLMRouter developer docs"
```

---

### Task 3: Browser Acceptance and Regression Certification

**Files:**
- Create: `web/e2e/docs.spec.ts`
- Modify: documentation source only if the failing browser test reveals a defect

**Interfaces:**
- Consumes: public `/docs` route from Task 2.
- Produces: automated desktop/mobile acceptance coverage and screenshot evidence outside the repository.

- [ ] **Step 1: Write failing Playwright acceptance test before serving the new build**

Create `web/e2e/docs.spec.ts` covering:

```ts
test('public developer docs switch language, copy code, and expose live links', async ({ page, context }) => {
  await context.grantPermissions(['clipboard-read', 'clipboard-write'])
  await page.goto('/docs#authentication')
  await expect(page.getByRole('heading', { name: /Developer Documentation|开发者文档/ })).toBeVisible()
  await page.getByRole('button', { name: 'English' }).click()
  await expect(page.getByRole('heading', { name: 'Authentication' })).toBeVisible()
  await page.getByRole('button', { name: /Copy/ }).first().click()
  await expect(page.getByText(/Copied/)).toBeVisible()
  await expect(page.getByRole('link', { name: /Live models/ })).toHaveAttribute('href', '/dashboard/models')
  await page.reload()
  await expect(page.getByRole('heading', { name: 'Authentication' })).toBeVisible()
})
```

Add a mobile viewport test that confirms the compact section selector is visible and the desktop sidebar is hidden.

- [ ] **Step 2: Verify the test fails against the production build**

Run the existing deployed image or current production URL with the test base URL set to `https://llmrouter.quukk.com`.

Expected: FAIL because the deployed `/docs` renders the 404 page.

- [ ] **Step 3: Run browser acceptance against the Task 2 build**

Start the built app using the repository's supported preview or full server workflow, then run:

```bash
cd web
npm run e2e -- e2e/docs.spec.ts
```

Expected: desktop and mobile tests pass.

- [ ] **Step 4: Perform rendered QA**

Use the in-app Browser plugin when available. Validate page identity, meaningful DOM, absence of framework/not-found overlays, console health, language persistence, code-copy feedback, anchor refresh, internal links, desktop layout, and a mobile viewport. Capture screenshots for desktop Chinese, desktop English, and mobile navigation outside the repository.

- [ ] **Step 5: Run frontend regression checks**

Run:

```bash
bash scripts/verify-web.sh
cd web
npm run e2e -- e2e/docs.spec.ts e2e/client-model-routes.spec.ts
cd ..
git diff --check
```

Expected: all checks pass.

- [ ] **Step 6: Commit**

```bash
git add web/e2e/docs.spec.ts
git commit -m "test(web): certify developer docs"
```

---

### Task 4: Build, Deploy, and Certify LLMRouter in Parallel

**Files:**
- Modify: deployment evidence under the server's existing protected backup/evidence directory only; do not commit secrets
- Do not modify: `llmrouter.quukk.com` nginx routing

**Interfaces:**
- Consumes: reviewed Task 1–3 commits.
- Produces: a versioned LLMRouter image, a parallel `llmrouter` container serving `/docs`, production browser/API evidence, and a rollback reference.

- [ ] **Step 1: Run release verification before building**

Run:

```bash
go test ./internal/onboarding ./cmd/llmrouter-onboard
bash scripts/verify-web.sh
bash scripts/verify-cli.sh
git diff --check
git status --short
```

Expected: all tests pass and only intentional ignored evidence files remain.

- [ ] **Step 2: Push reviewed branch to the private GitHub repository**

```bash
git push -u llmrouter feat/llmrouter-docs
```

Expected: branch is available at `NeoMei/llmrouter` and repository visibility remains PRIVATE.

- [ ] **Step 3: Build a revision-labelled image on the server**

Fetch the exact reviewed commit into a new server source directory, build an image tagged `llmrouter:docs-<short-sha>`, and record image ID, revision, build timestamp, and source SHA. Do not overwrite the previous image tag.

- [ ] **Step 4: Replace only the parallel LLMRouter container**

Record the current `llmrouter` container inspect output and image ID. Start a candidate container on a temporary loopback port using the same protected env file, volumes, and Docker network. Verify `/healthz` and rendered `/docs` before replacing the `llmrouter` container. Keep the old container renamed and stopped until certification completes.

- [ ] **Step 5: Certify production routes and APIs**

Verify through `https://llmrouter.quukk.com`:

```text
/healthz                         200 with {"status":"ok"}
/docs                            200 and rendered Developer Documentation, no 404
/docs#tool-calling               renders the anchored section
/install/                        200
/dashboard/models                200 application route
/client-model-routes             200 application route
/v1/models                       200 with an existing migrated test token
one bounded inference request    200 with a non-empty result
```

Run the Playwright docs acceptance test against the public URL. Confirm `https://llmrouter.quukk.com` still returns its original parallel system.

- [ ] **Step 6: Roll back on any failed certification**

If any check fails, stop/remove only the candidate container, restore the previous `llmrouter` container name and start it, then re-run `/healthz` and the public baseline. Do not alter the NewAPI container or its nginx virtual host.

- [ ] **Step 7: Publish reviewed commits**

After certification, merge the docs branch into the repository's release branch according to the existing branch workflow, push it to `NeoMei/llmrouter`, and record the deployed commit and image digest in release evidence. Do not create a new release tag until the broader onboarding release gates are complete.

