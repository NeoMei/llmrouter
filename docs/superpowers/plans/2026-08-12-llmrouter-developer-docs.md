# LLMRouter Connection Guide Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task.

**Goal:** Build, test, and deploy a concise bilingual `/docs` guide for connecting Agents to LLMRouter with a Token.

**Architecture:** A public TanStack Router route renders seven static bilingual sections through one small React feature and existing UI primitives. Test content and locale contracts first, then the route/page, followed by Playwright acceptance and isolated deployment.

**Tech Stack:** React 19, TypeScript, TanStack Router, Tailwind CSS, Node test runner through `tsx --test`, Playwright, Go embedded web assets, Docker, nginx.

## Global Constraints

- `/docs` is public and readable without authentication.
- Support exactly `zh-CN` and `en`; Chinese browser locales default to Chinese and all others to English.
- Persist language only after explicit user action.
- Do not call live APIs from the docs page or accept a Token in the page.
- Do not embed SDK tutorials, API protocol tutorials, streaming, tools, images, reasoning internals, model lists, prices, quotas, capabilities, or system history.
- Link Token management to `/keys`, models to `/dashboard/models`, routes to `/client-model-routes`, and installers to `/install/`.
- Use `https://llmrouter.quukk.com` and `https://llmrouter.quukk.com/v1` in examples.
- Reuse existing dependencies and design system.
- Public content must not mention any previous product name or migration system.
- Retain rollback state during deployment.

### Task 1: Concise Bilingual Connection Content and Locale Policy

**Files:** create `web/src/features/docs/docs-content.ts`, `docs-content.test.ts`, `docs-locale.ts`, and `docs-locale.test.ts`.

**Interfaces:** export `DOCS_LANGUAGES`, `DocsLanguage`, `DocsContent`, `docsContent`, `DOCS_LANGUAGE_STORAGE_KEY`, `resolveDocsLanguage`, and `persistDocsLanguage`.

- [x] Write failing Node tests for exactly seven section IDs (`start`, `get-token`, `connection-address`, `choose-model`, `configure-agent`, `verify`, `troubleshooting`), required Agent names, internal links, forbidden scope/legacy terms, locale precedence, and storage failure.
- [x] Run tests and confirm failure because modules do not exist.
- [x] Implement concise Chinese/English static content and exact storage key `llmrouter.docs.language`.
- [x] Run focused tests and commit `feat(docs): define bilingual developer content`.

### Task 2: Responsive Page, Clipboard, and Route

**Files:** create `web/src/features/docs/docs-page.tsx`, `index.ts`, `docs-page.test.ts`, and `web/src/routes/docs.tsx`; update generated route tree through build.

**Interfaces:** public `DocsPage`, `/docs` route, seven stable anchors, two labelled copy controls, and responsive navigation.

- [x] Write failing source-contract tests for route registration, `PublicLayout`, seven anchors, four internal links, no API imports, public naming, and native clipboard fallback.
- [x] Run tests and confirm failure.
- [x] Implement one data-driven page with locale switch, compact desktop/mobile navigation, selectable code, native copy feedback, and route.
- [x] Run focused tests, `npm run build:check`, lint, and commit `feat(web): add LLMRouter developer docs`.

### Task 3: Browser Acceptance

**Files:** create `web/e2e/docs.spec.ts`.

- [x] Write Playwright coverage for public render, language switching/persistence, Base URL copy, Agent section, internal links, anchor refresh, and mobile navigation.
- [x] Verify the test fails against current production 404.
- [x] Run against the new build and fix any defects.
- [x] Run `scripts/verify-web.sh` plus docs/client-route e2e tests and commit `test(web): certify developer docs`.

### Task 4: Parallel Deployment and Certification

- [x] Run web, CLI, onboarding, and diff verification.
- [x] Push `feat/llmrouter-docs` to private `NeoMei/llmrouter`.
- [x] Build a revision-labelled image on the server.
- [x] Start a candidate on a temporary loopback port with existing protected env, volumes, and network; certify before replacing the current LLMRouter service.
- [x] Verify public health, docs, Agent anchor, install, Token/model/route links, authenticated model-list, and Playwright acceptance.
- [x] Keep the previous LLMRouter service and image available for rollback.
