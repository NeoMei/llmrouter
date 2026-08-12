# LLMRouter Developer Documentation Design

**Date:** 2026-08-12

**Status:** Approved in design review; pending written-spec review

## Objective

Replace the current client-side 404 at `/docs` with a first-party, public developer documentation page for LLMRouter. The page must explain how to connect applications and supported AI clients to `https://llmrouter.quukk.com` without requiring authentication to read the documentation.

The documentation is bilingual Chinese/English, static, safe to cache, and useful when the live model catalog is unavailable. Information that changes with account, model routing, price, quota, or administrator configuration is intentionally excluded and linked to the appropriate live dashboard page instead.

## User Experience

`/docs` is a single-page developer center using the existing application shell, visual system, components, typography, and responsive breakpoints. It does not introduce a second documentation framework or a new runtime dependency.

The desktop layout uses a sticky section navigation alongside the main article. On mobile, the navigation collapses into a compact section selector. Sections use stable anchor IDs so links such as `/docs#authentication` and `/docs#tool-calling` remain shareable.

The page provides:

- a Chinese/English language switch;
- Chinese as the default for Chinese browser locales and English otherwise;
- persistence of the selected language in browser local storage;
- copy buttons for commands and code examples;
- accessible headings, landmarks, focus states, and code labels;
- direct links to the live model catalog, client model routes, and installer page.

The language switch changes documentation copy only. Protocol names, JSON property names, command names, model identifiers in examples, and environment variable names remain unchanged.

## Content Structure

The page contains the following sections in both languages:

1. **Overview and quick start**
   - LLMRouter purpose and compatibility boundary.
   - Production origin: `https://llmrouter.quukk.com`.
   - OpenAI-compatible base URL: `https://llmrouter.quukk.com/v1`.
   - A minimal authenticated model-list request and one bounded inference request.

2. **Authentication and security**
   - `Authorization: Bearer` usage.
   - Environment-variable examples that avoid command-line token exposure.
   - Explicit warnings not to commit, log, paste into URLs, or embed tokens in browser-delivered code.

3. **Supported API protocols**
   - OpenAI Chat Completions at `/v1/chat/completions`.
   - OpenAI Responses at `/v1/responses`.
   - Anthropic Messages at `/v1/messages`.
   - A short selection guide that describes client/protocol fit without claiming model-specific support.

4. **Streaming**
   - Server-sent event expectations, termination behavior, and client cancellation.
   - Static examples for curl, Python, and Node.js.

5. **Tool calling**
   - Request/response lifecycle, tool-call IDs, and client-side execution responsibility.
   - Bounded examples without external side effects.

6. **Image input**
   - Supported request shapes and safe payload guidance.
   - A clear statement that image capability is model-dependent, followed by a link to the live model page rather than a static capability table.

7. **Reasoning continuity**
   - High-level guidance for Responses continuity, previous-response identifiers, and client-managed conversation context.
   - No undocumented provider internals or guarantees.

8. **SDK examples**
   - curl, OpenAI Python SDK, OpenAI JavaScript/TypeScript SDK, and Anthropic SDK examples.
   - Examples use environment variables and clearly illustrative model identifiers rather than an asserted current production catalog.

9. **Client integration**
   - Codex CLI and Codex Desktop.
   - Claude Code and Claude Desktop, explicitly distinguishing direct Messages access from MCP tool access.
   - OpenCode, Continue, Aider, and generic OpenAI-compatible clients.
   - Links to `/install/` for managed scripts and onboarding.

10. **Models and routing**
    - Static explanation of public model IDs, protocol compatibility, client-specific aliases, and virtual/failover routes.
    - Link to `/dashboard/models` for the current account-visible catalog.
    - Link to `/client-model-routes` for current client routing decisions.
    - No static list of models, prices, multipliers, quotas, context limits, or per-model capabilities.

11. **Errors and troubleshooting**
    - Common HTTP status meanings: 400, 401, 403, 404, 408, 429, 5xx.
    - Request ID collection, timeout guidance, URL normalization, and protocol mismatch checks.
    - `/healthz` as a connectivity check, clearly separated from authenticated API readiness.

12. **Installation and next steps**
    - Linux, macOS, and PowerShell entry points from `/install/`.
    - Links to model and client-route pages.

## Static Data Boundary

The documentation page does not call `/v1/models`, management APIs, user APIs, pricing APIs, or routing APIs during page load. It does not request, accept, retain, validate, or transmit an API token.

