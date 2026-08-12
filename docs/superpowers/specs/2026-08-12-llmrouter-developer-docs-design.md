# LLMRouter Developer Documentation Design

**Date:** 2026-08-12

**Status:** Approved

## Objective

Replace the client-side 404 at `/docs` with a public bilingual developer center for LLMRouter. The page is static, safe to cache, and readable without authentication. It explains how to use `https://llmrouter.quukk.com` without requesting or retaining an API token.

## Product Requirements

- Public single-page `/docs` route using the existing application shell and design system.
- Chinese and English language switch. Persist only explicit selection; default from browser locale.
- Stable section anchors, responsive desktop/mobile navigation, selectable code blocks, and accessible copy feedback.
- Complete sections: quick start, authentication, Chat Completions, Responses, Anthropic Messages, streaming, tool calling, image input, reasoning continuity, SDK examples, client integration, models/routing concepts, errors/troubleshooting, and installation.
- Static documentation only. Do not load or embed model catalogs, pricing, ratios, quotas, context limits, channel state, routing state, or per-model capabilities.
- Link dynamic information to `/dashboard/models` and `/client-model-routes` and onboarding to `/install/`.
- Do not add Markdown, syntax-highlighting, OpenAPI, or documentation-framework dependencies.
- Keep `llmapi.quukk.com` on the parallel legacy system.

## Architecture

Add `web/src/routes/docs.tsx` and a focused module under `web/src/features/docs/`:

- `docs-content.ts`: typed bilingual static data;
- `docs-locale.ts`: deterministic locale resolution and safe local-storage persistence;
- `code-block.tsx`: labelled code display and clipboard feedback;
- `docs-page.tsx`: responsive navigation and content rendering;
- focused Node tests and Playwright acceptance coverage.

The route is served through the existing SPA fallback and embedded Go web assets. Production nginx continues forwarding `/docs` through the LLMRouter application on origin port 8445. `/install/` remains an independent static location.

## Acceptance

- `/docs` renders real documentation rather than the 404 component.
- `/docs#tool-calling` works on direct navigation and refresh.
- Language selection persists after explicit switching.
- Clipboard success and denied-clipboard fallback are accessible.
- Desktop and mobile layouts pass rendered browser QA with no relevant console errors.
- `/healthz`, `/install/`, `/dashboard/models`, `/client-model-routes`, authenticated `/v1/models`, and one bounded inference request remain healthy after deployment.
- The prior LLMRouter container/image remains available for rollback.

