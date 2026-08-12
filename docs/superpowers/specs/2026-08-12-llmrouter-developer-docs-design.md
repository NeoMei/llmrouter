# LLMRouter Connection Guide Design

**Date:** 2026-08-12

**Status:** Approved

## Objective

Replace the client-side 404 at `/docs` with a public bilingual connection guide for LLMRouter. The page explains how an Agent user gets a Token, chooses a model, configures a supported client, verifies the connection, and resolves common failures.

## Product Requirements

- Public single-page `/docs` route using the existing application shell and design system.
- Chinese and English language switch. Persist only explicit selection; default from browser locale.
- Seven concise sections: start, get a Token, connection address, choose a model, configure an Agent, verify the connection, and troubleshooting.
- Agent configuration covers Codex CLI/Desktop, Claude Code/Desktop, OpenCode, Continue, and Aider. Claude Desktop is labelled as MCP tool access.
- Use the existing `/install/` scripts instead of duplicating installer logic in the page.
- Link current models to `/dashboard/models`, Token management to `/keys`, and route management to `/client-model-routes`.
- Do not include SDK tutorials, API protocol tutorials, streaming, tool calling, image input, reasoning internals, prices, quotas, static model lists, or system migration history.
- Do not add dependencies.
- Public content uses only the LLMRouter product name and `llmrouter.quukk.com` domain.

## Architecture

Add `web/src/routes/docs.tsx` and a small module under `web/src/features/docs/`:

- `docs-content.ts`: concise bilingual static data;
- `docs-locale.ts`: deterministic locale resolution and safe local-storage persistence;
- `docs-page.tsx`: page, native code-copy buttons, and responsive navigation;
- focused Node tests and Playwright acceptance coverage.

The route is served through the existing SPA fallback and embedded Go web assets. `/install/` remains an independent static location.

## Acceptance

- `/docs` renders real documentation rather than the 404 component.
- `/docs#configure-agent` works on direct navigation and refresh.
- Language selection persists after explicit switching.
- Copy buttons work for the Base URL and one minimal connection-check command; the text remains selectable on clipboard failure.
- Desktop and mobile layouts pass rendered browser QA with no relevant console errors.
- `/healthz`, `/install/`, `/keys`, `/dashboard/models`, `/client-model-routes`, and authenticated `/v1/models` remain healthy after deployment.
- The prior LLMRouter image remains available for rollback.