The following live information must not be rendered as embedded documentation data:

- account-visible models;
- model prices, ratios, quotas, or limits;
- model-to-protocol capability declarations;
- channel health, route priority, or failover state;
- client-specific route decisions.

Instead, the page links to the live dashboard surfaces. Unauthenticated visitors can read all static guidance; protected links may naturally redirect them to sign in.

## Frontend Architecture

The implementation adds a public TanStack Router route at `web/src/routes/docs.tsx` and a focused feature module under `web/src/features/docs/`.

The feature module is divided into:

- `docs-page.tsx`: page shell, responsive navigation, language switch, and section rendering;
- `docs-content.ts`: typed bilingual content and code-example data;
- `code-block.tsx`: labelled code display and clipboard action;
- `docs-page.test.tsx`: route-independent component behavior tests.

The route file contains only route registration and metadata. Documentation content is local static TypeScript data so both languages remain reviewable and type-checked. The page reuses existing buttons, cards, separators, scroll-area or layout primitives where appropriate.

No Markdown runtime, syntax-highlighting dependency, remote documentation service, or OpenAPI renderer is added. Code blocks prioritize legibility, copy accuracy, and low bundle cost over full syntax highlighting.

## Language Behavior

The language state accepts only `zh-CN` and `en`. Resolution order is:

1. a valid persisted documentation-language preference;
2. Chinese when `navigator.language` starts with `zh`;
3. English otherwise.

Invalid or unavailable local storage falls back without blocking rendering. The selected language is stored only after user action. Switching language preserves the current anchor and scroll position.

## Clipboard and Error Handling

Copy buttons use the browser Clipboard API. Successful copy produces a short, accessible confirmation state. If clipboard access is unavailable or denied, the code remains selectable and the control reports that manual copying is required. Clipboard failures do not throw a page-level error or log secrets.

Broken protected-page links are not preflighted. External SDK links, if included, open safely with appropriate `rel` attributes. The page renders entirely from bundled data and therefore has no loading or API-error state.

## Routing and Server Behavior

TanStack Router generates the public `/docs` route and the embedded Go server continues serving the SPA fallback for direct navigation. No nginx-only redirect or standalone server HTML is the source of truth.

The production nginx configuration must continue forwarding `/docs` to the LLMRouter application on port 13100 through the public 8445 origin. The existing `/install/` static location remains independent.

After deployment:

- `GET https://llmrouter.quukk.com/docs` returns the application shell with HTTP 200;
- the client router renders the developer documentation rather than the not-found component;
- refreshing `/docs#tool-calling` renders the same section correctly;
- `llmapi.quukk.com` remains routed to the parallel NewAPI system and is not modified.

## Testing and Acceptance

### Component and route tests

- `/docs` is registered as a public route.
- both Chinese and English content include every required section.
- no documentation content or component contains a production token or a live model/price table.
- default locale resolution and persisted preference behavior are deterministic.
- language switching updates visible content and preserves the anchor.
- copy success and denied-clipboard fallback are covered.
- links target `/install/`, `/dashboard/models`, and `/client-model-routes` correctly.

### Build and regression checks

- run the focused docs tests;
- run the web validation/build checks;
- run route generation/type checking;
- run existing frontend tests affected by the route tree.

### Browser acceptance

The tested flow is: open `/docs` directly -> verify meaningful documentation renders -> switch language -> copy an example -> follow the installer/model-routing links -> refresh an anchored section.

Validate desktop and mobile viewports, page identity, no not-found/framework overlay, console health, visible responsive navigation, language persistence, copy feedback, and link behavior. Capture screenshots for desktop Chinese, desktop English, and mobile navigation.

### Production acceptance

Build a new LLMRouter image from the reviewed commit, deploy it in parallel without changing `llmapi.quukk.com`, and verify:

- `/healthz` remains 200;
- `/docs` renders documentation after JavaScript execution;
- the rendered page contains no 404 text;
- `/install/` remains 200;
- `/dashboard/models` and `/client-model-routes` links resolve;
- one authenticated `/v1/models` request and one bounded inference request still succeed;
- rollback retains the previous container/image and nginx configuration.

## Non-Goals

- An OpenAPI/Swagger endpoint or generated schema explorer.
- Live token entry, connection testing, or model discovery inside `/docs`.
- Static copies of dynamic model, pricing, quota, or route data.
- A standalone documentation site or separate deployment.
- Search indexing across multiple documentation routes.
- Replacing `/install/` onboarding or dashboard model-management pages.
