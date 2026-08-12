# LLMRouter Developer Documentation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task.

**Goal:** Build, test, and deploy a public bilingual `/docs` developer center without embedding dynamic model, price, quota, capability, or routing data.

**Architecture:** A public TanStack Router route renders typed bilingual static content through focused React components and existing UI primitives. Test locale/content contracts first, then page/route contracts, followed by Playwright browser acceptance and isolated production deployment.

**Tech Stack:** React 19, TypeScript, TanStack Router, Tailwind CSS, Node test runner through `tsx --test`, Playwright, Go embedded web assets, Docker, nginx.

## Global Constraints

- `/docs` is public and readable without authentication.
- Support exactly `zh-CN` and `en`; Chinese browser locales default to Chinese and all others to English.
- Persist language only after explicit user action.
- Do not call model, management, user, pricing, quota, capability, channel-health, or routing APIs.
- Do not request, accept, retain, validate, render, or transmit an API token.
- Do not embed a model list, prices, ratios, quotas, context limits, channel state, or per-model capability table.
- Link dynamic information to `/dashboard/models` and `/client-model-routes`.
- Use `https://llmrouter.quukk.com` and `https://llmrouter.quukk.com/v1` in examples.
- Reuse existing dependencies and design system.
- Keep `llmapi.quukk.com` unchanged and retain rollback state.

### Task 1: Typed Bilingual Content and Locale Policy

**Files:** create `web/src/features/docs/docs-content.ts`, `docs-content.test.ts`, `docs-locale.ts`, and `docs-locale.test.ts`.

**Interfaces:** export `DOCS_LANGUAGES`, `DocsLanguage`, `DocsContent`, `docsContent`, `DOCS_LANGUAGE_STORAGE_KEY`, `resolveDocsLanguage`, and `persistDocsLanguage`.

- [ ] Write failing Node tests for twelve required section IDs, three protocol endpoints, origin, internal links, forbidden dynamic/secret data, locale precedence, and storage failure.
- [ ] Run tests and confirm failure because modules do not exist.
- [ ] Implement complete typed Chinese/English static content and exact storage key `llmrouter.docs.language`.
- [ ] Run focused tests and commit `feat(docs): define bilingual developer content`.

### Task 2: Responsive Page, Clipboard, and Route

**Files:** create `web/src/features/docs/code-block.tsx`, `docs-page.tsx`, `index.ts`, `docs-page.test.ts`, and `web/src/routes/docs.tsx`; update generated route tree through build.

**Interfaces:** public `DocsPage`, `/docs` route, stable anchors, labelled copy controls, and responsive navigation.

- [ ] Write failing source-contract tests for public route registration, existing `PublicLayout`, required anchors/links, no API imports, and clipboard/manual-copy behavior.
- [ ] Run tests and confirm failure.
- [ ] Implement code block, data-driven page, locale switch, sticky desktop navigation, mobile section selector, and route.
- [ ] Run focused tests, `npm run build:check`, lint, and commit `feat(web): add LLMRouter developer docs`.

### Task 3: Browser Acceptance

**Files:** create `web/e2e/docs.spec.ts`.

- [ ] Write Playwright coverage for public render, language switching/persistence, copy feedback, live links, anchor refresh, and mobile navigation.
- [ ] Verify the test fails against current production 404.
- [ ] Run against the new build and fix any defects.
- [ ] Run `scripts/verify-web.sh` plus docs/client-route e2e tests and commit `test(web): certify developer docs`.

### Task 4: Parallel Deployment and Certification

- [ ] Run web, CLI, onboarding, and diff verification.
- [ ] Push `feat/llmrouter-docs` to private `NeoMei/llmrouter`.
- [ ] Build a revision-labelled image on the server.
- [ ] Start a candidate on a temporary loopback port with existing protected env/volumes/network; certify before replacing only `llmapi`.
- [ ] Verify public health, docs, anchor, install, model, route, authenticated model-list, bounded inference, and Playwright acceptance.
- [ ] Confirm `llmapi.quukk.com` remains unchanged and keep the previous LLMRouter container/image for rollback.

